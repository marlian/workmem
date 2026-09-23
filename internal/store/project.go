package store

import (
	"container/list"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	projectDBMu  sync.Mutex
	projectDBs   = map[string]*projectDBEntry{}
	projectDBLRU = list.New()
	// projectStore is the instance policy installed by ConfigureProjectStore;
	// the zero value behaves as legacy mode.
	projectStore ProjectStoreConfig
	// projectPathIDs memoizes canonical project path -> registry id in central
	// mode so cache hits skip the registry. Guarded by projectDBMu.
	projectPathIDs = map[string]string{}
)

const (
	projectMemoryDirName = ".memory"
	projectMemoryDBName  = "memory.db"
)

type projectDBEntry struct {
	db   *sql.DB
	refs int
	elem *list.Element
}

func ResolveProjectPath(project string, homedir string) string {
	if homedir == "" {
		homedir, _ = os.UserHomeDir()
	}
	if filepath.IsAbs(project) {
		return project
	}
	if project == "~" {
		return homedir
	}
	if len(project) >= 2 && project[0] == '~' && (project[1] == '/' || project[1] == '\\') {
		return filepath.Join(homedir, project[2:])
	}
	return filepath.Join(homedir, project)
}

func ResolveProjectDBPath(project string, homedir string) (string, string) {
	resolved := ResolveProjectPath(project, homedir)
	return resolved, filepath.Join(resolved, projectMemoryDirName, projectMemoryDBName)
}

// AcquireDB returns the global DB for empty project scope or a leased project
// DB handle for project scope. Callers must invoke the returned release
// function once they are done with the handle so idle project handles can be
// evicted when PROJECT_DB_CACHE_MAX is exceeded. The release function is
// idempotent.
func AcquireDB(defaultDB *sql.DB, project string) (*sql.DB, func(), error) {
	if project == "" {
		return defaultDB, func() {}, nil
	}

	projectDBMu.Lock()
	switch projectStore.Mode {
	case ProjectModeDisabled:
		projectDBMu.Unlock()
		return nil, nil, ErrProjectScopeDisabled
	case ProjectModeCentral:
		return acquireCentralDBLocked(projectStore.Root, project)
	}

	resolved, dbPath := ResolveProjectDBPath(project, "")

	if existing, ok := projectDBs[resolved]; ok {
		existing.refs++
		projectDBLRU.MoveToFront(existing.elem)
		projectDBMu.Unlock()
		return existing.db, projectDBRelease(resolved), nil
	}

	dbDir := filepath.Dir(dbPath)
	created := false
	if _, err := os.Stat(dbDir); err != nil {
		if !os.IsNotExist(err) {
			projectDBMu.Unlock()
			return nil, nil, fmt.Errorf("stat project memory dir: %w", err)
		}
		created = true
	}
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		projectDBMu.Unlock()
		return nil, nil, fmt.Errorf("create project memory dir: %w", err)
	}
	if created {
		_ = os.Chmod(dbDir, 0o700)
	}
	db, err := InitDB(dbPath)
	if err != nil {
		projectDBMu.Unlock()
		return nil, nil, err
	}
	projectDBs[resolved] = &projectDBEntry{
		db:   db,
		refs: 1,
		elem: projectDBLRU.PushFront(resolved),
	}
	toClose := evictProjectDBsLocked(projectDBCacheMax())
	projectDBMu.Unlock()
	closeProjectDBs(toClose)
	return db, projectDBRelease(resolved), nil
}

// acquireCentralDBLocked serves a central-mode project DB. It is entered with
// projectDBMu held and always releases it. Cache entries are keyed by registry
// id so a project reached through two spellings of its path shares one handle.
func acquireCentralDBLocked(root string, project string) (*sql.DB, func(), error) {
	// Canonicalization stats the filesystem; keep it outside the lock.
	projectDBMu.Unlock()
	canonical, err := CanonicalProjectPath(project)
	if err != nil {
		return nil, nil, err
	}
	projectDBMu.Lock()

	if id, ok := projectPathIDs[canonical]; ok {
		if existing, ok := projectDBs[id]; ok {
			existing.refs++
			projectDBLRU.MoveToFront(existing.elem)
			projectDBMu.Unlock()
			return existing.db, projectDBRelease(id), nil
		}
	}

	id, db, err := openCentralProjectDB(root, canonical)
	if err != nil {
		projectDBMu.Unlock()
		return nil, nil, err
	}
	projectPathIDs[canonical] = id
	if existing, ok := projectDBs[id]; ok {
		// Same project reached through another path spelling: keep the cached
		// handle and drop the duplicate.
		existing.refs++
		projectDBLRU.MoveToFront(existing.elem)
		projectDBMu.Unlock()
		closeProjectDBs([]*sql.DB{db})
		return existing.db, projectDBRelease(id), nil
	}
	projectDBs[id] = &projectDBEntry{
		db:   db,
		refs: 1,
		elem: projectDBLRU.PushFront(id),
	}
	toClose := evictProjectDBsLocked(projectDBCacheMax())
	projectDBMu.Unlock()
	closeProjectDBs(toClose)
	return db, projectDBRelease(id), nil
}

func projectDBRelease(resolved string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			projectDBMu.Lock()
			if entry, ok := projectDBs[resolved]; ok && entry.refs > 0 {
				entry.refs--
			}
			toClose := evictProjectDBsLocked(projectDBCacheMax())
			projectDBMu.Unlock()
			closeProjectDBs(toClose)
		})
	}
}

func evictProjectDBsLocked(maxEntries int) []*sql.DB {
	if maxEntries <= 0 {
		maxEntries = defaultProjectDBCacheMax
	}

	var toClose []*sql.DB
	for len(projectDBs) > maxEntries {
		var evicted bool
		for elem := projectDBLRU.Back(); elem != nil; {
			prev := elem.Prev()
			key, ok := elem.Value.(string)
			if !ok {
				projectDBLRU.Remove(elem)
				elem = prev
				continue
			}
			entry := projectDBs[key]
			if entry != nil && entry.refs == 0 {
				delete(projectDBs, key)
				projectDBLRU.Remove(elem)
				toClose = append(toClose, entry.db)
				evicted = true
				break
			}
			elem = prev
		}
		if !evicted {
			break
		}
	}
	return toClose
}

func closeProjectDBs(dbs []*sql.DB) {
	for _, db := range dbs {
		// Checkpoint WAL before close — ensures Windows releases file handles.
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		_ = db.Close()
	}
}

func ResetProjectDBs() error {
	projectDBMu.Lock()

	toClose := make([]*sql.DB, 0, len(projectDBs))
	activeRefs := 0
	for key, entry := range projectDBs {
		if entry != nil {
			activeRefs += entry.refs
			toClose = append(toClose, entry.db)
		}
		delete(projectDBs, key)
	}
	projectDBLRU.Init()
	projectPathIDs = map[string]string{}
	projectDBMu.Unlock()

	var firstErr error
	for _, db := range toClose {
		// Checkpoint WAL before close — ensures Windows releases file handles
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if activeRefs > 0 && firstErr == nil {
		firstErr = fmt.Errorf("reset project DBs with %d active lease(s)", activeRefs)
	}
	return firstErr
}
