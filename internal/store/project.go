package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	projectDBMu sync.Mutex
	projectDBs  = map[string]*sql.DB{}
)

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

func GetDB(defaultDB *sql.DB, project string) (*sql.DB, error) {
	if project == "" {
		return defaultDB, nil
	}

	resolved := ResolveProjectPath(project, "")
	projectDBMu.Lock()
	defer projectDBMu.Unlock()

	if existing, ok := projectDBs[resolved]; ok {
		return existing, nil
	}

	dbDir := filepath.Join(resolved, ".memory")
	created := false
	if _, err := os.Stat(dbDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat project memory dir: %w", err)
		}
		created = true
	}
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return nil, fmt.Errorf("create project memory dir: %w", err)
	}
	if created {
		_ = os.Chmod(dbDir, 0o700)
	}
	dbPath := filepath.Join(dbDir, "memory.db")
	db, err := InitDB(dbPath)
	if err != nil {
		return nil, err
	}
	projectDBs[resolved] = db
	return db, nil
}

func ResetProjectDBs() error {
	projectDBMu.Lock()
	defer projectDBMu.Unlock()

	var firstErr error
	for key, db := range projectDBs {
		// Checkpoint WAL before close — ensures Windows releases file handles
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(projectDBs, key)
	}
	return firstErr
}
