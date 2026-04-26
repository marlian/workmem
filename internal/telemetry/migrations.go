package telemetry

import (
	"database/sql"
	"fmt"
)

// telemetryMigrations are idempotent ALTER TABLE operations applied to
// pre-existing telemetry tables that were created before a new column was
// introduced. SQLite does not support ADD COLUMN IF NOT EXISTS, so every
// migration is guarded by a PRAGMA table_info check. Each entry must also
// appear in the CREATE TABLE in schema.go so fresh DBs are born at the target
// shape and the ALTER path short-circuits.
var telemetryMigrations = []struct {
	Table  string
	Column string
	Alter  string
}{
	{
		Table:  "tool_calls",
		Column: "conflicts_surfaced",
		Alter:  `ALTER TABLE tool_calls ADD COLUMN conflicts_surfaced INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Table:  "tool_calls",
		Column: "conflict_fts_query_errors",
		Alter:  `ALTER TABLE tool_calls ADD COLUMN conflict_fts_query_errors INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Table:  "search_metrics",
		Column: "fts_query_errors",
		Alter:  `ALTER TABLE search_metrics ADD COLUMN fts_query_errors INTEGER NOT NULL DEFAULT 0`,
	},
}

// applyMigrations brings an existing telemetry DB up to the current schema
// without erroring on columns that already exist. Called after the CREATE
// TABLE IF NOT EXISTS statements in InitIfEnabled, so a freshly-created
// table matches the target shape and every migration becomes a no-op via
// the columnExists guard. Idempotent: safe to run repeatedly.
func applyMigrations(db *sql.DB) error {
	for _, mig := range telemetryMigrations {
		present, err := columnExists(db, mig.Table, mig.Column)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := db.Exec(mig.Alter); err != nil {
			return fmt.Errorf("migrate %s add %s: %w", mig.Table, mig.Column, err)
		}
	}
	return nil
}

// columnExists reports whether a column is present on the given SQLite
// table. The PRAGMA table_info pragma is the idiomatic way to introspect
// SQLite schema; it returns one row per column with (cid, name, type,
// notnull, dflt_value, pk).
//
// INVARIANT: callers MUST pass a hardcoded table literal, never an
// externally-derived string. SQLite does not support parameter binding
// inside a PRAGMA expression, so the table name is interpolated via
// fmt.Sprintf — which would be a SQL injection surface if `table` came
// from untrusted input. The only current call site passes literals from
// telemetryMigrations. Do not export this helper or accept variable table names
// without re-evaluating the contract.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan pragma table_info(%s): %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
