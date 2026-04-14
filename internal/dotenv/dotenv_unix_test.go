//go:build !windows

package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadReadError lives in a Unix-only file because mode 0o000 has no
// reliable equivalent on Windows: NTFS ACLs let the file owner read the file
// regardless of the POSIX-bit mode, so permission-denied cannot be provoked
// portably from Go.
func TestLoadReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission check")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.env")
	if err := os.WriteFile(path, []byte("FOO=bar"), 0o000); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	err := Load(path)
	if err == nil {
		t.Fatalf("Load() on permission-denied file = nil, want error")
	}
}
