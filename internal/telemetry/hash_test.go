package telemetry

import (
	"strings"
	"testing"
)

func TestHashStringDeterministicAndPrefixed(t *testing.T) {
	a := hashString("hello")
	b := hashString("hello")
	if a != b {
		t.Fatalf("hashString not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("hashString missing sha256: prefix: %q", a)
	}
	if len(a) != len("sha256:")+64 {
		t.Fatalf("hashString unexpected length %d: %q", len(a), a)
	}
}

func TestHashStringEmptyInputReturnsEmpty(t *testing.T) {
	if got := hashString(""); got != "" {
		t.Fatalf("hashString(\"\") = %q, want empty", got)
	}
}

func TestHashIfStrict(t *testing.T) {
	if got := hashIfStrict("alpha", false); got != "alpha" {
		t.Fatalf("hashIfStrict(strict=false) should pass through, got %q", got)
	}
	got := hashIfStrict("alpha", true)
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("hashIfStrict(strict=true) should hash, got %q", got)
	}
	if got == "alpha" {
		t.Fatalf("hashIfStrict(strict=true) returned plaintext")
	}
}
