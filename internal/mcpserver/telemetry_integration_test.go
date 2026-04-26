package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "modernc.org/sqlite"

	"workmem/internal/telemetry"
)

func TestTelemetryEnabledRoundtripLogsToolCallsAndSearchMetrics(t *testing.T) {
	telePath := filepath.Join(t.TempDir(), "telemetry.db")
	tele := telemetry.InitIfEnabled(telePath, false)
	if tele == nil {
		t.Fatalf("telemetry InitIfEnabled returned nil on valid path")
	}

	runtime, err := New(Config{DBPath: filepath.Join(t.TempDir(), "memory.db"), Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()

	callOK(t, session, ctx, "remember", map[string]any{
		"entity":      "TelemetryEntity",
		"observation": "sensitive observation content must never leak",
	})
	callOK(t, session, ctx, "recall", map[string]any{
		"query": "TelemetryEntity",
		"limit": 5,
	})
	callOK(t, session, ctx, "forget", map[string]any{
		"entity": "TelemetryEntity",
	})

	// Shut the runtime down before reading back. Ownership of tele is held
	// by Runtime, so Runtime.Close() (triggered through stop -> Run's defer)
	// is the right way to flush it. Direct tele.Close() here would violate
	// the ownership contract documented in mcpserver.Config.
	stop()

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(telePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open telemetry db for readback: %v", err)
	}
	defer rdb.Close()

	var callCount int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&callCount); err != nil {
		t.Fatalf("count tool_calls: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("tool_calls count = %d, want 3", callCount)
	}

	rows, err := rdb.Query(`SELECT tool, args_summary, result_summary, duration_ms, is_error FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatalf("query tool_calls: %v", err)
	}
	defer rows.Close()
	var tools []string
	for rows.Next() {
		var tool string
		var argsSummary, resultSummary sql.NullString
		var dur float64
		var isErr int
		if err := rows.Scan(&tool, &argsSummary, &resultSummary, &dur, &isErr); err != nil {
			t.Fatalf("scan tool_calls row: %v", err)
		}
		tools = append(tools, tool)
		if strings.Contains(argsSummary.String, "sensitive observation content") {
			t.Fatalf("args_summary leaked observation content: %s", argsSummary.String)
		}
		if dur <= 0 {
			t.Fatalf("duration_ms = %v for %s, want > 0", dur, tool)
		}
		if isErr != 0 {
			t.Fatalf("is_error = %d for %s, want 0", isErr, tool)
		}
	}
	expected := []string{"remember", "recall", "forget"}
	for i, want := range expected {
		if tools[i] != want {
			t.Fatalf("tool[%d] = %q, want %q", i, tools[i], want)
		}
	}

	var smCount int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM search_metrics`).Scan(&smCount); err != nil {
		t.Fatalf("count search_metrics: %v", err)
	}
	if smCount != 1 {
		t.Fatalf("search_metrics count = %d, want 1", smCount)
	}

	var smQuery sql.NullString
	var candidatesTotal, resultsReturned int
	if err := rdb.QueryRow(`SELECT query, candidates_total, results_returned FROM search_metrics`).Scan(&smQuery, &candidatesTotal, &resultsReturned); err != nil {
		t.Fatalf("readback search_metrics: %v", err)
	}
	if smQuery.String != "TelemetryEntity" {
		t.Fatalf("search_metrics.query = %q, want TelemetryEntity", smQuery.String)
	}
	if candidatesTotal == 0 {
		t.Fatalf("search_metrics.candidates_total = 0, want > 0")
	}
}

func TestTelemetryDisabledViaFromEnvCreatesNoArtifacts(t *testing.T) {
	// Drive the full env → FromEnv → runtime → dispatch path with telemetry
	// explicitly disabled, and verify that no telemetry artifact appears
	// anywhere in the runtime's data directory after a complete tool call
	// cycle. This proves the disabled path is silent end-to-end, not just
	// that a hand-picked path happens not to exist.
	t.Setenv("MEMORY_TELEMETRY_PATH", "")
	t.Setenv("MEMORY_TELEMETRY_PRIVACY", "")
	tele := telemetry.FromEnv()
	if tele != nil {
		t.Fatalf("FromEnv with MEMORY_TELEMETRY_PATH unset must return nil, got %+v", tele)
	}

	dir := t.TempDir()
	memDB := filepath.Join(dir, "memory.db")
	runtime, err := New(Config{DBPath: memDB, Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()

	callOK(t, session, ctx, "remember", map[string]any{
		"entity":      "NoTelemetryEntity",
		"observation": "this must dispatch without creating any telemetry file",
	})
	callOK(t, session, ctx, "recall", map[string]any{"query": "NoTelemetry", "limit": 3})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %q: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// memory.db + its WAL/SHM companions are expected; anything else is
		// a telemetry leak.
		if name == "memory.db" || strings.HasPrefix(name, "memory.db-") {
			continue
		}
		t.Fatalf("unexpected artifact %q in data dir — telemetry disabled path should not create any file", name)
	}
}

func TestTelemetryStrictModeHashesIdentifiersEndToEnd(t *testing.T) {
	telePath := filepath.Join(t.TempDir(), "strict-telemetry.db")
	tele := telemetry.InitIfEnabled(telePath, true)
	if tele == nil {
		t.Fatalf("telemetry InitIfEnabled(strict) returned nil")
	}

	runtime, err := New(Config{DBPath: filepath.Join(t.TempDir(), "memory.db"), Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()

	callOK(t, session, ctx, "remember", map[string]any{
		"entity":      "Alice",
		"observation": "confidential note attached to Alice",
	})
	callOK(t, session, ctx, "recall", map[string]any{
		"query": "sensitive question about therapy",
		"limit": 5,
	})

	// Same ownership discipline as the enabled roundtrip test: stop the
	// runtime (which triggers Runtime.Close -> tele.Close) before reading
	// the telemetry DB back.
	stop()

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(telePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open strict telemetry db: %v", err)
	}
	defer rdb.Close()

	rows, err := rdb.Query(`SELECT args_summary FROM tool_calls`)
	if err != nil {
		t.Fatalf("query tool_calls: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var args sql.NullString
		if err := rows.Scan(&args); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, leak := range []string{"Alice", "sensitive question", "therapy"} {
			if strings.Contains(args.String, leak) {
				t.Fatalf("strict args_summary leaked %q: %s", leak, args.String)
			}
		}
	}

	var smQuery sql.NullString
	if err := rdb.QueryRow(`SELECT query FROM search_metrics`).Scan(&smQuery); err != nil {
		t.Fatalf("read search_metrics.query: %v", err)
	}
	if strings.Contains(smQuery.String, "sensitive") || strings.Contains(smQuery.String, "therapy") {
		t.Fatalf("strict search_metrics.query leaked plaintext: %q", smQuery.String)
	}
	if !strings.HasPrefix(smQuery.String, "sha256:") {
		t.Fatalf("strict search_metrics.query not hashed: %q", smQuery.String)
	}
}

func TestTelemetryRedactsMalformedSensitiveArgumentsEndToEnd(t *testing.T) {
	telePath := filepath.Join(t.TempDir(), "strict-malformed-telemetry.db")
	tele := telemetry.InitIfEnabled(telePath, true)
	if tele == nil {
		t.Fatalf("telemetry InitIfEnabled(strict) returned nil")
	}

	runtime, err := New(Config{DBPath: filepath.Join(t.TempDir(), "memory.db"), Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()
	sentinel := "SECRET-MALFORMED-PAYLOAD"

	calls := []struct {
		name string
		args map[string]any
	}{
		{
			name: "remember",
			args: map[string]any{
				"entity":      "MalformedTelemetryEntity",
				"observation": map[string]any{"secret": sentinel},
			},
		},
		{
			name: "remember_event",
			args: map[string]any{
				"label":        "MalformedTelemetryEvent",
				"event_date":   "2026-04-26",
				"context":      []any{sentinel},
				"observations": []any{map[string]any{"entity": "Nested", "observation": sentinel}},
			},
		},
		{
			name: "remember_batch",
			args: map[string]any{
				"facts": map[string]any{"secret": sentinel},
			},
		},
	}

	for _, tc := range calls {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Fatalf("CallTool(%s) error = %v", tc.name, err)
		}
		if !result.IsError {
			t.Fatalf("CallTool(%s) returned success for malformed sensitive args", tc.name)
		}
		if text := toolTextContent(t, result); strings.Contains(text, sentinel) {
			t.Fatalf("CallTool(%s) error payload leaked malformed sensitive value: %s", tc.name, text)
		}
	}

	stop()

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(telePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open strict telemetry db: %v", err)
	}
	defer rdb.Close()

	rows, err := rdb.Query(`SELECT tool, args_summary, is_error FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatalf("query tool_calls: %v", err)
	}
	defer rows.Close()

	var rowCount int
	for rows.Next() {
		rowCount++
		var tool string
		var argsSummary sql.NullString
		var isErr int
		if err := rows.Scan(&tool, &argsSummary, &isErr); err != nil {
			t.Fatalf("scan tool_calls row: %v", err)
		}
		if isErr != 1 {
			t.Fatalf("tool_calls row for %s is_error = %d, want 1", tool, isErr)
		}
		if strings.Contains(argsSummary.String, sentinel) {
			t.Fatalf("tool_calls args_summary for %s leaked malformed sensitive value: %s", tool, argsSummary.String)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tool_calls: %v", err)
	}
	if rowCount != len(calls) {
		t.Fatalf("tool_calls count = %d, want %d", rowCount, len(calls))
	}
}

// TestStepGateConflictHintEndToEndLoop is the Step 4.1 Gate fixture.
// It drives the full conflict-hint loop through the MCP stdio surface
// in-process and asserts every part of the promised behavior lands:
//
//  1. a seed `remember` produces no hints
//  2. a near-duplicate `remember` surfaces possible_conflicts referencing
//     the seed observation
//  3. `forget` on the hinted observation_id soft-deletes it
//  4. subsequent `recall` returns only the surviving observation
//     (FTS + ranking both respect the tombstone)
//  5. telemetry rows reflect conflicts_surfaced = {0, >=1, 0, 0} in order
func TestStepGateConflictHintEndToEndLoop(t *testing.T) {
	telePath := filepath.Join(t.TempDir(), "step-gate.db")
	tele := telemetry.InitIfEnabled(telePath, false)
	if tele == nil {
		t.Fatalf("telemetry InitIfEnabled returned nil")
	}

	runtime, err := New(Config{DBPath: filepath.Join(t.TempDir(), "memory.db"), Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()

	// Step 1 — seed observation; no hints expected.
	seedResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "API",
			"observation": "rate limit is 100 per minute",
		},
	})
	if err != nil {
		t.Fatalf("seed remember: %v", err)
	}
	if seedResult.IsError {
		t.Fatalf("seed remember tool error: %#v", seedResult)
	}
	seedPayload := unmarshalToolText(t, seedResult)
	if _, present := seedPayload["possible_conflicts"]; present {
		t.Fatalf("seed remember surfaced possible_conflicts; expected none")
	}
	seedObsID, ok := seedPayload["observation_id"].(float64)
	if !ok {
		t.Fatalf("seed observation_id not a number: %#v", seedPayload["observation_id"])
	}

	// Step 2 — near-duplicate; hint must reference the seed observation.
	followResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember",
		Arguments: map[string]any{
			"entity":      "API",
			"observation": "rate limit is 200 per minute",
		},
	})
	if err != nil {
		t.Fatalf("follow-up remember: %v", err)
	}
	if followResult.IsError {
		t.Fatalf("follow-up remember tool error: %#v", followResult)
	}
	followPayload := unmarshalToolText(t, followResult)
	rawHints, present := followPayload["possible_conflicts"]
	if !present {
		t.Fatalf("follow-up remember missing possible_conflicts: %v", followPayload)
	}
	hints, ok := rawHints.([]any)
	if !ok || len(hints) == 0 {
		t.Fatalf("possible_conflicts not a non-empty array: %#v", rawHints)
	}
	hint := hints[0].(map[string]any)
	hintObsID, ok := hint["observation_id"].(float64)
	if !ok {
		t.Fatalf("hint observation_id not a number: %#v", hint["observation_id"])
	}
	if hintObsID != seedObsID {
		t.Fatalf("hint observation_id = %v, want seed %v", hintObsID, seedObsID)
	}

	// Step 3 — forget the hinted observation.
	forgetResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "forget",
		Arguments: map[string]any{
			"observation_id": seedObsID,
		},
	})
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if forgetResult.IsError {
		t.Fatalf("forget tool error: %#v", forgetResult)
	}
	forgetPayload := unmarshalToolText(t, forgetResult)
	if deleted, _ := forgetPayload["deleted"].(bool); !deleted {
		t.Fatalf("forget deleted = false, want true: %v", forgetPayload)
	}

	// Step 4 — recall must return only the surviving observation, and the
	// forgotten one must not appear anywhere in the text content (which
	// covers both FTS and ranking paths since recall joins them).
	recallResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "recall",
		Arguments: map[string]any{
			"query": "rate limit per minute",
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if recallResult.IsError {
		t.Fatalf("recall tool error: %#v", recallResult)
	}
	text := toolTextContent(t, recallResult)
	if strings.Contains(text, "rate limit is 100 per minute") {
		t.Fatalf("recall returned the tombstoned observation: %s", text)
	}
	if !strings.Contains(text, "rate limit is 200 per minute") {
		t.Fatalf("recall missing the surviving observation: %s", text)
	}

	// Step 5 — read telemetry back. Runtime.Close (via stop) flushes the
	// sink; see the ownership contract in mcpserver.Config.
	stop()

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(telePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open telemetry db: %v", err)
	}
	defer rdb.Close()

	rows, err := rdb.Query(`SELECT tool, conflicts_surfaced FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatalf("query tool_calls: %v", err)
	}
	defer rows.Close()
	type row struct {
		tool              string
		conflictsSurfaced int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tool, &r.conflictsSurfaced); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	wantTools := []string{"remember", "remember", "forget", "recall"}
	if len(got) != len(wantTools) {
		t.Fatalf("tool_calls count = %d, want %d; rows=%+v", len(got), len(wantTools), got)
	}
	for i, want := range wantTools {
		if got[i].tool != want {
			t.Fatalf("row[%d].tool = %q, want %q", i, got[i].tool, want)
		}
	}
	if got[0].conflictsSurfaced != 0 {
		t.Fatalf("seed remember conflicts_surfaced = %d, want 0", got[0].conflictsSurfaced)
	}
	if got[1].conflictsSurfaced < 1 {
		t.Fatalf("follow-up remember conflicts_surfaced = %d, want >=1", got[1].conflictsSurfaced)
	}
	if got[2].conflictsSurfaced != 0 {
		t.Fatalf("forget conflicts_surfaced = %d, want 0", got[2].conflictsSurfaced)
	}
	if got[3].conflictsSurfaced != 0 {
		t.Fatalf("recall conflicts_surfaced = %d, want 0", got[3].conflictsSurfaced)
	}
}

func unmarshalToolText(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolTextContent(t, result)), &payload); err != nil {
		t.Fatalf("unmarshal tool text: %v", err)
	}
	return payload
}

func toolTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatalf("tool result has no content: %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

func TestTelemetryLogsConflictsSurfacedOnRememberWithHints(t *testing.T) {
	telePath := filepath.Join(t.TempDir(), "conflicts-surfaced.db")
	tele := telemetry.InitIfEnabled(telePath, false)
	if tele == nil {
		t.Fatalf("telemetry InitIfEnabled returned nil")
	}

	runtime, err := New(Config{DBPath: filepath.Join(t.TempDir(), "memory.db"), Telemetry: tele})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, stop := startTelemetrySession(t, runtime)
	defer stop()
	ctx := context.Background()

	// Seed an observation, then a near-duplicate on the same entity so
	// the second remember produces at least one possible_conflicts hint
	// and the telemetry row for that second call records the count.
	callOK(t, session, ctx, "remember", map[string]any{
		"entity":      "API",
		"observation": "rate limit is 100 per minute",
	})
	callOK(t, session, ctx, "remember", map[string]any{
		"entity":      "API",
		"observation": "rate limit is 200 per minute",
	})
	// An unrelated tool whose telemetry row should always show 0
	// conflicts_surfaced regardless of prior remember calls.
	callOK(t, session, ctx, "recall", map[string]any{
		"query": "API",
		"limit": 5,
	})

	stop()

	rdb, err := sql.Open("sqlite", "file:"+filepath.Clean(telePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open telemetry db: %v", err)
	}
	defer rdb.Close()

	rows, err := rdb.Query(`SELECT tool, conflicts_surfaced FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatalf("query tool_calls: %v", err)
	}
	defer rows.Close()

	type row struct {
		tool              string
		conflictsSurfaced int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tool, &r.conflictsSurfaced); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("tool_calls count = %d, want 3", len(got))
	}

	if got[0].tool != "remember" || got[0].conflictsSurfaced != 0 {
		t.Fatalf("row[0] = %+v; want seed remember with conflicts_surfaced=0", got[0])
	}
	if got[1].tool != "remember" || got[1].conflictsSurfaced < 1 {
		t.Fatalf("row[1] = %+v; want follow-up remember with conflicts_surfaced>=1", got[1])
	}
	if got[2].tool != "recall" || got[2].conflictsSurfaced != 0 {
		t.Fatalf("row[2] = %+v; want recall with conflicts_surfaced=0", got[2])
	}
}

func callOK(t *testing.T, session *mcp.ClientSession, ctx context.Context, name string, args map[string]any) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned tool error: %+v", name, result)
	}
}

// startTelemetrySession spins up an in-memory MCP client+server pair against
// the given runtime and returns a session plus a stop function that unwinds
// the session and the server goroutine cleanly.
func startTelemetrySession(t *testing.T, runtime *Runtime) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() { errCh <- runtime.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "telemetry-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect() error = %v", err)
	}
	// Idempotent stop — tests call it explicitly before DB readback (so
	// Runtime.Close flushes telemetry) and then again through defer for
	// panic-safety. Both calls must converge without blocking or fataling.
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = session.Close()
			cancel()
			select {
			case <-errCh:
			case <-time.After(2 * time.Second):
				t.Error("runtime.Run() did not exit after client shutdown")
			}
		})
	}
	return session, stop
}
