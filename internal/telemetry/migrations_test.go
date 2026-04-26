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

// legacySearchMetricsCreateSQL mirrors the telemetry schema before
// fts_query_errors was introduced.
const legacySearchMetricsCreateSQL = `CREATE TABLE search_metrics (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_call_id     INTEGER REFERENCES tool_calls(id),
    query            TEXT,
    channels         TEXT,
    candidates_total INTEGER,
    results_returned INTEGER,
    limit_requested  INTEGER,
    score_min        REAL,
    score_max        REAL,
    score_median     REAL,
    compact          INTEGER DEFAULT 0
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

func assertSchemaMigrationCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count schema_migrations error = %v", err)
	}
	if got != want {
		t.Fatalf("schema_migrations count = %d, want %d", got, want)
	}
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
	checks := []struct {
		table  string
		column string
	}{
		{table: "tool_calls", column: "conflicts_surfaced"},
		{table: "tool_calls", column: "conflict_fts_query_errors"},
		{table: "search_metrics", column: "fts_query_errors"},
	}
	for _, check := range checks {
		present, err := columnExists(rdb, check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s.%s): %v", check.table, check.column, err)
		}
		if !present {
			t.Fatalf("%s.%s column missing after fresh InitIfEnabled", check.table, check.column)
		}
	}
	assertSchemaMigrationCount(t, rdb, len(telemetryMigrations))
	// Idempotent second pass must not error.
	if err := applyMigrations(rdb); err != nil {
		t.Fatalf("applyMigrations second pass: %v", err)
	}
	if err := applyMigrations(rdb); err != nil {
		t.Fatalf("applyMigrations third pass: %v", err)
	}
	assertSchemaMigrationCount(t, rdb, len(telemetryMigrations))
}

func TestApplyMigrationsUpgradesLegacyDB(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openRawTelemetryDB(t, path)

	// Seed a legacy schema that pre-dates the conflicts_surfaced column.
	if _, err := db.Exec(legacyToolCallsCreateSQL); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if _, err := db.Exec(legacySearchMetricsCreateSQL); err != nil {
		t.Fatalf("seed legacy search_metrics table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tool_calls (tool) VALUES ('legacy-row')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO search_metrics (tool_call_id, query) VALUES (1, 'legacy-query')`); err != nil {
		t.Fatalf("seed legacy search_metrics row: %v", err)
	}

	beforeChecks := []struct {
		table  string
		column string
	}{
		{table: "tool_calls", column: "conflicts_surfaced"},
		{table: "tool_calls", column: "conflict_fts_query_errors"},
		{table: "search_metrics", column: "fts_query_errors"},
	}
	for _, check := range beforeChecks {
		before, err := columnExists(db, check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s.%s) before: %v", check.table, check.column, err)
		}
		if before {
			t.Fatalf("legacy table already had %s.%s — test fixture is wrong", check.table, check.column)
		}
	}

	if err := applyMigrations(db); err != nil {
		t.Fatalf("applyMigrations on legacy: %v", err)
	}

	for _, check := range beforeChecks {
		after, err := columnExists(db, check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s.%s) after: %v", check.table, check.column, err)
		}
		if !after {
			t.Fatalf("%s.%s column missing after migration", check.table, check.column)
		}
	}

	// Existing rows get the NOT NULL DEFAULT 0 from the ADD COLUMN.
	var conflictsSurfaced, conflictFTSErrors, searchFTSErrors int
	if err := db.QueryRow(`SELECT conflicts_surfaced, conflict_fts_query_errors FROM tool_calls WHERE tool = 'legacy-row'`).Scan(&conflictsSurfaced, &conflictFTSErrors); err != nil {
		t.Fatalf("read legacy row tool_calls migration columns: %v", err)
	}
	if err := db.QueryRow(`SELECT fts_query_errors FROM search_metrics WHERE query = 'legacy-query'`).Scan(&searchFTSErrors); err != nil {
		t.Fatalf("read legacy row search_metrics.fts_query_errors: %v", err)
	}
	if conflictsSurfaced != 0 || conflictFTSErrors != 0 || searchFTSErrors != 0 {
		t.Fatalf("legacy migration defaults = conflicts:%d conflict_fts:%d search_fts:%d, want all 0", conflictsSurfaced, conflictFTSErrors, searchFTSErrors)
	}
	assertSchemaMigrationCount(t, db, len(telemetryMigrations))

	// Second application is a pure no-op.
	if err := applyMigrations(db); err != nil {
		t.Fatalf("applyMigrations second pass: %v", err)
	}
	assertSchemaMigrationCount(t, db, len(telemetryMigrations))
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
		id := c.LogToolCall(ToolCallInput{Tool: tc.tool, ConflictsSurfaced: tc.count, ConflictFTSQueryErrors: tc.count + 10})
		if id == 0 {
			t.Fatalf("LogToolCall(%q) returned 0", tc.name)
		}
		ids[tc.name] = id
	}

	rdb := openRawTelemetryDB(t, path)
	for _, tc := range cases {
		var stored, storedFTSErrors int
		if err := rdb.QueryRow(`SELECT conflicts_surfaced, conflict_fts_query_errors FROM tool_calls WHERE id = ?`, ids[tc.name]).Scan(&stored, &storedFTSErrors); err != nil {
			t.Fatalf("readback %q: %v", tc.name, err)
		}
		if stored != tc.count {
			t.Fatalf("%q conflicts_surfaced = %d, want %d", tc.name, stored, tc.count)
		}
		if storedFTSErrors != tc.count+10 {
			t.Fatalf("%q conflict_fts_query_errors = %d, want %d", tc.name, storedFTSErrors, tc.count+10)
		}
	}
}
