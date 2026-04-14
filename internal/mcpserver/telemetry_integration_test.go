package mcpserver

import (
	"context"
	"database/sql"
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

	rdb, err := sql.Open("sqlite", telePath)
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

	rdb, err := sql.Open("sqlite", telePath)
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
