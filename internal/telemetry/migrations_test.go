package telemetry

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// legacyToolCallsCreateSQL mirrors the v0.x telemetry schema, before
// conflicts_surfaced was introduced. Used to exercise the migration path
// so we know an existing DB upgrades cleanly when workmem is bumped past
// this point without recreating the telemetry DB.
const legacyToolCallsCreateSQL = `CREATE TABLE tool_calls (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    ts             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
    tool           TEXT NOT NULL,
    client_name    TEXT,
    client_version TEXT,
    client_source  TEXT,
    db_scope       TEXT NOT NULL DEFAULT 'global',
    project_path   TEXT,
    duration_ms    REAL,
    args_summary   TEXT,
    result_summary TEXT,
    is_error       INTEGER NOT NULL DEFAULT 0
)`

func openRawTelemetryDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.Clean(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw telemetry db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApplyMigrationsOnFreshDBIsNoop(t *testing.T) {
	t.Parallel()
	// InitIfEnabled runs applyMigrations as part of setup; the column must
	// already exist after init so the subsequent explicit call short-circuits
	// via columnExists and stays a no-op under repeated invocation.
	path := filepath.Join(t.TempDir(), "fresh.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("InitIfEnabled returned nil")
	}
	t.Cleanup(func() { _ = c.Close() })

	rdb := openRawTelemetryDB(t, path)
	present, err := columnExists(rdb, "tool_calls", "conflicts_surfaced")
	if err != nil {
		t.Fatalf("columnExists: %v", err)
	}
	if !present {
		t.Fatalf("conflicts_surfaced column missing after fresh InitIfEnabled")
	}
	// Idempotent second pass must not error.
	if err := applyMigrations(rdb); err != nil {
		t.Fatalf("applyMigrations second pass: %v", err)
	}
	if err := applyMigrations(rdb); err != nil {
		t.Fatalf("applyMigrations third pass: %v", err)
	}
}

func TestApplyMigrationsUpgradesLegacyDB(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openRawTelemetryDB(t, path)

	// Seed a legacy schema that pre-dates the conflicts_surfaced column.
	if _, err := db.Exec(legacyToolCallsCreateSQL); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tool_calls (tool) VALUES ('legacy-row')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	before, err := columnExists(db, "tool_calls", "conflicts_surfaced")
	if err != nil {
		t.Fatalf("columnExists before: %v", err)
	}
	if before {
		t.Fatalf("legacy table already had conflicts_surfaced — test fixture is wrong")
	}

	if err := applyMigrations(db); err != nil {
		t.Fatalf("applyMigrations on legacy: %v", err)
	}

	after, err := columnExists(db, "tool_calls", "conflicts_surfaced")
	if err != nil {
		t.Fatalf("columnExists after: %v", err)
	}
	if !after {
		t.Fatalf("conflicts_surfaced column missing after migration")
	}

	// Existing rows get the NOT NULL DEFAULT 0 from the ADD COLUMN.
	var legacyValue int
	if err := db.QueryRow(`SELECT conflicts_surfaced FROM tool_calls WHERE tool = 'legacy-row'`).Scan(&legacyValue); err != nil {
		t.Fatalf("read legacy row conflicts_surfaced: %v", err)
	}
	if legacyValue != 0 {
		t.Fatalf("legacy row conflicts_surfaced = %d, want 0 (ADD COLUMN default)", legacyValue)
	}

	// Second application is a pure no-op.
	if err := applyMigrations(db); err != nil {
		t.Fatalf("applyMigrations second pass: %v", err)
	}
}

func TestLogToolCallPersistsConflictsSurfaced(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conflicts-surfaced.db")
	c := InitIfEnabled(path, false)
	if c == nil {
		t.Fatalf("InitIfEnabled returned nil")
	}
	t.Cleanup(func() { _ = c.Close() })

	cases := []struct {
		name  string
		tool  string
		count int
	}{
		{"remember-zero", "remember", 0},
		{"remember-one", "remember", 1},
		{"remember-three", "remember", 3},
		{"forget-zero-unrelated", "forget", 0},
	}
	ids := make(map[string]int64, len(cases))
	for _, tc := range cases {
		id := c.LogToolCall(ToolCallInput{Tool: tc.tool, ConflictsSurfaced: tc.count})
		if id == 0 {
			t.Fatalf("LogToolCall(%q) returned 0", tc.name)
		}
		ids[tc.name] = id
	}

	rdb := openRawTelemetryDB(t, path)
	for _, tc := range cases {
		var stored int
		if err := rdb.QueryRow(`SELECT conflicts_surfaced FROM tool_calls WHERE id = ?`, ids[tc.name]).Scan(&stored); err != nil {
			t.Fatalf("readback %q: %v", tc.name, err)
		}
		if stored != tc.count {
			t.Fatalf("%q conflicts_surfaced = %d, want %d", tc.name, stored, tc.count)
		}
	}
}
