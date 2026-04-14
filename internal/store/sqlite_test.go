package store

import (
	"path/filepath"
	"testing"
)

func TestSQLiteCanary(t *testing.T) {
	t.Parallel()

	result, err := RunSQLiteCanary("")
	if err != nil {
		t.Fatalf("RunSQLiteCanary() error = %v", err)
	}
	if result.Driver != sqliteDriverName {
		t.Fatalf("driver = %q, want %q", result.Driver, sqliteDriverName)
	}
	if result.MatchCountBeforeDelete != 1 {
		t.Fatalf("MatchCountBeforeDelete = %d, want 1", result.MatchCountBeforeDelete)
	}
	if result.MatchCountAfterDelete != 0 {
		t.Fatalf("MatchCountAfterDelete = %d, want 0", result.MatchCountAfterDelete)
	}
	if !result.ForeignKeysEnabled {
		t.Fatalf("ForeignKeysEnabled = %t, want true", result.ForeignKeysEnabled)
	}
	if !result.OrphanInsertRejected {
		t.Fatalf("OrphanInsertRejected = %t, want true", result.OrphanInsertRejected)
	}
	if result.PersistedObservationCount != 1 {
		t.Fatalf("PersistedObservationCount = %d, want 1", result.PersistedObservationCount)
	}
}

func TestForgetObservationUsesIndexedEntityTypeSnapshot(t *testing.T) {
	t.Parallel()

	db, err := openSQLite(t.TempDir() + "/snapshot.db")
	if err != nil {
		t.Fatalf("openSQLite() error = %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() error = %v", err)
	}

	entityID, err := UpsertEntity(db, "TypeChangingEntity", "original_type")
	if err != nil {
		t.Fatalf("UpsertEntity(original) error = %v", err)
	}
	observationID, err := AddObservation(db, entityID, "observation with original type", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation() error = %v", err)
	}
	if _, err := UpsertEntity(db, "TypeChangingEntity", "updated_type"); err != nil {
		t.Fatalf("UpsertEntity(updated) error = %v", err)
	}

	var observationType string
	if err := db.QueryRow(`SELECT entity_type FROM observations WHERE id = ?`, observationID).Scan(&observationType); err != nil {
		t.Fatalf("select observation entity_type error = %v", err)
	}
	if observationType != "original_type" {
		t.Fatalf("observation entity_type = %q, want %q", observationType, "original_type")
	}

	var currentEntityType string
	if err := db.QueryRow(`SELECT entity_type FROM entities WHERE id = ?`, entityID).Scan(&currentEntityType); err != nil {
		t.Fatalf("select current entity_type error = %v", err)
	}
	if currentEntityType != "updated_type" {
		t.Fatalf("entity entity_type = %q, want %q", currentEntityType, "updated_type")
	}

	deleted, err := ForgetObservation(db, observationID)
	if err != nil {
		t.Fatalf("ForgetObservation() error = %v", err)
	}
	if !deleted {
		t.Fatalf("ForgetObservation() deleted = %t, want true", deleted)
	}

	resultIDs, err := SearchObservationIDs(db, "observation")
	if err != nil {
		t.Fatalf("SearchObservationIDs() error = %v", err)
	}
	if len(resultIDs) != 0 {
		t.Fatalf("SearchObservationIDs() len = %d, want 0", len(resultIDs))
	}

	tombstoned, err := ObservationDeletedAtIsSet(db, observationID)
	if err != nil {
		t.Fatalf("ObservationDeletedAtIsSet() error = %v", err)
	}
	if !tombstoned {
		t.Fatalf("ObservationDeletedAtIsSet() = %t, want true", tombstoned)
	}
}

func TestInitDBMigratesLegacyProjectSchemaBeforeDeletedIndexes(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "legacy-project.db")
	legacyDB, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite(legacy) error = %v", err)
	}

	legacySchema := []string{
		`CREATE TABLE entities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			entity_type TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		);`,
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			source TEXT DEFAULT 'user',
			confidence REAL DEFAULT 1.0,
			access_count INTEGER DEFAULT 0,
			last_accessed TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			event_id INTEGER REFERENCES events(id)
		);`,
		`CREATE TABLE relations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			to_entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation_type TEXT NOT NULL,
			context TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			UNIQUE(from_entity_id, to_entity_id, relation_type)
		);`,
		`CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			label TEXT NOT NULL,
			event_date TEXT,
			event_type TEXT,
			context TEXT,
			expires_at TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);`,
		`CREATE INDEX idx_obs_entity ON observations(entity_id);`,
		`CREATE INDEX idx_obs_content ON observations(content);`,
		`CREATE INDEX idx_rel_from ON relations(from_entity_id);`,
		`CREATE INDEX idx_rel_to ON relations(to_entity_id);`,
		`CREATE INDEX idx_entities_type ON entities(entity_type);`,
		`CREATE INDEX idx_entities_name ON entities(name);`,
		`CREATE INDEX idx_events_date ON events(event_date);`,
		`CREATE INDEX idx_events_type ON events(event_type);`,
		`CREATE INDEX idx_events_label ON events(label);`,
		`CREATE INDEX idx_obs_event ON observations(event_id);`,
		`CREATE VIRTUAL TABLE memory_fts USING fts5(
			entity_name,
			observation_content,
			entity_type,
			content=''
		);`,
	}
	for _, stmt := range legacySchema {
		if _, err := legacyDB.Exec(stmt); err != nil {
			legacyDB.Close()
			t.Fatalf("seed legacy schema statement failed: %v", err)
		}
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("legacyDB.Close() error = %v", err)
	}

	migratedDB, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() on legacy schema error = %v", err)
	}
	defer migratedDB.Close()

	entityID, err := UpsertEntity(migratedDB, "ProjectScopedDebug", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() after migration error = %v", err)
	}
	if _, err := AddObservation(migratedDB, entityID, "fact scoped to project", "session", 1.0); err != nil {
		t.Fatalf("AddObservation() after migration error = %v", err)
	}

	var deletedColumnCount int
	if err := migratedDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('entities') WHERE name = 'deleted_at'`).Scan(&deletedColumnCount); err != nil {
		t.Fatalf("pragma_table_info(entities) error = %v", err)
	}
	if deletedColumnCount != 1 {
		t.Fatalf("entities.deleted_at column count = %d, want 1", deletedColumnCount)
	}

	var deletedIndexCount int
	if err := migratedDB.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('entities') WHERE name = 'idx_entities_deleted'`).Scan(&deletedIndexCount); err != nil {
		t.Fatalf("pragma_index_list(entities) error = %v", err)
	}
	if deletedIndexCount != 1 {
		t.Fatalf("idx_entities_deleted count = %d, want 1", deletedIndexCount)
	}
}
