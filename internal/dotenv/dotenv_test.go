package dotenv

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "simple assignment",
			input: "FOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "double quoted value",
			input: `FOO="bar baz"`,
			want:  map[string]string{"FOO": "bar baz"},
		},
		{
			name:  "single quoted value",
			input: `FOO='bar baz'`,
			want:  map[string]string{"FOO": "bar baz"},
		},
		{
			name:  "inline comment stripped after whitespace",
			input: "FOO=bar # trailing comment",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "hash without preceding whitespace is literal",
			input: "FOO=bar#baz",
			want:  map[string]string{"FOO": "bar#baz"},
		},
		{
			name:  "hash inside double quotes is literal",
			input: `FOO="bar # still quoted"`,
			want:  map[string]string{"FOO": "bar # still quoted"},
		},
		{
			name:  "hash inside single quotes is literal",
			input: `FOO='bar # still quoted'`,
			want:  map[string]string{"FOO": "bar # still quoted"},
		},
		{
			name:  "export prefix stripped",
			input: "export FOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "export with extra whitespace",
			input: "export   FOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "full-line comment skipped",
			input: "# this is a comment\nFOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "empty value is preserved",
			input: "FOO=",
			want:  map[string]string{"FOO": ""},
		},
		{
			name:  "CRLF line endings",
			input: "FOO=bar\r\nBAZ=qux\r\n",
			want:  map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:  "UTF-8 BOM at file start",
			input: "\uFEFFFOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "BOM combined with CRLF",
			input: "\uFEFFFOO=bar\r\n",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "blank lines between entries",
			input: "FOO=bar\n\n\nBAZ=qux\n",
			want:  map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:  "key starting with digit is rejected",
			input: "1FOO=bar",
			want:  map[string]string{},
		},
		{
			name:  "key with dash is rejected",
			input: "FOO-BAR=baz",
			want:  map[string]string{},
		},
		{
			name:  "key with dot is rejected",
			input: "FOO.BAR=baz",
			want:  map[string]string{},
		},
		{
			name:  "line without equals is skipped",
			input: "not an assignment\nFOO=bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "unterminated double quote skips line",
			input: `FOO="unterminated` + "\nBAR=ok",
			want:  map[string]string{"BAR": "ok"},
		},
		{
			name:  "unterminated single quote skips line",
			input: "FOO='unterminated\nBAR=ok",
			want:  map[string]string{"BAR": "ok"},
		},
		{
			name:  "trailing whitespace on unquoted value trimmed",
			input: "FOO=bar   \t",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "leading whitespace after equals trimmed",
			input: "FOO=   bar",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "multiple equals in value preserved after first",
			input: "FOO=a=b=c",
			want:  map[string]string{"FOO": "a=b=c"},
		},
		{
			name:  "trailing garbage after closing quote ignored",
			input: `FOO="hello" and then garbage # comment`,
			want:  map[string]string{"FOO": "hello"},
		},
		{
			name:  "underscore-prefixed key accepted",
			input: "_FOO=bar",
			want:  map[string]string{"_FOO": "bar"},
		},
		{
			name:  "numeric chars inside key accepted",
			input: "FOO123=bar",
			want:  map[string]string{"FOO123": "bar"},
		},
		{
			name:  "empty input returns empty map",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "whitespace-only value becomes empty",
			input: "FOO=   \t   ",
			want:  map[string]string{"FOO": ""},
		},
		{
			name:  "unicode inside quoted value preserved",
			input: `FOO="caffè — β"`,
			want:  map[string]string{"FOO": "caffè — β"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestLoadEmptyPath(t *testing.T) {
	if err := Load(""); err != nil {
		t.Fatalf("Load(\"\") = %v, want nil", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.env")
	if err := Load(missing); err != nil {
		t.Fatalf("Load(missing) = %v, want nil (ENOENT silence)", err)
	}
}

func TestLoadReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-denied read not reproducible the same way on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.env")
	if err := os.WriteFile(path, []byte("FOO=bar"), 0o000); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission check")
	}
	err := Load(path)
	if err == nil {
		t.Fatalf("Load() on permission-denied file = nil, want error")
	}
}

func TestLoadFillsUnsetKeys(t *testing.T) {
	const key = "WORKMEM_DOTENV_TEST_FILLS_UNSET"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })

	path := writeEnvFile(t, key+"=from-dotenv")
	if err := Load(path); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := os.Getenv(key); got != "from-dotenv" {
		t.Errorf("os.Getenv(%q) = %q, want %q", key, got, "from-dotenv")
	}
}

func TestLoadProcessEnvWinsOverDotenv(t *testing.T) {
	const key = "WORKMEM_DOTENV_TEST_PRECEDENCE"
	t.Setenv(key, "from-env")

	path := writeEnvFile(t, key+"=from-dotenv")
	if err := Load(path); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := os.Getenv(key); got != "from-env" {
		t.Errorf("os.Getenv(%q) = %q, want %q (process env must win)", key, got, "from-env")
	}
}

func TestLoadEmptyStringInEnvStillWins(t *testing.T) {
	// Document LookupEnv semantics: an explicitly-set empty string counts as
	// "set" and is not overridden by the .env value. Matches Node's
	// process.env[k] === undefined check.
	const key = "WORKMEM_DOTENV_TEST_EMPTY_WINS"
	t.Setenv(key, "")

	path := writeEnvFile(t, key+"=from-dotenv")
	if err := Load(path); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := os.Getenv(key); got != "" {
		t.Errorf("os.Getenv(%q) = %q, want empty string (LookupEnv treats empty as set)", key, got)
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	return path
}
