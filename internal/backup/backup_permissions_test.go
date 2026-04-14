//go:build !windows

package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stat error other than fs.ErrNotExist must surface verbatim — if
// ParseRecipients silently fell through to literal parsing on EACCES, a
// misconfigured recipients file with restrictive permissions would produce
// the misleading "neither an existing file nor an age1 literal" error.
//
// POSIX-gated: on Windows, directory permissions do not block traversal the
// same way, and root on Linux bypasses permission checks entirely — so the
// test also skips when running as root.
func TestParseRecipientsSurfacesNonNotExistStatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permission checks; EACCES cannot be synthesized")
	}
	tmp := t.TempDir()
	// Create a subdir with a file inside, then strip all perms from the
	// subdir so os.Stat on the nested file fails with permission denied.
	blockDir := filepath.Join(tmp, "blocked")
	if err := os.Mkdir(blockDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nested := filepath.Join(blockDir, "recipients.txt")
	if err := os.WriteFile(nested, []byte("age1whatever\n"), 0o600); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	if err := os.Chmod(blockDir, 0o000); err != nil {
		t.Fatalf("chmod block dir: %v", err)
	}
	t.Cleanup(func() {
		// Restore perms so t.TempDir cleanup can remove the tree.
		_ = os.Chmod(blockDir, 0o700)
	})

	_, err := ParseRecipients([]string{nested})
	if err == nil {
		t.Fatalf("expected error on EACCES stat, got nil")
	}
	if strings.Contains(err.Error(), "neither an existing file nor an age1 literal") {
		t.Fatalf("EACCES stat fell through to literal-parse path: %v", err)
	}
	if !strings.Contains(err.Error(), "stat recipient path") {
		t.Fatalf("error = %v, want 'stat recipient path' prefix", err)
	}
}
