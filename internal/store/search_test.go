package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDecayedConfidence_ClampedToOriginal(t *testing.T) {
	t.Parallel()

	// Future timestamp must not amplify beyond original confidence.
	future := time.Now().UTC().Add(7 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	got := DecayedConfidence(future, 0.9, 0, 12)
	if got > 0.9 {
		t.Fatalf("DecayedConfidence(future) = %f, want <= 0.9", got)
	}

	// Recent past timestamp must stay within a tight band of the original
	// confidence (no clamp needed, just no amplification).
	recent := time.Now().UTC().Add(-1 * time.Second).Format("2006-01-02 15:04:05")
	gotNow := DecayedConfidence(recent, 0.9, 0, 12)
	if gotNow > 0.9 {
		t.Fatalf("DecayedConfidence(recent) = %f, want <= 0.9", gotNow)
	}
	if gotNow < 0.899 {
		t.Fatalf("DecayedConfidence(recent) = %f, want >= 0.899", gotNow)
	}
}

func TestEscapeLikePattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"50%", `50\%`},
		{"test_a", `test\_a`},
		{"a%b_c", `a\%b\_c`},
		{`a\%b`, `a\\\%b`},
		{"", ""},
	}

	for _, tt := range tests {
		got := escapeLikePattern(tt.input)
		if got != tt.expected {
			t.Fatalf("escapeLikePattern(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestEscapeLikePattern_QueryVerification(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "like-escape.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "LikeEscapeEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}

	// Content that differs only by a literal % vs an extra 0 digit.
	if _, err := AddObservation(db, entityID, "discount is 50% off", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(50%%) error = %v", err)
	}
	if _, err := AddObservation(db, entityID, "discount is 500 off", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(500) error = %v", err)
	}

	// Direct SQL query with escaped pattern — this isolates the LIKE layer
	// from FTS noise. The pattern "50%" escaped must match only "50% off".
	pattern := "%" + escapeLikePattern("50%") + "%"
	rows, err := db.Query("SELECT content FROM observations WHERE content LIKE ? ESCAPE '\\'", pattern)
	if err != nil {
		t.Fatalf("direct LIKE query error = %v", err)
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan error = %v", err)
		}
		matches = append(matches, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate error = %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("expected 1 match for escaped '50%%', got %d: %v", len(matches), matches)
	}
	if matches[0] != "discount is 50% off" {
		t.Fatalf("match = %q, want %q", matches[0], "discount is 50% off")
	}
}

func TestSearchMemoryReportsFTSQueryErrorsAndUsesFallback(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "fts-degraded-search.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "FTSDegradedEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	if _, err := AddObservation(db, entityID, "fallbackonlytoken survives fts outage", "user", 1.0); err != nil {
		t.Fatalf("AddObservation() error = %v", err)
	}
	if _, err := db.Exec(`DROP TABLE memory_fts`); err != nil {
		t.Fatalf("drop memory_fts: %v", err)
	}

	results, metrics, err := SearchMemory(db, "fallbackonlytoken", 5, memoryHalfLifeWeeks())
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchMemory() returned %d results, want fallback result: %#v", len(results), results)
	}
	if results[0].ID == 0 || results[0].Content != "fallbackonlytoken survives fts outage" {
		t.Fatalf("SearchMemory() fallback result = %#v", results[0])
	}
	if metrics.FTSQueryErrors == 0 {
		t.Fatalf("SearchMemory() FTSQueryErrors = 0, want degraded signal")
	}
	if metrics.Channels["content_like"] == 0 {
		t.Fatalf("SearchMemory() channels = %#v, want content_like fallback", metrics.Channels)
	}
}
