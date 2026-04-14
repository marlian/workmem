// Package telemetry provides opt-in usage analytics for the workmem MCP server.
//
// The package is a no-op when MEMORY_TELEMETRY_PATH is unset — InitIfEnabled
// returns nil, and every method of *Client returns immediately when the
// receiver is nil. There is no global state, no side channel: the server
// passes a *Client down to wherever logging happens.
//
// When MEMORY_TELEMETRY_PRIVACY=strict is set, entity/query/label values in
// args_summary and search_metrics.query are sha256-hashed. observation
// content and structured array values are always reduced to counts, in any
// mode.
package telemetry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Client is a telemetry sink. A nil *Client is valid and represents the
// disabled state — every method below checks for nil and returns harmlessly.
type Client struct {
	db           *sql.DB
	insertCall   *sql.Stmt
	insertSearch *sql.Stmt
	strict       bool
}

// FromEnv reads MEMORY_TELEMETRY_PATH and MEMORY_TELEMETRY_PRIVACY from the
// environment and delegates to InitIfEnabled. Returns nil when the path is
// unset (telemetry disabled). Strict mode is enabled when
// MEMORY_TELEMETRY_PRIVACY is exactly "strict"; any other value (including
// empty) means permissive.
func FromEnv() *Client {
	return InitIfEnabled(
		os.Getenv("MEMORY_TELEMETRY_PATH"),
		os.Getenv("MEMORY_TELEMETRY_PRIVACY") == "strict",
	)
}

// InitIfEnabled opens (or creates) the telemetry SQLite database at the given
// path. If path is empty, returns nil (telemetry disabled). If init fails at
// any step, logs a single warning to stderr and returns nil — telemetry is
// never allowed to break the tool call path.
func InitIfEnabled(path string, strict bool) *Client {
	if path == "" {
		return nil
	}
	// Mirror the main memory DB's open pattern (see internal/store/sqlite.go
	// openSQLite): a file: DSN with foreign_keys set at open time, cleaned
	// path for cross-platform safety (notably Windows drive letters), single
	// open connection for deterministic write ordering under concurrent tool
	// calls, WAL for reader-friendly durability, and a non-zero busy_timeout
	// so brief lock contention retries instead of erroring immediately.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.Clean(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		initWarn(err)
		return nil
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		initWarn(err)
		return nil
	}
	// PRAGMA foreign_keys is also set via the DSN (_pragma=foreign_keys(1)),
	// but openSQLite in the main store issues the PRAGMA explicitly after
	// open anyway — some driver/version combinations honor one but not the
	// other. Belt-and-suspenders.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			initWarn(err)
			return nil
		}
	}
	for i, stmt := range schemaStatements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			initWarn(fmt.Errorf("schema statement %d: %w", i, err))
			return nil
		}
	}
	insertCall, err := db.Prepare(insertCallSQL)
	if err != nil {
		_ = db.Close()
		initWarn(err)
		return nil
	}
	insertSearch, err := db.Prepare(insertSearchSQL)
	if err != nil {
		_ = insertCall.Close()
		_ = db.Close()
		initWarn(err)
		return nil
	}
	return &Client{db: db, insertCall: insertCall, insertSearch: insertSearch, strict: strict}
}

func initWarn(err error) {
	fmt.Fprintf(os.Stderr, "[memory] telemetry init failed (disabled for this session): %v\n", err)
}

// Close releases the prepared statements and the underlying database
// connection. Safe to call on a nil receiver. Idempotent: after the first
// call, fields are nil-ed so subsequent calls return nil instead of
// trying to close an already-closed *sql.DB (which would surface a
// confusing shutdown error).
func (c *Client) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	insertCall := c.insertCall
	insertSearch := c.insertSearch
	db := c.db
	c.insertCall = nil
	c.insertSearch = nil
	c.db = nil
	if insertCall != nil {
		_ = insertCall.Close()
	}
	if insertSearch != nil {
		_ = insertSearch.Close()
	}
	return db.Close()
}

// Strict reports whether privacy-strict mode is active.
func (c *Client) Strict() bool {
	if c == nil {
		return false
	}
	return c.strict
}

// ToolCallInput captures a single tool invocation for telemetry.
type ToolCallInput struct {
	Tool          string
	Client        ClientInfo
	DBScope       string // "global" or "project"
	ProjectPath   string
	DurationMs    float64
	ArgsSummary   string
	ResultSummary string
	IsError       bool
}

// LogToolCall inserts a tool_calls row. Returns the new row id on success or
// 0 on failure / disabled client. The returned id is used by LogSearchMetrics
// to link the search_metrics row back to its parent tool call.
func (c *Client) LogToolCall(in ToolCallInput) int64 {
	if c == nil {
		return 0
	}
	dbScope := in.DBScope
	if dbScope == "" {
		dbScope = "global"
	}
	res, err := c.insertCall.Exec(
		in.Tool,
		nullIfEmpty(in.Client.Name),
		nullIfEmpty(in.Client.Version),
		nullIfEmpty(in.Client.Source),
		dbScope,
		nullIfEmpty(in.ProjectPath),
		in.DurationMs,
		nullIfEmpty(in.ArgsSummary),
		nullIfEmpty(in.ResultSummary),
		boolToInt(in.IsError),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[memory] telemetry log failed: %v\n", err)
		return 0
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0
	}
	return id
}

// SearchMetricsInput captures ranking-pipeline metrics for a single recall call.
type SearchMetricsInput struct {
	ToolCallID      int64
	Query           string
	Channels        map[string]int
	CandidatesTotal int
	ResultsReturned int
	LimitRequested  int
	ScoreMin        float64
	ScoreMax        float64
	ScoreMedian     float64
	Compact         bool
}

// LogSearchMetrics inserts a search_metrics row linked to the tool_call id.
// No-op when client is nil or ToolCallID is 0 (the linking parent failed).
// In strict mode, Query is hashed before insertion.
func (c *Client) LogSearchMetrics(in SearchMetricsInput) {
	if c == nil || in.ToolCallID == 0 {
		return
	}
	channelsJSON, err := json.Marshal(in.Channels)
	if err != nil {
		channelsJSON = []byte("{}")
	}
	query := hashIfStrict(in.Query, c.strict)
	if _, err := c.insertSearch.Exec(
		in.ToolCallID,
		nullIfEmpty(query),
		string(channelsJSON),
		in.CandidatesTotal,
		in.ResultsReturned,
		in.LimitRequested,
		in.ScoreMin,
		in.ScoreMax,
		in.ScoreMedian,
		boolToInt(in.Compact),
	); err != nil {
		fmt.Fprintf(os.Stderr, "[memory] telemetry search log failed: %v\n", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
