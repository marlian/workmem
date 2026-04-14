package backup

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	_ "modernc.org/sqlite"
)

// newIdentity generates a fresh X25519 age identity for the test, failing
// loudly if the rand source is broken rather than silently handing back a
// nil identity whose Recipient() call would panic later.
func newIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	return id
}

// seedSourceDB creates a minimal SQLite database with a single table and a
// couple of rows so the round-trip test has something to compare after
// decryption.
func seedSourceDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO notes (body) VALUES (?), (?)`, "first row", "second row"); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
}

func TestRunRoundTripDecryptsToReadableDatabase(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	dest := filepath.Join(tmp, "backup.age")
	seedSourceDB(t, source)

	identity := newIdentity(t)

	if err := Run(context.Background(), source, dest, []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("destination file is empty")
	}

	encFile, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open dest for decrypt: %v", err)
	}
	defer encFile.Close()

	plainReader, err := age.Decrypt(encFile, identity)
	if err != nil {
		t.Fatalf("age.Decrypt error = %v", err)
	}

	restored := filepath.Join(tmp, "restored.db")
	rf, err := os.Create(restored)
	if err != nil {
		t.Fatalf("create restored: %v", err)
	}
	if _, err := io.Copy(rf, plainReader); err != nil {
		_ = rf.Close()
		t.Fatalf("copy decrypted: %v", err)
	}
	if err := rf.Close(); err != nil {
		t.Fatalf("close restored: %v", err)
	}

	rdb, err := sql.Open("sqlite", restored)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer rdb.Close()

	rows, err := rdb.Query(`SELECT body FROM notes ORDER BY id`)
	if err != nil {
		t.Fatalf("query restored: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, body)
	}
	want := []string{"first row", "second row"}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRunFailsOnMissingSource(t *testing.T) {
	tmp := t.TempDir()
	identity := newIdentity(t)
	err := Run(context.Background(),
		filepath.Join(tmp, "does-not-exist.db"),
		filepath.Join(tmp, "out.age"),
		[]age.Recipient{identity.Recipient()},
	)
	if err == nil {
		t.Fatalf("Run() expected error on missing source, got nil")
	}
	if !strings.Contains(err.Error(), "stat source db") {
		t.Fatalf("error = %v, want mention of stat source db", err)
	}
}

func TestRunFailsOnZeroRecipients(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	seedSourceDB(t, source)
	err := Run(context.Background(), source, filepath.Join(tmp, "out.age"), nil)
	if err == nil {
		t.Fatalf("Run() expected error on zero recipients, got nil")
	}
	if !strings.Contains(err.Error(), "at least one age recipient") {
		t.Fatalf("error = %v, want recipient-required message", err)
	}
}

func TestRunFailsOnUnwritableDestination(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	seedSourceDB(t, source)
	identity := newIdentity(t)
	err := Run(context.Background(), source,
		filepath.Join(tmp, "no-such-dir", "out.age"),
		[]age.Recipient{identity.Recipient()},
	)
	if err == nil {
		t.Fatalf("Run() expected error on unwritable dest, got nil")
	}
}

func TestRunEmptyPathsRejected(t *testing.T) {
	identity := newIdentity(t)
	if err := Run(context.Background(), "", "/tmp/x.age", []age.Recipient{identity.Recipient()}); err == nil {
		t.Fatalf("expected error on empty source")
	}
	if err := Run(context.Background(), "/tmp/x.db", "", []age.Recipient{identity.Recipient()}); err == nil {
		t.Fatalf("expected error on empty dest")
	}
}

func TestParseRecipientsAcceptsLiteralKey(t *testing.T) {
	identity := newIdentity(t)
	pub := identity.Recipient().String()
	out, err := ParseRecipients([]string{pub})
	if err != nil {
		t.Fatalf("ParseRecipients error = %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 recipient, got %d", len(out))
	}
}

func TestParseRecipientsAcceptsFilePath(t *testing.T) {
	tmp := t.TempDir()
	id1 := newIdentity(t)
	id2 := newIdentity(t)
	path := filepath.Join(tmp, "recipients.txt")
	content := "# comment line\n" + id1.Recipient().String() + "\n" + id2.Recipient().String() + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	out, err := ParseRecipients([]string{path})
	if err != nil {
		t.Fatalf("ParseRecipients error = %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 recipients, got %d", len(out))
	}
}

func TestParseRecipientsRejectsInvalidKey(t *testing.T) {
	_, err := ParseRecipients([]string{"age1-invalid"})
	if err == nil {
		t.Fatalf("expected error on invalid recipient literal")
	}
}

func TestParseRecipientsRejectsEmptyInput(t *testing.T) {
	_, err := ParseRecipients([]string{"", "  "})
	if err == nil {
		t.Fatalf("expected error when only whitespace recipients")
	}
}

func TestRunRejectsDestEqualToSource(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	seedSourceDB(t, source)
	identity := newIdentity(t)

	err := Run(context.Background(), source, source, []age.Recipient{identity.Recipient()})
	if err == nil {
		t.Fatalf("Run() expected error when destPath == sourceDB, got nil")
	}
	if !strings.Contains(err.Error(), "same as source") {
		t.Fatalf("error = %v, want 'same as source' guard", err)
	}
}

func TestRunLeavesNoTempFileAfterSuccess(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	dest := filepath.Join(tmp, "backup.age")
	seedSourceDB(t, source)
	identity := newIdentity(t)

	if err := Run(context.Background(), source, dest, []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The atomic-write pattern uses a sibling ".tmp-*" file and renames it
	// onto dest on success. After a successful Run the dest dir must
	// contain only the source DB and backup.age — no orphan temp file.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "memory.db" || name == "backup.age" {
			continue
		}
		t.Fatalf("unexpected file %q in dest dir — atomic-write cleanup leaked", name)
	}
}

func TestRunAtomicWriteReplacesExistingBackupOnSecondRun(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	dest := filepath.Join(tmp, "backup.age")
	seedSourceDB(t, source)

	id1 := newIdentity(t)
	id2 := newIdentity(t)

	// First backup with id1
	if err := Run(context.Background(), source, dest, []age.Recipient{id1.Recipient()}); err != nil {
		t.Fatalf("Run() #1 error = %v", err)
	}
	// Second backup with id2 — should atomically replace, not append or merge
	if err := Run(context.Background(), source, dest, []age.Recipient{id2.Recipient()}); err != nil {
		t.Fatalf("Run() #2 error = %v", err)
	}

	// The final dest must decrypt with id2 (new) and NOT with id1 (old).
	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer f.Close()
	if _, err := age.Decrypt(f, id2); err != nil {
		t.Fatalf("dest should decrypt with new identity after atomic replace: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := age.Decrypt(f, id1); err == nil {
		t.Fatalf("dest unexpectedly decrypted with old identity — rename did not fully replace prior content")
	}
}

func TestRunRejectsDestResolvingToSameFile(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "memory.db")
	seedSourceDB(t, source)

	// Hard-link dest onto source so the two distinct paths share an inode.
	// os.SameFile should catch this even though filepath.Abs comparison does
	// not.
	linked := filepath.Join(tmp, "aliased.db")
	if err := os.Link(source, linked); err != nil {
		t.Skipf("os.Link not supported on this filesystem: %v", err)
	}

	identity := newIdentity(t)
	err := Run(context.Background(), source, linked, []age.Recipient{identity.Recipient()})
	if err == nil {
		t.Fatalf("Run() expected error when dest hard-links to source, got nil")
	}
	if !strings.Contains(err.Error(), "same file as source") {
		t.Fatalf("error = %v, want 'same file as source' guard", err)
	}
}
