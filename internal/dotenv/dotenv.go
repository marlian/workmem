// Package dotenv loads values from a .env file into os.Environ().
//
// The parser mirrors the zero-dependency loader in the reference Node server
// (mcp-memory/server.js). Process env precedence is honored via os.LookupEnv:
// a key already present in the environment is never overwritten, even if its
// value is the empty string. A missing file is not an error (ENOENT silence).
//
// Supported syntax:
//
//	KEY=value                     # simple assignment
//	KEY="value with spaces"       # double quotes (no escapes, no interpolation)
//	KEY='value with spaces'       # single quotes (no escapes, no interpolation)
//	KEY=value # trailing comment  # inline comment (unquoted only, needs whitespace before #)
//	KEY="value # literal hash"    # hash is literal inside quotes
//	export KEY=value              # bash-style export prefix (allowed, stripped)
//	# full-line comment           # skipped
//	KEY=                          # empty string (key present, value empty)
//
// NOT supported: variable interpolation (${OTHER}), multi-line values,
// escape sequences inside quotes.
//
// Lines that don't match KEY=VALUE shape are skipped silently.
package dotenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Parse converts .env file content into a map of key/value pairs.
// Malformed lines are skipped silently (matches the reference Node parser).
func Parse(content string) map[string]string {
	result := map[string]string{}

	// Strip UTF-8 BOM if present (some Windows editors add it).
	content = strings.TrimPrefix(content, "\uFEFF")

	// Split on LF; trailing \r on each line is stripped below (CRLF).
	for rawLine := range strings.SplitSeq(content, "\n") {
		rawLine = strings.TrimSuffix(rawLine, "\r")
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Allow optional 'export ' prefix (bash-compatible).
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimLeft(line[len("export "):], " \t")
		}

		keyRaw, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key := strings.TrimSpace(keyRaw)
		if !isValidKey(key) {
			continue
		}

		// Drop leading whitespace after =; trailing handling depends on quoting.
		rest := strings.TrimLeft(rawValue, " \t")

		var value string
		if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`) {
			quote := rest[0]
			closeIdx := strings.IndexByte(rest[1:], quote)
			if closeIdx == -1 {
				// Unterminated quote — skip line entirely.
				continue
			}
			value = rest[1 : 1+closeIdx]
			// Content after the closing quote is ignored (whitespace, comment, garbage).
		} else {
			// Unquoted: strip inline comment (whitespace followed by #), then trim trailing.
			if idx := findInlineCommentStart(rest); idx >= 0 {
				value = rest[:idx]
			} else {
				value = rest
			}
			value = strings.TrimRight(value, " \t")
		}

		result[key] = value
	}
	return result
}

// Load reads the file at path, parses it, and injects keys not already in
// the environment via os.Setenv. Existing env values win (LookupEnv-based
// precedence — empty-string-set-in-env also wins, matching Node semantics).
//
// Returns nil if the file does not exist (ENOENT silence). Returns an error
// for other read failures; callers typically log and continue.
// An empty path is treated as "don't load" and returns nil.
func Load(path string) error {
	if path == "" {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read env file %q: %w", path, err)
	}
	for key, value := range Parse(string(content)) {
		if _, ok := os.LookupEnv(key); ok {
			continue
		}
		if setErr := os.Setenv(key, value); setErr != nil {
			return fmt.Errorf("set env %q: %w", key, setErr)
		}
	}
	return nil
}

// isValidKey reports whether key is a valid env var identifier:
// matches the regex ^[A-Za-z_][A-Za-z0-9_]*$ without pulling regexp.
func isValidKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c == '_' ||
			(c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// findInlineCommentStart returns the index of an inline comment marker —
// whitespace (space or tab) immediately followed by #. Returns -1 if none.
// Matches the reference parser's /\s#/ intent (restricted here to space/tab
// since newlines cannot appear within a single parsed line).
func findInlineCommentStart(s string) int {
	for i := 0; i < len(s)-1; i++ {
		if (s[i] == ' ' || s[i] == '\t') && s[i+1] == '#' {
			return i
		}
	}
	return -1
}
