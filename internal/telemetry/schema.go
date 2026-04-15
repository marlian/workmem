package telemetry

// schemaStatements is executed one-by-one by InitIfEnabled. The main store
// follows the same single-statement-per-Exec pattern (see
// internal/store/sqlite.go InitSchema) — more portable across SQLite
// drivers than chaining multiple statements into one db.Exec call.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS tool_calls (
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
)`,
	`CREATE TABLE IF NOT EXISTS search_metrics (
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
)`,
	`CREATE INDEX IF NOT EXISTS idx_tool_calls_ts ON tool_calls(ts)`,
	`CREATE INDEX IF NOT EXISTS idx_tool_calls_tool ON tool_calls(tool)`,
	`CREATE INDEX IF NOT EXISTS idx_search_metrics_tool_call ON search_metrics(tool_call_id)`,
}

const (
	insertCallSQL = `INSERT INTO tool_calls
	(tool, client_name, client_version, client_source, db_scope, project_path, duration_ms, args_summary, result_summary, is_error)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	insertSearchSQL = `INSERT INTO search_metrics
	(tool_call_id, query, channels, candidates_total, results_returned, limit_requested, score_min, score_max, score_median, compact)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
)
