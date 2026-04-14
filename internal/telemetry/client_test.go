package telemetry

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNilClientMethodsAreSafe(t *testing.T) {
	var c *Client
	if c.Strict() {
		t.Fatalf("nil client should not be strict")
	}
	if got := c.LogToolCall(ToolCallInput{Tool: "noop"}); got != 0 {
		t.Fatalf("nil client LogToolCall should return 0, got %d", got)
	}
	c.LogSearchMetrics(SearchMetricsInput{ToolCallID: 123, Query: "anything"})
	if err := c.Close(); err != nil {
		t.Fatalf("nil client Close should be nil, got %v", err)
	}
}

func TestInitIfEnabledEmptyPathReturnsNil(t *testing.T) {
	if got := InitIfEnabled("", false); got != nil {
		t.Fatalf("InitIfEnabled(\"\", false) should return nil, got %+v", got)
	}
}

func TestInitIfEnabledInvalidPathReturnsNil(t *testing.T) {
	// Build a path inside a non-existent subdirectory of t.TempDir() so the
	// failure mode is identical across macOS, Linux, and Windows.
	bad := filepath.Join(t.TempDir(), "missing-subdir", "telemetry.db")
	if got := InitIfEnabled(bad, false); got != nil {
		_ = got.Close()
		t.Fatalf("InitIfEnabled on path inside missing directory should return nil, got client")
	}
}

func TestInitIfEnabledCreatesSchemaAndInserts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("InitIfEnabled returned nil on valid path")
	}
	t.Cleanup(func() { _ = c.Close() })

	id := c.LogToolCall(ToolCallInput{
		Tool:          "remember",
		Client:        ClientInfo{Name: "claude-code", Source: "env"},
		DBScope:       "global",
		DurationMs:    1.23,
		ArgsSummary:   `{"entity":"Alice"}`,
		ResultSummary: `{"stored":true}`,
	})
	if id == 0 {
		t.Fatalf("LogToolCall returned 0 on valid client")
	}

	c.LogSearchMetrics(SearchMetricsInput{
		ToolCallID:      id,
		Query:           "alice",
		Channels:        map[string]int{"fts": 3},
		CandidatesTotal: 3,
		ResultsReturned: 1,
		LimitRequested:  5,
		ScoreMin:        0.1,
		ScoreMax:        0.9,
		ScoreMedian:     0.5,
	})

	// Read back from disk to prove persistence. Use the same DSN pattern as
	// InitIfEnabled so the readback works consistently across Windows and
	// POSIX (raw paths with drive letters can trip the driver).
	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open telemetry db for readback: %v", err)
	}
	defer rdb.Close()

	var toolCount, searchCount int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&toolCount); err != nil {
		t.Fatalf("count tool_calls: %v", err)
	}
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM search_metrics`).Scan(&searchCount); err != nil {
		t.Fatalf("count search_metrics: %v", err)
	}
	if toolCount != 1 {
		t.Fatalf("tool_calls count = %d, want 1", toolCount)
	}
	if searchCount != 1 {
		t.Fatalf("search_metrics count = %d, want 1", searchCount)
	}

	var argsSummary, resultSummary, clientName sql.NullString
	if err := rdb.QueryRow(`SELECT client_name, args_summary, result_summary FROM tool_calls WHERE id = ?`, id).Scan(&clientName, &argsSummary, &resultSummary); err != nil {
		t.Fatalf("readback tool_calls: %v", err)
	}
	if clientName.String != "claude-code" {
		t.Fatalf("client_name = %q, want claude-code", clientName.String)
	}
	if !strings.Contains(argsSummary.String, "Alice") {
		t.Fatalf("args_summary missing entity: %q", argsSummary.String)
	}
}

func TestStrictModeHashesSearchQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strict.db")
	c := InitIfEnabled(path, true)
	if c == nil {
		t.Fatalf("InitIfEnabled strict returned nil")
	}
	t.Cleanup(func() { _ = c.Close() })

	if !c.Strict() {
		t.Fatalf("expected strict mode active")
	}

	id := c.LogToolCall(ToolCallInput{Tool: "recall", DBScope: "project"})
	c.LogSearchMetrics(SearchMetricsInput{
		ToolCallID:      id,
		Query:           "sensitive therapy question",
		Channels:        map[string]int{"fts": 1},
		CandidatesTotal: 1,
		ResultsReturned: 1,
	})

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer rdb.Close()
	var storedQuery sql.NullString
	if err := rdb.QueryRow(`SELECT query FROM search_metrics WHERE tool_call_id = ?`, id).Scan(&storedQuery); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if strings.Contains(storedQuery.String, "sensitive") || strings.Contains(storedQuery.String, "therapy") {
		t.Fatalf("strict mode leaked plaintext query: %q", storedQuery.String)
	}
	if !strings.HasPrefix(storedQuery.String, "sha256:") {
		t.Fatalf("strict mode did not hash query, got %q", storedQuery.String)
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-twice.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("InitIfEnabled returned nil on valid path")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil (Close should be idempotent)", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("third Close() = %v, want nil", err)
	}
}

// A late-arriving tool call that lands after the Runtime has begun shutting
// down must not panic. Close nils the db/stmt fields; LogToolCall and
// LogSearchMetrics must both degrade gracefully to a no-op.
func TestLogAfterCloseDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log-after-close.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("init failed")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// LogToolCall after Close must return 0 without panicking.
	if id := c.LogToolCall(ToolCallInput{Tool: "remember"}); id != 0 {
		t.Fatalf("LogToolCall after Close returned id %d, want 0", id)
	}

	// LogSearchMetrics after Close must silently no-op (no panic).
	c.LogSearchMetrics(SearchMetricsInput{ToolCallID: 42, Query: "anything"})
}

// Under concurrent Close + LogToolCall, the client mutex must serialize
// operations so no goroutine observes a half-closed state. Run with
// `go test -race` to turn an unsynchronized access into a test failure
// instead of an occasional production panic.
func TestClientCloseRacesWithLogging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("init failed")
	}

	const loggers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < loggers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Fire several calls so Close has real overlap with active inserts.
			for j := 0; j < 4; j++ {
				_ = c.LogToolCall(ToolCallInput{Tool: "remember"})
				c.LogSearchMetrics(SearchMetricsInput{ToolCallID: 1, Query: "q"})
			}
		}()
	}

	close(start)
	// Give the goroutines a moment to be mid-flight, then close. The mutex
	// in Client must make this safe; without it, -race would flag the
	// db/stmt pointer reads against the Close writes.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()

	// A second set of calls after Close must be no-ops, not panics.
	if id := c.LogToolCall(ToolCallInput{Tool: "post-close"}); id != 0 {
		t.Fatalf("post-close LogToolCall returned %d, want 0", id)
	}
	c.LogSearchMetrics(SearchMetricsInput{ToolCallID: 99, Query: "post"})
}

func TestLogSearchMetricsZeroToolCallIDIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noop.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("init failed")
	}
	t.Cleanup(func() { _ = c.Close() })

	c.LogSearchMetrics(SearchMetricsInput{ToolCallID: 0, Query: "anything"})
	var count int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM search_metrics`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("LogSearchMetrics with ToolCallID=0 inserted row, count=%d", count)
	}
}
