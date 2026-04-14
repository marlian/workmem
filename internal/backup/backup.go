// Package backup produces an age-encrypted SQLite snapshot of the workmem
// memory database. The snapshot is consistent (taken via VACUUM INTO, not a
// raw file copy) and the plaintext intermediate file never leaves the
// temporary directory.
//
// Restoration is deliberately left as a one-liner with the age CLI:
//
//	age -d -i <identity-file> <backup.age> > memory.db
//
// This keeps the CLI honest about its scope — backup is the side of the
// pipeline that has filesystem consistency concerns; restore is a plain
// decrypt and place-where-you-want.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	_ "modernc.org/sqlite"
)

// Run produces an age-encrypted consistent snapshot of sourceDB at destPath.
// The snapshot is created via VACUUM INTO into a temporary file, then streamed
// through age.Encrypt into a sibling temp file next to destPath, fsynced,
// and atomically renamed onto destPath. This guarantees destPath either
// contains the previous valid backup or the new valid backup — never a
// truncated halfway state, even if the process is interrupted mid-write.
// destPath is written with 0600 permissions (enforced via Chmod on the open
// file, so a pre-existing file with looser mode is tightened before
// sensitive ciphertext is written to it).
//
// destPath must not resolve to the same filesystem object as sourceDB —
// overwriting the live DB with its encrypted backup would corrupt it.
//
// At least one recipient is required. The caller is responsible for supplying
// recipients; this function has no notion of keychains or default keys.
func Run(ctx context.Context, sourceDB, destPath string, recipients []age.Recipient) error {
	if sourceDB == "" {
		return fmt.Errorf("source db path is empty")
	}
	if destPath == "" {
		return fmt.Errorf("destination path is empty")
	}
	if len(recipients) == 0 {
		return fmt.Errorf("at least one age recipient is required")
	}
	sourceInfo, err := os.Stat(sourceDB)
	if err != nil {
		return fmt.Errorf("stat source db: %w", err)
	}
	if err := rejectDestEqualsSource(sourceDB, destPath, sourceInfo); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "workmem-backup-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapPath := filepath.Join(tmpDir, "snap.db")

	if err := vacuumSnapshot(ctx, sourceDB, snapPath); err != nil {
		return err
	}

	return encryptToFile(ctx, snapPath, destPath, recipients)
}

// ctxReader wraps an io.Reader with a context so a cancelled context during
// a long io.Copy aborts promptly on the next Read instead of silently
// continuing to completion. The overhead is a single load per Read call.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// rejectDestEqualsSource refuses to proceed when destPath would overwrite
// sourceDB. Checks (in order): cleaned absolute path equality (covers the
// direct case including "./memory.db" vs "memory.db") and os.SameFile when
// destPath already exists (covers hard links and symlinks to the same inode).
// The exotic "dest is a symlink to source but does not yet exist" case is
// not covered — the VACUUM INTO step would fail later on the symlink target
// rather than corrupting anything, but no guarantees.
func rejectDestEqualsSource(sourceDB, destPath string, sourceInfo os.FileInfo) error {
	srcAbs, err := filepath.Abs(sourceDB)
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}
	dstAbs, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("resolve destination path: %w", err)
	}
	if srcAbs == dstAbs {
		return fmt.Errorf("destination path is the same as source db path: %s", sourceDB)
	}
	if destInfo, statErr := os.Stat(destPath); statErr == nil && sourceInfo != nil {
		if os.SameFile(sourceInfo, destInfo) {
			return fmt.Errorf("destination path resolves to the same file as source db path: %s -> %s", destPath, sourceDB)
		}
	}
	return nil
}

func vacuumSnapshot(ctx context.Context, sourceDB, snapPath string) error {
	db, err := sql.Open("sqlite", sourceDB)
	if err != nil {
		return fmt.Errorf("open source db: %w", err)
	}
	// Align with the main store's modernc.org/sqlite conventions: pin the
	// pool to a single connection for deterministic behavior, and Ping so
	// open failures surface here rather than midway through VACUUM INTO.
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping source db: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapPath); err != nil {
		return fmt.Errorf("vacuum into snapshot: %w", err)
	}
	return nil
}

func encryptToFile(ctx context.Context, snapPath, destPath string, recipients []age.Recipient) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	src, err := os.Open(snapPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer src.Close()

	// Write the ciphertext into a sibling temp file next to destPath, then
	// rename atomically. This preserves any existing destPath file as long
	// as the rename itself has not happened — a crash or Ctrl+C during
	// encryption leaves the old backup untouched. CreateTemp places the
	// file in the same directory so the rename is guaranteed to be on the
	// same filesystem (cross-device renames are not atomic).
	destDir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(destDir, filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp destination next to %s: %w", destPath, err)
	}
	tmpPath := tmp.Name()
	commit := false
	defer func() {
		if !commit {
			_ = os.Remove(tmpPath)
		}
	}()

	// CreateTemp uses 0600 by default, but be explicit for clarity and to
	// guard against umask weirdness or Windows semantics.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set destination permissions: %w", err)
	}

	enc, err := age.Encrypt(tmp, recipients...)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("start age encryption: %w", err)
	}

	if _, err := io.Copy(enc, &ctxReader{ctx: ctx, r: src}); err != nil {
		_ = enc.Close()
		_ = tmp.Close()
		return fmt.Errorf("encrypt copy: %w", err)
	}
	if err := enc.Close(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("finalize age encryption: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync destination: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename temp onto destination: %w", err)
	}
	// POSIX durability: the rename is atomic wrt other reads, but the
	// directory entry's persistence is not guaranteed until the containing
	// directory's fsync. On Windows, os.File.Sync on a directory returns
	// an error — we ignore it (best-effort) since the rename itself is
	// durable by the time ReplaceFile/MoveFileEx returns on NTFS.
	if d, err := os.Open(destDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	commit = true
	return nil
}

// ParseRecipients accepts a slice where each entry is either an age1...
// recipient literal or a path to a recipients file (one key per line, #
// comments allowed — the format consumed by age.ParseRecipients).
//
// Disambiguation: an input that exists on disk is treated as a file, even
// if the base name starts with "age1" (so "./age1-recipients.txt" works).
// An input that does not exist is parsed as an age1 literal. Anything
// that is neither is reported as a clear error.
//
// At least one valid recipient must be resolved or an error is returned.
func ParseRecipients(inputs []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, s := range inputs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, statErr := os.Stat(s); statErr == nil {
			f, err := os.Open(s)
			if err != nil {
				return nil, fmt.Errorf("open recipients file %q: %w", s, err)
			}
			rs, parseErr := age.ParseRecipients(f)
			_ = f.Close()
			if parseErr != nil {
				return nil, fmt.Errorf("parse recipients file %q: %w", s, parseErr)
			}
			out = append(out, rs...)
			continue
		}
		if strings.HasPrefix(s, "age1") {
			r, err := age.ParseX25519Recipient(s)
			if err != nil {
				return nil, fmt.Errorf("parse recipient %q: %w", s, err)
			}
			out = append(out, r)
			continue
		}
		return nil, fmt.Errorf("recipient %q is neither an existing file nor an age1 literal", s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no recipients resolved from input")
	}
	return out, nil
}
