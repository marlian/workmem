package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const sqliteDriverName = "sqlite"

const schemaMigrationsCreateSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);`

type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type schemaMigration struct {
	Version int
	Table   string
	Column  string
	SQL     string
	PostSQL []string
}

var schemaMigrations = []schemaMigration{
	{
		Version: 1,
		Table:   "observations",
		Column:  "event_id",
		SQL:     `ALTER TABLE observations ADD COLUMN event_id INTEGER REFERENCES events(id)`,
	},
	{
		Version: 2,
		Table:   "entities",
		Column:  "deleted_at",
		SQL:     `ALTER TABLE entities ADD COLUMN deleted_at TEXT`,
	},
	{
		Version: 3,
		Table:   "observations",
		Column:  "deleted_at",
		SQL:     `ALTER TABLE observations ADD COLUMN deleted_at TEXT`,
	},
	{
		Version: 4,
		Table:   "observations",
		Column:  "entity_type",
		SQL:     `ALTER TABLE observations ADD COLUMN entity_type TEXT`,
	},
	{
		Version: 5,
		Table:   "observations",
		Column:  "access_count",
		SQL:     `ALTER TABLE observations ADD COLUMN access_count INTEGER DEFAULT 0`,
	},
	{
		Version: 6,
		Table:   "observations",
		Column:  "last_accessed",
		SQL:     `ALTER TABLE observations ADD COLUMN last_accessed TEXT`,
	},
	{
		Version: 7,
		Table:   "entities",
		Column:  "created_at",
		SQL:     `ALTER TABLE entities ADD COLUMN created_at TEXT`,
		PostSQL: []string{`UPDATE entities SET created_at = COALESCE(created_at, CURRENT_TIMESTAMP)`},
	},
	{
		Version: 8,
		Table:   "entities",
		Column:  "updated_at",
		SQL:     `ALTER TABLE entities ADD COLUMN updated_at TEXT`,
		PostSQL: []string{`UPDATE entities SET updated_at = COALESCE(updated_at, CURRENT_TIMESTAMP)`},
	},
	{
		Version: 9,
		Table:   "reconcile_runs",
		Column:  "id",
		SQL: `CREATE TABLE IF NOT EXISTS reconcile_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
			mode TEXT NOT NULL,
			trigger_source TEXT,
			scope TEXT NOT NULL,
			scanned_entities INTEGER NOT NULL DEFAULT 0,
			candidates_proposed INTEGER NOT NULL DEFAULT 0,
			supersessions_applied INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			notes TEXT
		);`,
	},
	{
		Version: 10,
		Table:   "observations",
		Column:  "superseded_by",
		SQL:     `ALTER TABLE observations ADD COLUMN superseded_by INTEGER REFERENCES observations(id)`,
	},
	{
		Version: 11,
		Table:   "observations",
		Column:  "superseded_at",
		SQL:     `ALTER TABLE observations ADD COLUMN superseded_at TEXT`,
	},
	{
		Version: 12,
		Table:   "observations",
		Column:  "superseded_reason",
		SQL:     `ALTER TABLE observations ADD COLUMN superseded_reason TEXT`,
	},
	{
		Version: 13,
		Table:   "observations",
		Column:  "superseded_by_run",
		SQL:     `ALTER TABLE observations ADD COLUMN superseded_by_run INTEGER REFERENCES reconcile_runs(id)`,
	},
	{
		Version: 14,
		Table:   "reconcile_decisions",
		Column:  "id",
		SQL: `CREATE TABLE IF NOT EXISTS reconcile_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES reconcile_runs(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			entity_id INTEGER REFERENCES entities(id),
			source_obs_ids TEXT NOT NULL,
			target_obs_id INTEGER REFERENCES observations(id),
			content_snapshot TEXT,
			similarity REAL,
			action TEXT NOT NULL,
			rationale TEXT,
			reverted_at TEXT,
			reverted_by_run INTEGER REFERENCES reconcile_runs(id)
		);`,
	},
	{
		Version: 15,
		Table:   "reconcile_decisions",
		Column:  "content_snapshot",
		SQL:     `ALTER TABLE reconcile_decisions ADD COLUMN content_snapshot TEXT`,
	},
	{
		Version: 16,
		Table:   "observation_embeddings",
		Column:  "observation_id",
		SQL: `CREATE TABLE IF NOT EXISTS observation_embeddings (
			observation_id INTEGER NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
			provider TEXT NOT NULL CHECK (trim(provider) <> ''),
			endpoint_key TEXT NOT NULL CHECK (trim(endpoint_key) <> ''),
			model_id TEXT NOT NULL CHECK (trim(model_id) <> ''),
			dimensions INTEGER NOT NULL CHECK (dimensions > 0),
			embedding BLOB NOT NULL CHECK (typeof(embedding) = 'blob' AND length(embedding) > 0),
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
			PRIMARY KEY (observation_id, provider, endpoint_key, model_id, dimensions)
		);`,
	},
}

type CanaryResult struct {
	Driver                    string
	DatabasePath              string
	ObservationID             int64
	MatchCountBeforeDelete    int
	MatchCountAfterDelete     int
	ForeignKeysEnabled        bool
	OrphanInsertRejected      bool
	PersistedObservationCount int
}

func RunSQLiteCanary(dbPath string) (CanaryResult, error) {
	path := dbPath
	tempDir := ""
	if path == "" {
		var err error
		tempDir, err = os.MkdirTemp("", "workmem-sqlite-canary-")
		if err != nil {
			return CanaryResult{}, fmt.Errorf("create temp dir: %w", err)
		}
		path = filepath.Join(tempDir, "canary.db")
	}
	if tempDir != "" {
		defer os.RemoveAll(tempDir)
	}

	result, err := runSQLiteCanaryAtPath(path)
	if err != nil {
		return CanaryResult{}, err
	}
	return result, nil
}

func runSQLiteCanaryAtPath(dbPath string) (CanaryResult, error) {
	db, err := openSQLite(dbPath)
	if err != nil {
		return CanaryResult{}, err
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		return CanaryResult{}, err
	}

	foreignKeysEnabled, err := ForeignKeysEnabled(db)
	if err != nil {
		return CanaryResult{}, err
	}
	if !foreignKeysEnabled {
		return CanaryResult{}, errors.New("sqlite foreign_keys pragma is disabled")
	}

	orphanInsertRejected, err := RejectsOrphanObservationInsert(db)
	if err != nil {
		return CanaryResult{}, err
	}
	if !orphanInsertRejected {
		return CanaryResult{}, errors.New("orphan observation insert unexpectedly succeeded")
	}

	entityID, err := UpsertEntity(db, "TypeChangingEntity", "original_type")
	if err != nil {
		return CanaryResult{}, err
	}
	observationID, err := AddObservation(db, entityID, "observation to forget", "user", 1.0)
	if err != nil {
		return CanaryResult{}, err
	}

	beforeDelete, err := SearchObservationIDs(db, "observation")
	if err != nil {
		return CanaryResult{}, err
	}
	if len(beforeDelete) != 1 || beforeDelete[0] != observationID {
		return CanaryResult{}, fmt.Errorf("unexpected FTS results before delete: %v", beforeDelete)
	}

	if _, err := UpsertEntity(db, "TypeChangingEntity", "updated_type"); err != nil {
		return CanaryResult{}, err
	}

	deleted, err := ForgetObservation(db, observationID)
	if err != nil {
		return CanaryResult{}, err
	}
	if !deleted {
		return CanaryResult{}, errors.New("forget observation reported no deletion")
	}

	afterDelete, err := SearchObservationIDs(db, "observation")
	if err != nil {
		return CanaryResult{}, err
	}
	if len(afterDelete) != 0 {
		return CanaryResult{}, fmt.Errorf("expected FTS results to be empty after delete, got %v", afterDelete)
	}

	deletedAtValid, err := ObservationDeletedAtIsSet(db, observationID)
	if err != nil {
		return CanaryResult{}, err
	}
	if !deletedAtValid {
		return CanaryResult{}, errors.New("observation tombstone was not persisted")
	}

	if err := db.Close(); err != nil {
		return CanaryResult{}, fmt.Errorf("close db before reopen: %w", err)
	}

	reopened, err := openSQLite(dbPath)
	if err != nil {
		return CanaryResult{}, err
	}
	defer reopened.Close()

	persistedCount, err := CountObservationRows(reopened)
	if err != nil {
		return CanaryResult{}, err
	}
	if persistedCount != 1 {
		return CanaryResult{}, fmt.Errorf("expected 1 persisted observation row, got %d", persistedCount)
	}

	return CanaryResult{
		Driver:                    sqliteDriverName,
		DatabasePath:              dbPath,
		ObservationID:             observationID,
		MatchCountBeforeDelete:    len(beforeDelete),
		MatchCountAfterDelete:     len(afterDelete),
		ForeignKeysEnabled:        foreignKeysEnabled,
		OrphanInsertRejected:      orphanInsertRejected,
		PersistedObservationCount: persistedCount,
	}, nil
}

func openSQLite(dbPath string) (*sql.DB, error) {
	cleanPath := filepath.Clean(dbPath)
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)", sqliteFileURI(cleanPath))
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	hardenSQLiteFiles(cleanPath)
	return db, nil
}

func OpenReadOnlyDB(dbPath string) (*sql.DB, error) {
	cleanPath, err := existingRegularDBPath(dbPath)
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("%s?mode=ro&_pragma=foreign_keys(1)", sqliteFileURI(cleanPath))
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping read-only sqlite: %w", err)
	}
	return db, nil
}

func OpenExistingDB(dbPath string) (*sql.DB, error) {
	cleanPath, err := existingRegularDBPath(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := openSQLite(cleanPath)
	if err != nil {
		return nil, err
	}
	if err := InitSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	hardenSQLiteFiles(cleanPath)
	return db, nil
}

func existingRegularDBPath(dbPath string) (string, error) {
	trimmedPath := strings.TrimSpace(dbPath)
	if trimmedPath == "" {
		return "", fmt.Errorf("memory db path is empty")
	}
	cleanPath := filepath.Clean(trimmedPath)
	info, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("memory db does not exist: %s", cleanPath)
		}
		return "", fmt.Errorf("stat memory db: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("memory db path is not a regular file: %s", cleanPath)
	}
	return cleanPath, nil
}

func sqliteFileURI(path string) string {
	escapedPath := strings.NewReplacer(
		"%", "%25",
		"?", "%3F",
		"#", "%23",
	).Replace(filepath.ToSlash(path))
	return "file:" + escapedPath
}

func InitDB(dbPath string) (*sql.DB, error) {
	db, err := openSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	if err := InitSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	hardenSQLiteFiles(filepath.Clean(dbPath))
	return db, nil
}

func hardenSQLiteFiles(dbPath string) {
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		_ = os.Chmod(path, 0o600)
	}
}

func InitSchema(db *sql.DB) error {
	stmts := []string{
		schemaMigrationsCreateSQL,
		`CREATE TABLE IF NOT EXISTS entities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			entity_type TEXT,
			deleted_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS reconcile_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
			mode TEXT NOT NULL,
			trigger_source TEXT,
			scope TEXT NOT NULL,
			scanned_entities INTEGER NOT NULL DEFAULT 0,
			candidates_proposed INTEGER NOT NULL DEFAULT 0,
			supersessions_applied INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			notes TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER NOT NULL,
			content TEXT NOT NULL,
			source TEXT DEFAULT 'user',
			confidence REAL DEFAULT 1.0,
			access_count INTEGER DEFAULT 0,
			last_accessed TEXT,
			event_id INTEGER,
			entity_type TEXT,
			deleted_at TEXT,
			superseded_by INTEGER REFERENCES observations(id),
			superseded_at TEXT,
			superseded_reason TEXT,
			superseded_by_run INTEGER REFERENCES reconcile_runs(id),
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(event_id) REFERENCES events(id)
		);`,
		`CREATE TABLE IF NOT EXISTS relations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			to_entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation_type TEXT NOT NULL,
			context TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(from_entity_id, to_entity_id, relation_type)
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			label TEXT NOT NULL,
			event_date TEXT,
			event_type TEXT,
			context TEXT,
			expires_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS reconcile_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES reconcile_runs(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			entity_id INTEGER REFERENCES entities(id),
			source_obs_ids TEXT NOT NULL,
			target_obs_id INTEGER REFERENCES observations(id),
			content_snapshot TEXT,
			similarity REAL,
			action TEXT NOT NULL,
			rationale TEXT,
			reverted_at TEXT,
			reverted_by_run INTEGER REFERENCES reconcile_runs(id)
		);`,
		`CREATE TABLE IF NOT EXISTS observation_embeddings (
			observation_id INTEGER NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
			provider TEXT NOT NULL CHECK (trim(provider) <> ''),
			endpoint_key TEXT NOT NULL CHECK (trim(endpoint_key) <> ''),
			model_id TEXT NOT NULL CHECK (trim(model_id) <> ''),
			dimensions INTEGER NOT NULL CHECK (dimensions > 0),
			embedding BLOB NOT NULL CHECK (typeof(embedding) = 'blob' AND length(embedding) > 0),
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now')),
			PRIMARY KEY (observation_id, provider, endpoint_key, model_id, dimensions)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_obs_entity ON observations(entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_obs_content ON observations(content);`,
		`CREATE INDEX IF NOT EXISTS idx_rel_from ON relations(from_entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_rel_to ON relations(to_entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(entity_type);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);`,
		`CREATE INDEX IF NOT EXISTS idx_events_date ON events(event_date);`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);`,
		`CREATE INDEX IF NOT EXISTS idx_events_label ON events(label);`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			entity_name,
			observation_content,
			entity_type,
			content=''
		);`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}

	if err := applySchemaMigrations(db); err != nil {
		return err
	}

	postMigrationStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_obs_event ON observations(event_id);`,
		`CREATE INDEX IF NOT EXISTS idx_obs_active_entity_content ON observations(entity_id, content) WHERE deleted_at IS NULL AND superseded_by IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_obs_active_event ON observations(event_id) WHERE deleted_at IS NULL AND superseded_by IS NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_obs_superseded ON observations(superseded_by) WHERE superseded_by IS NOT NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_obs_superseded_run ON observations(superseded_by_run) WHERE superseded_by_run IS NOT NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_reconcile_decisions_run ON reconcile_decisions(run_id);`,
		`CREATE INDEX IF NOT EXISTS idx_reconcile_decisions_kind ON reconcile_decisions(kind);`,
		`CREATE INDEX IF NOT EXISTS idx_reconcile_decisions_entity ON reconcile_decisions(entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_observation_embeddings_model ON observation_embeddings(provider, endpoint_key, model_id, dimensions);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_deleted ON entities(deleted_at);`,
		`CREATE INDEX IF NOT EXISTS idx_obs_deleted ON observations(deleted_at);`,
		`CREATE TRIGGER IF NOT EXISTS trg_entities_insert_timestamps
			AFTER INSERT ON entities
			WHEN NEW.created_at IS NULL OR NEW.updated_at IS NULL
			BEGIN
				UPDATE entities
				SET created_at = COALESCE(created_at, CURRENT_TIMESTAMP),
					updated_at = COALESCE(updated_at, CURRENT_TIMESTAMP)
				WHERE id = NEW.id;
			END;`,
	}
	for _, stmt := range postMigrationStmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init post-migration schema: %w", err)
		}
	}
	return nil
}

func applySchemaMigrations(db *sql.DB) error {
	if _, err := db.Exec(schemaMigrationsCreateSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, migration := range schemaMigrations {
		if err := applySchemaMigration(db, migration); err != nil {
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
	}
	return nil
}

func applySchemaMigration(db *sql.DB, migration schemaMigration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	applied, err := migrationApplied(tx, migration.Version)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	present, err := columnExists(tx, migration.Table, migration.Column)
	if err != nil {
		return err
	}
	if !present {
		if _, err := tx.Exec(migration.SQL); err != nil {
			return fmt.Errorf("execute schema migration: %w", err)
		}
		for _, stmt := range migration.PostSQL {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("execute schema migration post-step: %w", err)
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%f', 'now'))`, migration.Version); err != nil {
		return fmt.Errorf("record schema migration: %w", err)
	}
	return tx.Commit()
}

func migrationApplied(db dbtx, version int) (bool, error) {
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

// columnExists reports whether a column is present on the given SQLite table.
// Callers must pass hardcoded table literals; SQLite does not support binding
// identifiers in PRAGMA table_info.
func columnExists(db dbtx, table, column string) (bool, error) {
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

func UpsertEntity(db dbtx, name, entityType string) (int64, error) {
	var existingID int64
	var existingType sql.NullString
	var deletedAt sql.NullString
	err := db.QueryRow(
		`SELECT id, entity_type, deleted_at FROM entities WHERE name = ? COLLATE NOCASE`,
		name,
	).Scan(&existingID, &existingType, &deletedAt)
	if err == nil {
		if deletedAt.Valid || (entityType != "" && entityType != existingType.String) {
			if _, updateErr := db.Exec(
				`UPDATE entities SET entity_type = ?, deleted_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				nullableString(entityType, existingType.String),
				existingID,
			); updateErr != nil {
				return 0, fmt.Errorf("update entity: %w", updateErr)
			}
		}
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup entity: %w", err)
	}

	result, err := db.Exec(`INSERT INTO entities (name, entity_type, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, name, nullableString(entityType, ""))
	if err != nil {
		return 0, fmt.Errorf("insert entity: %w", err)
	}
	entityID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("entity last insert id: %w", err)
	}
	return entityID, nil
}

func AddObservation(db dbtx, entityID int64, content, source string, confidence float64, eventID ...int64) (int64, error) {
	if source == "" {
		source = "user"
	}
	if confidence < 0 {
		confidence = 1.0
	}
	var entityName string
	var entityType sql.NullString
	if err := db.QueryRow(`SELECT name, entity_type FROM entities WHERE id = ?`, entityID).Scan(&entityName, &entityType); err != nil {
		return 0, fmt.Errorf("select entity for observation: %w", err)
	}

	var eventValue any
	if len(eventID) > 0 && eventID[0] > 0 {
		if err := ensureEventIsActive(db, eventID[0]); err != nil {
			return 0, err
		}
		eventValue = eventID[0]
	}

	var duplicateID int64
	duplicateErr := db.QueryRow(
		fmt.Sprintf(`SELECT o.id FROM observations o WHERE o.entity_id = ? AND o.content = ? AND %s`, activeObservationSQL("o")),
		entityID,
		content,
	).Scan(&duplicateID)
	if duplicateErr == nil {
		return duplicateID, nil
	}
	if !errors.Is(duplicateErr, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup duplicate observation: %w", duplicateErr)
	}

	result, err := db.Exec(
		`INSERT INTO observations (entity_id, content, source, confidence, event_id, entity_type) VALUES (?, ?, ?, ?, ?, ?)`,
		entityID,
		content,
		source,
		confidence,
		eventValue,
		nullableString(entityType.String, ""),
	)
	if err != nil {
		return 0, fmt.Errorf("insert observation: %w", err)
	}
	observationID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("observation last insert id: %w", err)
	}

	if _, err := db.Exec(
		`INSERT INTO memory_fts (rowid, entity_name, observation_content, entity_type) VALUES (?, ?, ?, ?)`,
		observationID,
		entityName,
		content,
		nullableString(entityType.String, ""),
	); err != nil {
		return 0, fmt.Errorf("insert memory_fts row: %w", err)
	}

	return observationID, nil
}

func ensureEventIsActive(db dbtx, eventID int64) error {
	var id int64
	err := db.QueryRow(
		fmt.Sprintf(`SELECT e.id FROM events e WHERE e.id = ? AND %s`, activeEventSQL("e")),
		eventID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("event %d not found or expired", eventID)
	}
	if err != nil {
		return fmt.Errorf("select active event: %w", err)
	}
	return nil
}

func TouchObservations(db *sql.DB, observationIDs []int64) error {
	if len(observationIDs) == 0 {
		return nil
	}
	const chunkSize = 900
	for start := 0; start < len(observationIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(observationIDs) {
			end = len(observationIDs)
		}
		chunk := observationIDs[start:end]
		placeholders := placeholders(len(chunk))
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		if _, err := db.Exec(
			fmt.Sprintf(`UPDATE observations SET access_count = access_count + 1, last_accessed = CURRENT_TIMESTAMP
				WHERE id IN (%s) AND %s
				  AND EXISTS (SELECT 1 FROM entities e WHERE e.id = observations.entity_id AND e.deleted_at IS NULL)`, placeholders, activeObservationSQL("observations")),
			args...,
		); err != nil {
			return fmt.Errorf("touch observations: %w", err)
		}
	}
	return nil
}

func SearchObservationIDs(db *sql.DB, query string) ([]int64, error) {
	ftsQuery := strings.TrimSpace(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := db.Query(fmt.Sprintf(`
		SELECT memory_fts.rowid
		FROM memory_fts
		JOIN observations o ON o.id = memory_fts.rowid
		JOIN entities e ON e.id = o.entity_id
		WHERE memory_fts MATCH ? AND %s AND e.deleted_at IS NULL
		ORDER BY memory_fts.rank
	`, activeObservationSQL("o")), quoteFTSTerms(ftsQuery))
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan fts row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fts rows: %w", err)
	}
	return ids, nil
}

func ForgetObservation(db *sql.DB, observationID int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin forget observation: %w", err)
	}
	defer tx.Rollback()

	var entityName string
	var content string
	var entityType sql.NullString
	err = tx.QueryRow(
		`SELECT e.name, o.content, o.entity_type
		 FROM observations o
		 JOIN entities e ON e.id = o.entity_id
		 WHERE o.id = ? AND o.deleted_at IS NULL AND e.deleted_at IS NULL`,
		observationID,
	).Scan(&entityName, &content, &entityType)
	if errors.Is(err, sql.ErrNoRows) {
		if err := cleanupObservationForTombstonedEntityDrift(tx, observationID); err != nil {
			return false, err
		}
		if err := deleteObservationEmbeddings(tx, observationID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("select observation for forget: %w", err)
	}

	if err := deleteObservationFTS(tx, observationID, entityName, content, entityType); err != nil {
		return false, err
	}

	result, err := tx.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`, observationID)
	if err != nil {
		return false, fmt.Errorf("tombstone observation: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	if err := deleteObservationEmbeddings(tx, observationID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit forget observation: %w", err)
	}
	return rowsAffected > 0, nil
}

func ForgetEntity(db *sql.DB, entity string) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin forget entity: %w", err)
	}
	defer tx.Rollback()

	var entityID int64
	var entityDeletedAt sql.NullString
	err = tx.QueryRow(`SELECT id, deleted_at FROM entities WHERE name = ? COLLATE NOCASE`, entity).Scan(&entityID, &entityDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit empty forget entity: %w", commitErr)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select entity for forget: %w", err)
	}
	if entityDeletedAt.Valid {
		if err := deleteEntityObservationFTS(tx, entityID); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM relations WHERE from_entity_id = ? OR to_entity_id = ?`, entityID, entityID); err != nil {
			return false, fmt.Errorf("delete tombstoned entity relations: %w", err)
		}
		if err := deleteEntityObservationEmbeddings(tx, entityID); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE entity_id = ? AND deleted_at IS NULL`, entityID); err != nil {
			return false, fmt.Errorf("tombstone tombstoned-entity observations: %w", err)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit tombstoned forget entity cleanup: %w", commitErr)
		}
		return false, nil
	}

	if err := deleteEntityObservationFTS(tx, entityID); err != nil {
		return false, err
	}

	if _, err := tx.Exec(`DELETE FROM relations WHERE from_entity_id = ? OR to_entity_id = ?`, entityID, entityID); err != nil {
		return false, fmt.Errorf("delete entity relations: %w", err)
	}
	if err := deleteEntityObservationEmbeddings(tx, entityID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE entity_id = ? AND deleted_at IS NULL`, entityID); err != nil {
		return false, fmt.Errorf("tombstone entity observations: %w", err)
	}
	if _, err := tx.Exec(`UPDATE entities SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`, entityID); err != nil {
		return false, fmt.Errorf("tombstone entity: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit forget entity: %w", err)
	}
	return true, nil
}

func deleteObservationEmbeddings(db dbtx, observationID int64) error {
	if _, err := db.Exec(`DELETE FROM observation_embeddings WHERE observation_id = ?`, observationID); err != nil {
		return fmt.Errorf("delete observation embeddings: %w", err)
	}
	return nil
}

func cleanupObservationForTombstonedEntityDrift(db dbtx, observationID int64) error {
	var entityName string
	var content string
	var entityType sql.NullString
	var observationDeletedAt sql.NullString
	var entityDeletedAt sql.NullString
	err := db.QueryRow(
		`SELECT e.name, o.content, o.entity_type, o.deleted_at, e.deleted_at
		 FROM observations o
		 JOIN entities e ON e.id = o.entity_id
		 WHERE o.id = ?`,
		observationID,
	).Scan(&entityName, &content, &entityType, &observationDeletedAt, &entityDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select observation FTS cleanup state: %w", err)
	}
	if observationDeletedAt.Valid || !entityDeletedAt.Valid {
		return nil
	}
	if err := deleteObservationFTS(db, observationID, entityName, content, entityType); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`, observationID); err != nil {
		return fmt.Errorf("tombstone observation hidden by tombstoned entity: %w", err)
	}
	return nil
}

func deleteEntityObservationFTS(db dbtx, entityID int64) error {
	rows, err := db.Query(
		`SELECT o.id, o.content, o.entity_type, e.name
		 FROM observations o
		 JOIN entities e ON e.id = o.entity_id
		 WHERE o.entity_id = ? AND o.deleted_at IS NULL`,
		entityID,
	)
	if err != nil {
		return fmt.Errorf("query entity observations for FTS cleanup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var observationID int64
		var content string
		var entityType sql.NullString
		var entityName string
		if err := rows.Scan(&observationID, &content, &entityType, &entityName); err != nil {
			return fmt.Errorf("scan entity observation for FTS cleanup: %w", err)
		}
		if err := deleteObservationFTS(db, observationID, entityName, content, entityType); err != nil {
			return fmt.Errorf("fts special delete for entity: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate entity observations for FTS cleanup: %w", err)
	}
	return nil
}

func deleteObservationFTS(db dbtx, observationID int64, entityName string, content string, entityType sql.NullString) error {
	if _, err := db.Exec(
		`INSERT INTO memory_fts(memory_fts, rowid, entity_name, observation_content, entity_type) VALUES('delete', ?, ?, ?, ?)`,
		observationID,
		entityName,
		content,
		nullableString(entityType.String, ""),
	); err != nil {
		return fmt.Errorf("fts special delete: %w", err)
	}
	return nil
}

func deleteEntityObservationEmbeddings(db dbtx, entityID int64) error {
	if _, err := db.Exec(`DELETE FROM observation_embeddings WHERE observation_id IN (SELECT id FROM observations WHERE entity_id = ?)`, entityID); err != nil {
		return fmt.Errorf("delete entity observation embeddings: %w", err)
	}
	return nil
}

func ObservationDeletedAtIsSet(db *sql.DB, observationID int64) (bool, error) {
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, observationID).Scan(&deletedAt); err != nil {
		return false, fmt.Errorf("select observation tombstone: %w", err)
	}
	return deletedAt.Valid, nil
}

func CountObservationRows(db *sql.DB) (int, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count observations: %w", err)
	}
	return count, nil
}

func ForeignKeysEnabled(db *sql.DB) (bool, error) {
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys;`).Scan(&enabled); err != nil {
		return false, fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	return enabled == 1, nil
}

func RejectsOrphanObservationInsert(db *sql.DB) (bool, error) {
	_, err := db.Exec(
		`INSERT INTO observations (entity_id, content, source, confidence, entity_type) VALUES (?, ?, ?, ?, ?)`,
		999999,
		"orphan observation",
		"user",
		1.0,
		nil,
	)
	if err == nil {
		return false, nil
	}
	if !isForeignKeyConstraint(err) {
		return false, fmt.Errorf("orphan observation insert failed for non-foreign-key reason: %w", err)
	}
	return true, nil
}

func isForeignKeyConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}

func quoteFTSTerms(query string) string {
	parts := strings.Fields(query)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := strings.ReplaceAll(part, `"`, "")
		if clean == "" {
			continue
		}
		quoted = append(quoted, `"`+clean+`"`)
	}
	return strings.Join(quoted, " OR ")
}

func nullableString(primary, fallback string) any {
	value := primary
	if value == "" {
		value = fallback
	}
	if value == "" {
		return nil
	}
	return value
}

func placeholders(count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}
