package telemetry

import (
	"database/sql"
	"errors"
	"fmt"
)

// telemetryMigrations are versioned ALTER TABLE operations applied to
// pre-existing telemetry tables that were created before a new column was
// introduced. Each entry must also appear in the CREATE TABLE in schema.go so
// fresh DBs are born at the target shape and get stamped as already applied.
var telemetryMigrations = []telemetryMigration{
	{
		Version: 1,
		Table:   "tool_calls",
		Column:  "conflicts_surfaced",
		Alter:   `ALTER TABLE tool_calls ADD COLUMN conflicts_surfaced INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 2,
		Table:   "tool_calls",
		Column:  "conflict_fts_query_errors",
		Alter:   `ALTER TABLE tool_calls ADD COLUMN conflict_fts_query_errors INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 3,
		Table:   "search_metrics",
		Column:  "fts_query_errors",
		Alter:   `ALTER TABLE search_metrics ADD COLUMN fts_query_errors INTEGER NOT NULL DEFAULT 0`,
	},
}

type telemetryMigration struct {
	Version int
	Table   string
	Column  string
	Alter   string
}

// applyMigrations brings an existing telemetry DB up to the current schema.
// Called after the CREATE TABLE IF NOT EXISTS statements in InitIfEnabled, so a
// freshly-created table matches the target shape and every migration stamps the
// registry without running ALTER. Idempotent: safe to run repeatedly.
func applyMigrations(db *sql.DB) error {
	if _, err := db.Exec(schemaMigrationsCreateSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, mig := range telemetryMigrations {
		if err := applyMigration(db, mig); err != nil {
			return fmt.Errorf("migrate %s add %s: %w", mig.Table, mig.Column, err)
		}
	}
	return nil
}

func applyMigration(db *sql.DB, mig telemetryMigration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	applied, err := migrationApplied(tx, mig.Version)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	present, err := columnExists(tx, mig.Table, mig.Column)
	if err != nil {
		return err
	}
	if !present {
		if _, err := tx.Exec(mig.Alter); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%f', 'now'))`, mig.Version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return tx.Commit()
}

func migrationApplied(db migrationDB, version int) (bool, error) {
	var applied int
	err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&applied)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check schema migration %d: %w", version, err)
}

type migrationDB interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
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
func columnExists(db migrationDB, table, column string) (bool, error) {
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
