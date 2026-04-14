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

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

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
	identity, _ := age.GenerateX25519Identity()
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
	identity, _ := age.GenerateX25519Identity()
	err := Run(context.Background(), source,
		filepath.Join(tmp, "no-such-dir", "out.age"),
		[]age.Recipient{identity.Recipient()},
	)
	if err == nil {
		t.Fatalf("Run() expected error on unwritable dest, got nil")
	}
}

func TestRunEmptyPathsRejected(t *testing.T) {
	identity, _ := age.GenerateX25519Identity()
	if err := Run(context.Background(), "", "/tmp/x.age", []age.Recipient{identity.Recipient()}); err == nil {
		t.Fatalf("expected error on empty source")
	}
	if err := Run(context.Background(), "/tmp/x.db", "", []age.Recipient{identity.Recipient()}); err == nil {
		t.Fatalf("expected error on empty dest")
	}
}

func TestParseRecipientsAcceptsLiteralKey(t *testing.T) {
	identity, _ := age.GenerateX25519Identity()
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
	id1, _ := age.GenerateX25519Identity()
	id2, _ := age.GenerateX25519Identity()
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
