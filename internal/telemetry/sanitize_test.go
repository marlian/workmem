package telemetry

import (
	"strings"
	"testing"
)

func TestSanitizeArgsStripsObservationContent(t *testing.T) {
	got := SanitizeArgs(map[string]any{
		"entity":      "Alice",
		"observation": "this is sensitive content that must never be logged",
	}, false)
	if strings.Contains(got, "sensitive content") {
		t.Fatalf("observation content leaked in args_summary: %s", got)
	}
	if !strings.Contains(got, "<51 chars>") {
		t.Fatalf("observation length marker missing: %s", got)
	}
	if !strings.Contains(got, "Alice") {
		t.Fatalf("entity should be plaintext in permissive mode: %s", got)
	}
}

func TestSanitizeArgsStripsFactsAndObservationsArrays(t *testing.T) {
	got := SanitizeArgs(map[string]any{
		"facts":        []any{map[string]any{}, map[string]any{}, map[string]any{}},
		"observations": []any{map[string]any{}, map[string]any{}},
	}, false)
	if !strings.Contains(got, "<3 facts>") {
		t.Fatalf("facts count marker missing: %s", got)
	}
	if !strings.Contains(got, "<2 observations>") {
		t.Fatalf("observations count marker missing: %s", got)
	}
}

func TestSanitizeArgsStrictModeHashesIdentifiers(t *testing.T) {
	got := SanitizeArgs(map[string]any{
		"entity": "Alice",
		"from":   "Bob",
		"to":     "Carol",
		"label":  "therapy session",
		"query":  "anxiety",
		"limit":  10,
	}, true)
	for _, leak := range []string{"Alice", "Bob", "Carol", "therapy session", "anxiety"} {
		if strings.Contains(got, leak) {
			t.Fatalf("strict mode leaked %q in args_summary: %s", leak, got)
		}
	}
	if !strings.Contains(got, "sha256:") {
		t.Fatalf("strict mode did not hash identifiers: %s", got)
	}
	if !strings.Contains(got, "\"limit\":10") {
		t.Fatalf("non-identifier fields should pass through in strict mode: %s", got)
	}
}

func TestSanitizeArgsPermissiveModeKeepsIdentifiers(t *testing.T) {
	got := SanitizeArgs(map[string]any{
		"entity": "Alice",
		"query":  "preferences",
	}, false)
	if !strings.Contains(got, "Alice") {
		t.Fatalf("permissive mode should keep entity plaintext: %s", got)
	}
	if !strings.Contains(got, "preferences") {
		t.Fatalf("permissive mode should keep query plaintext: %s", got)
	}
}

func TestSanitizeArgsNilReturnsEmpty(t *testing.T) {
	if got := SanitizeArgs(nil, false); got != "" {
		t.Fatalf("SanitizeArgs(nil) = %q, want empty", got)
	}
}

type summarizableFake struct {
	countA int
	countB int
}

func (s summarizableFake) TelemetrySummary() map[string]any {
	return map[string]any{"a": s.countA, "b": s.countB}
}

func TestSummarizeResultUsesFastPathForSummarizable(t *testing.T) {
	got := SummarizeResult(summarizableFake{countA: 3, countB: 7})
	if !strings.Contains(got, "\"a\":3") || !strings.Contains(got, "\"b\":7") {
		t.Fatalf("SummarizeResult fast path missing fields: %s", got)
	}
}

func TestSummarizeResultFallsBackToJSONForNonSummarizable(t *testing.T) {
	// Plain map — no Summarizable interface, must go through fallback
	got := SummarizeResult(map[string]any{
		"stored":  true,
		"results": []any{map[string]any{}, map[string]any{}},
	})
	if !strings.Contains(got, "\"stored\":true") {
		t.Fatalf("fallback missing stored: %s", got)
	}
	if !strings.Contains(got, "\"entity_groups\":2") {
		t.Fatalf("fallback missing entity_groups: %s", got)
	}
}

func TestSummarizeResultReturnsCountsOnly(t *testing.T) {
	result := map[string]any{
		"stored": true,
		"results": []any{
			map[string]any{"entity": "A", "observations": []any{"secret content 1"}},
			map[string]any{"entity": "B", "observations": []any{"secret content 2"}},
		},
		"total":   5,
		"compact": false,
	}
	got := SummarizeResult(result)
	if strings.Contains(got, "secret content") {
		t.Fatalf("SummarizeResult leaked content: %s", got)
	}
	if strings.Contains(got, "\"A\"") || strings.Contains(got, "\"B\"") {
		t.Fatalf("SummarizeResult leaked entity names: %s", got)
	}
	if !strings.Contains(got, "\"entity_groups\":2") {
		t.Fatalf("SummarizeResult missing entity_groups count: %s", got)
	}
	if !strings.Contains(got, "\"stored\":true") {
		t.Fatalf("SummarizeResult missing stored: %s", got)
	}
	if !strings.Contains(got, "\"total\":5") {
		t.Fatalf("SummarizeResult missing total: %s", got)
	}
}

func TestSummarizeResultNilReturnsEmpty(t *testing.T) {
	if got := SummarizeResult(nil); got != "" {
		t.Fatalf("SummarizeResult(nil) = %q, want empty", got)
	}
}
