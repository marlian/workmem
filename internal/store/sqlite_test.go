package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func TestRejectsOrphanObservationInsertOnlyAcceptsForeignKeyFailure(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "fk-specific.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	rejected, err := RejectsOrphanObservationInsert(db)
	if err != nil {
		t.Fatalf("RejectsOrphanObservationInsert() error = %v", err)
	}
	if !rejected {
		t.Fatalf("RejectsOrphanObservationInsert() = false, want true")
	}
	if isForeignKeyConstraint(errors.New("database is locked")) {
		t.Fatalf("isForeignKeyConstraint accepted a non-SQLite FK error")
	}
}

func TestInitDBCreatesPrivateDatabaseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	dbPath := filepath.Join(t.TempDir(), "private.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db file error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("db file mode = %o, want 600", got)
	}
}

func TestInitDBHardensSQLiteSidecarFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	dbPath := filepath.Join(t.TempDir(), "sidecars.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "SidecarEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	if _, err := AddObservation(db, entityID, "sidecar mode probe", "user", 1.0); err != nil {
		t.Fatalf("AddObservation() error = %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat sqlite file %s error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("sqlite file %s mode = %o, want 600", path, got)
		}
	}
}

func TestProjectDBCreatesPrivateMemoryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	defaultDB, err := InitDB(filepath.Join(t.TempDir(), "default.db"))
	if err != nil {
		t.Fatalf("InitDB(default) error = %v", err)
	}
	defer defaultDB.Close()
	t.Cleanup(func() {
		if err := ResetProjectDBs(); err != nil {
			t.Fatalf("ResetProjectDBs() error = %v", err)
		}
	})

	projectRoot := filepath.Join(t.TempDir(), "project")
	if _, err := GetDB(defaultDB, projectRoot); err != nil {
		t.Fatalf("GetDB(project) error = %v", err)
	}

	memoryDir := filepath.Join(projectRoot, ".memory")
	info, err := os.Stat(memoryDir)
	if err != nil {
		t.Fatalf("stat project .memory dir error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("project .memory dir mode = %o, want 700", got)
	}

	dbInfo, err := os.Stat(filepath.Join(memoryDir, "memory.db"))
	if err != nil {
		t.Fatalf("stat project memory.db error = %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("project memory.db mode = %o, want 600", got)
	}
}

func TestProjectDBDoesNotTightenExistingMemoryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}

	defaultDB, err := InitDB(filepath.Join(t.TempDir(), "default.db"))
	if err != nil {
		t.Fatalf("InitDB(default) error = %v", err)
	}
	defer defaultDB.Close()
	t.Cleanup(func() {
		if err := ResetProjectDBs(); err != nil {
			t.Fatalf("ResetProjectDBs() error = %v", err)
		}
	})

	projectRoot := filepath.Join(t.TempDir(), "project-existing")
	memoryDir := filepath.Join(projectRoot, ".memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("mkdir existing project .memory error = %v", err)
	}
	if err := os.Chmod(memoryDir, 0o755); err != nil {
		t.Fatalf("chmod existing project .memory error = %v", err)
	}

	if _, err := GetDB(defaultDB, projectRoot); err != nil {
		t.Fatalf("GetDB(project) error = %v", err)
	}

	info, err := os.Stat(memoryDir)
	if err != nil {
		t.Fatalf("stat project .memory dir error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing project .memory dir mode = %o, want preserved 755", got)
	}

	dbInfo, err := os.Stat(filepath.Join(memoryDir, "memory.db"))
	if err != nil {
		t.Fatalf("stat project memory.db error = %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("project memory.db mode = %o, want 600", got)
	}
}

func TestSearchObservationIDsHonorsExpiryGuard(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "search-ids-expiry.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "SearchIDsExpiry", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	expiredEventID, err := CreateEvent(db, "Expired search ids event", "", "session", "", "")
	if err != nil {
		t.Fatalf("CreateEvent(expired) error = %v", err)
	}
	if _, err := AddObservation(db, entityID, "expiredsearchidtoken", "user", 1.0, expiredEventID); err != nil {
		t.Fatalf("AddObservation(expired) error = %v", err)
	}
	if _, err := db.Exec(`UPDATE events SET expires_at = ? WHERE id = ?`, time.Now().Add(-1*time.Hour).UTC().Format(sqliteTimestampLayout), expiredEventID); err != nil {
		t.Fatalf("expire event error = %v", err)
	}

	ids, err := SearchObservationIDs(db, "expiredsearchidtoken")
	if err != nil {
		t.Fatalf("SearchObservationIDs() error = %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("SearchObservationIDs returned expired observation ids: %#v", ids)
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
