package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashString returns sha256:<hex> for a non-empty input, or "" for empty input.
func hashString(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// hashIfStrict returns s unchanged unless strict is true, in which case s is
// replaced with its sha256 hash (prefixed with "sha256:"). Empty input always
// returns empty.
func hashIfStrict(s string, strict bool) string {
	if !strict {
		return s
	}
	return hashString(s)
}
