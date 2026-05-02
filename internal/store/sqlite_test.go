package store

import (
	"database/sql"
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
	_, releaseProjectDB, err := AcquireDB(defaultDB, projectRoot)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	releaseProjectDB()

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

	_, releaseProjectDB, err := AcquireDB(defaultDB, projectRoot)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	releaseProjectDB()

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

func TestProjectDBCacheEvictsLeastRecentlyUsedIdleHandle(t *testing.T) {
	if err := ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() pre-test error = %v", err)
	}
	root := t.TempDir()
	// Register after t.TempDir so ResetProjectDBs closes SQLite handles before
	// Windows attempts to remove the project directories.
	t.Cleanup(func() {
		if err := ResetProjectDBs(); err != nil {
			t.Fatalf("ResetProjectDBs() cleanup error = %v", err)
		}
	})
	t.Setenv("PROJECT_DB_CACHE_MAX", "2")

	defaultDB, err := InitDB(filepath.Join(root, "default.db"))
	if err != nil {
		t.Fatalf("InitDB(default) error = %v", err)
	}
	defer defaultDB.Close()

	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	projectC := filepath.Join(root, "project-c")

	dbA, releaseA, err := AcquireDB(defaultDB, projectA)
	if err != nil {
		t.Fatalf("AcquireDB(projectA) error = %v", err)
	}
	if _, err := UpsertEntity(dbA, "ProjectA", "test"); err != nil {
		t.Fatalf("UpsertEntity(projectA) error = %v", err)
	}
	releaseA()

	dbB, releaseB, err := AcquireDB(defaultDB, projectB)
	if err != nil {
		t.Fatalf("AcquireDB(projectB) error = %v", err)
	}
	if _, err := UpsertEntity(dbB, "ProjectB", "test"); err != nil {
		t.Fatalf("UpsertEntity(projectB) error = %v", err)
	}
	releaseB()

	dbAAgain, releaseAAgain, err := AcquireDB(defaultDB, projectA)
	if err != nil {
		t.Fatalf("AcquireDB(projectA again) error = %v", err)
	}
	if dbAAgain != dbA {
		t.Fatalf("projectA cache hit returned a different handle before eviction")
	}
	releaseAAgain()

	dbC, releaseC, err := AcquireDB(defaultDB, projectC)
	if err != nil {
		t.Fatalf("AcquireDB(projectC) error = %v", err)
	}
	defer releaseC()

	if err := dbB.Ping(); err == nil {
		t.Fatalf("least-recently-used projectB handle still accepts queries after cap eviction")
	}
	if err := dbA.Ping(); err != nil {
		t.Fatalf("recently used projectA handle was evicted: %v", err)
	}
	if err := dbC.Ping(); err != nil {
		t.Fatalf("new projectC handle is unusable: %v", err)
	}

	dbBReopened, releaseBReopened, err := AcquireDB(defaultDB, projectB)
	if err != nil {
		t.Fatalf("AcquireDB(projectB reopened) error = %v", err)
	}
	defer releaseBReopened()
	if dbBReopened == dbB {
		t.Fatalf("projectB reopened with the closed evicted handle")
	}
	var projectBCount int
	if err := dbBReopened.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = 'ProjectB'`).Scan(&projectBCount); err != nil {
		t.Fatalf("read projectB reopened data error = %v", err)
	}
	if projectBCount != 1 {
		t.Fatalf("projectB reopened count = %d, want persisted data", projectBCount)
	}

	globalID, err := UpsertEntity(defaultDB, "GlobalAfterEviction", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(global) after project eviction error = %v", err)
	}
	if globalID == 0 {
		t.Fatalf("UpsertEntity(global) returned id 0 after project eviction")
	}
}

func TestInitDBRecordsSchemaMigrationsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "migration-registry.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	assertSchemaMigrationCount(t, db, len(schemaMigrations))
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() second pass error = %v", err)
	}
	assertSchemaMigrationCount(t, db, len(schemaMigrations))

	var missingAppliedAt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE applied_at = ''`).Scan(&missingAppliedAt); err != nil {
		t.Fatalf("read schema_migrations applied_at error = %v", err)
	}
	if missingAppliedAt != 0 {
		t.Fatalf("schema_migrations rows with empty applied_at = %d, want 0", missingAppliedAt)
	}
	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "observations", column: "superseded_by"},
		{table: "observations", column: "superseded_at"},
		{table: "observations", column: "superseded_reason"},
		{table: "observations", column: "superseded_by_run"},
		{table: "reconcile_runs", column: "id"},
		{table: "reconcile_runs", column: "trigger_source"},
		{table: "reconcile_decisions", column: "id"},
	} {
		present, err := columnExists(db, check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s.%s) error = %v", check.table, check.column, err)
		}
		if !present {
			t.Fatalf("%s.%s missing after InitDB", check.table, check.column)
		}
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

func TestSearchObservationIDsHonorsSupersessionGuard(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "search-ids-supersession.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "SearchIDsSupersession", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	sourceID, err := AddObservation(db, entityID, "supersededsearchidtoken", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(source) error = %v", err)
	}
	targetID, err := AddObservation(db, entityID, "active replacement search id token", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(target) error = %v", err)
	}
	markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")

	ids, err := SearchObservationIDs(db, "supersededsearchidtoken")
	if err != nil {
		t.Fatalf("SearchObservationIDs() error = %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("SearchObservationIDs returned superseded observation ids: %#v", ids)
	}
}

func TestTouchObservationsSkipsSupersededRows(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "touch-superseded.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	entityID, err := UpsertEntity(db, "TouchSuperseded", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	sourceID, err := AddObservation(db, entityID, "superseded touch source", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(source) error = %v", err)
	}
	targetID, err := AddObservation(db, entityID, "active touch target", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(target) error = %v", err)
	}
	markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")

	if err := TouchObservations(db, []int64{sourceID, targetID}); err != nil {
		t.Fatalf("TouchObservations() error = %v", err)
	}

	var sourceAccessCount, targetAccessCount int
	if err := db.QueryRow(`SELECT access_count FROM observations WHERE id = ?`, sourceID).Scan(&sourceAccessCount); err != nil {
		t.Fatalf("read source access_count error = %v", err)
	}
	if err := db.QueryRow(`SELECT access_count FROM observations WHERE id = ?`, targetID).Scan(&targetAccessCount); err != nil {
		t.Fatalf("read target access_count error = %v", err)
	}
	if sourceAccessCount != 0 {
		t.Fatalf("superseded source access_count = %d, want 0", sourceAccessCount)
	}
	if targetAccessCount != 1 {
		t.Fatalf("active target access_count = %d, want 1", targetAccessCount)
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
	for _, table := range []string{"reconcile_runs", "reconcile_decisions"} {
		exists, err := tableExists(migratedDB, table)
		if err != nil {
			t.Fatalf("tableExists(%s) error = %v", table, err)
		}
		if !exists {
			t.Fatalf("%s table missing after migration", table)
		}
	}
	assertSchemaMigrationCount(t, migratedDB, len(schemaMigrations))
	if err := InitSchema(migratedDB); err != nil {
		t.Fatalf("InitSchema() second pass on legacy migration error = %v", err)
	}
	assertSchemaMigrationCount(t, migratedDB, len(schemaMigrations))
}

func TestInitDBMigratesPreRegistryLegacySchema(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pre-registry-legacy.db")
	legacyDB, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite(legacy) error = %v", err)
	}
	legacySchema := []string{
		`CREATE TABLE entities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			entity_type TEXT
		);`,
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			source TEXT DEFAULT 'user',
			confidence REAL DEFAULT 1.0,
			access_count INTEGER DEFAULT 0,
			last_accessed TEXT,
			created_at TEXT DEFAULT (datetime('now'))
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
	}
	for _, stmt := range legacySchema {
		if _, err := legacyDB.Exec(stmt); err != nil {
			legacyDB.Close()
			t.Fatalf("seed pre-registry legacy schema statement failed: %v", err)
		}
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("legacyDB.Close() error = %v", err)
	}

	migratedDB, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() on pre-registry legacy schema error = %v", err)
	}
	defer migratedDB.Close()
	assertSchemaMigrationCount(t, migratedDB, len(schemaMigrations))

	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "entities", column: "deleted_at"},
		{table: "observations", column: "event_id"},
		{table: "observations", column: "deleted_at"},
		{table: "observations", column: "entity_type"},
		{table: "observations", column: "superseded_by"},
		{table: "observations", column: "superseded_at"},
		{table: "observations", column: "superseded_reason"},
		{table: "observations", column: "superseded_by_run"},
		{table: "entities", column: "created_at"},
		{table: "entities", column: "updated_at"},
		{table: "reconcile_runs", column: "id"},
		{table: "reconcile_runs", column: "trigger_source"},
		{table: "reconcile_decisions", column: "id"},
	} {
		present, err := columnExists(migratedDB, check.table, check.column)
		if err != nil {
			t.Fatalf("columnExists(%s.%s) error = %v", check.table, check.column, err)
		}
		if !present {
			t.Fatalf("%s.%s missing after migration", check.table, check.column)
		}
	}

	var eventIndexCount int
	if err := migratedDB.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('observations') WHERE name = 'idx_obs_event'`).Scan(&eventIndexCount); err != nil {
		t.Fatalf("pragma_index_list(observations) error = %v", err)
	}
	if eventIndexCount != 1 {
		t.Fatalf("idx_obs_event count = %d, want 1", eventIndexCount)
	}

	entityID, err := UpsertEntity(migratedDB, "LegacyTimestampProbe", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() after timestamp migration error = %v", err)
	}
	var createdAt, updatedAt string
	if err := migratedDB.QueryRow(`SELECT created_at, updated_at FROM entities WHERE id = ?`, entityID).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read migrated entity timestamps error = %v", err)
	}
	if createdAt == "" || updatedAt == "" {
		t.Fatalf("created_at=%q updated_at=%q, want populated timestamps", createdAt, updatedAt)
	}

	if _, err := migratedDB.Exec(`INSERT INTO entities (name, entity_type) VALUES ('DirectLegacyInsert', 'test')`); err != nil {
		t.Fatalf("direct legacy entity insert error = %v", err)
	}
	if err := migratedDB.QueryRow(`SELECT created_at, updated_at FROM entities WHERE name = 'DirectLegacyInsert'`).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read direct legacy insert timestamps error = %v", err)
	}
	if createdAt == "" || updatedAt == "" {
		t.Fatalf("direct insert created_at=%q updated_at=%q, want trigger-populated timestamps", createdAt, updatedAt)
	}
}

func tableExists(tdb *sql.DB, table string) (bool, error) {
	var count int
	if err := tdb.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
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
