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
// through age.Encrypt. The temporary plaintext file is removed on both
// success and failure paths. destPath is written with 0600 permissions.
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
	if _, err := os.Stat(sourceDB); err != nil {
		return fmt.Errorf("stat source db: %w", err)
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

	return encryptToFile(snapPath, destPath, recipients)
}

func vacuumSnapshot(ctx context.Context, sourceDB, snapPath string) error {
	db, err := sql.Open("sqlite", sourceDB)
	if err != nil {
		return fmt.Errorf("open source db: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapPath); err != nil {
		return fmt.Errorf("vacuum into snapshot: %w", err)
	}
	return nil
}

func encryptToFile(snapPath, destPath string, recipients []age.Recipient) error {
	src, err := os.Open(snapPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	enc, err := age.Encrypt(dst, recipients...)
	if err != nil {
		_ = dst.Close()
		_ = os.Remove(destPath)
		return fmt.Errorf("start age encryption: %w", err)
	}

	if _, err := io.Copy(enc, src); err != nil {
		_ = enc.Close()
		_ = dst.Close()
		_ = os.Remove(destPath)
		return fmt.Errorf("encrypt copy: %w", err)
	}
	if err := enc.Close(); err != nil {
		_ = dst.Close()
		_ = os.Remove(destPath)
		return fmt.Errorf("finalize age encryption: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("close destination: %w", err)
	}
	return nil
}

// ParseRecipients accepts a slice where each entry is either a raw age
// recipient (starts with "age1") or a path to a recipients file (one key per
// line, # comments allowed — the format consumed by age.ParseRecipients).
// At least one valid recipient must be resolved or an error is returned.
func ParseRecipients(inputs []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, s := range inputs {
		s = strings.TrimSpace(s)
		if s == "" {
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
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no recipients resolved from input")
	}
	return out, nil
}
