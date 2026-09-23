package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ProjectMode selects where an instance keeps project-scoped memory.
// See DECISION_LOG 2026-09-23.
type ProjectMode string

const (
	// ProjectModeLegacy stores project memory at <project>/.memory/memory.db.
	ProjectModeLegacy ProjectMode = "legacy"
	// ProjectModeCentral stores project memory under one root, located through
	// a registry that maps canonical project paths to opaque ids.
	ProjectModeCentral ProjectMode = "central"
	// ProjectModeDisabled rejects every non-empty project argument.
	ProjectModeDisabled ProjectMode = "disabled"
)

const (
	projectModeEnv       = "MEMORY_PROJECT_MODE"
	projectsRootEnv      = "MEMORY_PROJECTS_ROOT"
	defaultProjectsDir   = "projects"
	registryDBName       = "registry.db"
	projectIDRandomBytes = 6
	projectIDSlugMaxLen  = 40
)

// ErrProjectScopeDisabled is returned when a project argument reaches an
// instance whose project mode is disabled.
var ErrProjectScopeDisabled = errors.New("project-scoped memory is disabled for this instance (MEMORY_PROJECT_MODE=disabled)")

// ErrProjectNotRegistered is returned when a lookup that must not create a
// store finds no registry entry for the project.
var ErrProjectNotRegistered = errors.New("project is not registered in the central project store")

// ProjectStoreConfig is the resolved project storage policy of one instance.
type ProjectStoreConfig struct {
	Mode ProjectMode
	// Root is the central store directory. It is only meaningful in central mode.
	Root string
}

// ProjectStoreConfigFromEnv resolves MEMORY_PROJECT_MODE and
// MEMORY_PROJECTS_ROOT. globalDBPath is the instance's global DB path; the
// default root sits beside it so each instance gets its own project store.
// Unknown modes and relative roots are errors rather than silent fallbacks.
func ProjectStoreConfigFromEnv(globalDBPath string) (ProjectStoreConfig, error) {
	rawMode := strings.ToLower(strings.TrimSpace(os.Getenv(projectModeEnv)))
	mode := ProjectModeLegacy
	switch ProjectMode(rawMode) {
	case "", ProjectModeLegacy:
	case ProjectModeCentral, ProjectModeDisabled:
		mode = ProjectMode(rawMode)
	default:
		return ProjectStoreConfig{}, fmt.Errorf("invalid %s %q (use legacy, central or disabled)", projectModeEnv, rawMode)
	}
	if mode != ProjectModeCentral {
		return ProjectStoreConfig{Mode: mode}, nil
	}

	root := strings.TrimSpace(os.Getenv(projectsRootEnv))
	if root == "" {
		if strings.TrimSpace(globalDBPath) == "" {
			return ProjectStoreConfig{}, fmt.Errorf("%s is required when the global DB path is unknown", projectsRootEnv)
		}
		absGlobal, err := filepath.Abs(globalDBPath)
		if err != nil {
			return ProjectStoreConfig{}, fmt.Errorf("resolve global db path for default projects root: %w", err)
		}
		root = filepath.Join(filepath.Dir(absGlobal), defaultProjectsDir)
	} else if !filepath.IsAbs(root) {
		return ProjectStoreConfig{}, fmt.Errorf("%s must be an absolute path, got %q", projectsRootEnv, root)
	}
	return ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Clean(root)}, nil
}

// ConfigureProjectStore installs the instance's project storage policy. It
// must be called before project-scoped tools run. Reconfiguring to a different
// policy while project handles are cached is refused so that no handle opened
// under one mode is served under another.
func ConfigureProjectStore(config ProjectStoreConfig) error {
	if config.Mode == "" {
		config.Mode = ProjectModeLegacy
	}
	if config.Mode == ProjectModeCentral && !filepath.IsAbs(config.Root) {
		return fmt.Errorf("central project store root must be absolute, got %q", config.Root)
	}
	projectDBMu.Lock()
	defer projectDBMu.Unlock()
	if config != projectStore && len(projectDBs) > 0 {
		return fmt.Errorf("cannot change project store configuration with %d cached project DB(s)", len(projectDBs))
	}
	projectStore = config
	projectPathIDs = map[string]string{}
	return nil
}

// CurrentProjectStore returns the configured project storage policy.
func CurrentProjectStore() ProjectStoreConfig {
	projectDBMu.Lock()
	defer projectDBMu.Unlock()
	return projectStore
}

// CanonicalProjectPath resolves a project argument the way central mode keys
// it: home-relative expansion as in ResolveProjectPath, then absolute, cleaned
// and symlink-resolved. The target must be an existing directory; central mode
// never creates project directories.
func CanonicalProjectPath(project string) (string, error) {
	if strings.TrimSpace(project) == "" {
		return "", fmt.Errorf("project path is empty")
	}
	resolved, err := filepath.Abs(ResolveProjectPath(project, ""))
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("project directory does not exist: %s", resolved)
		}
		return "", fmt.Errorf("resolve project path symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat project directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", canonical)
	}
	return canonical, nil
}

// ProjectRecord is one registry entry.
type ProjectRecord struct {
	ID            string
	CanonicalPath string
	CreatedAt     string
	UpdatedAt     string
	Initialized   bool
}

// ProjectDBPath returns the DB file of a registered project under root.
func ProjectDBPath(root string, id string) string {
	return filepath.Join(root, id, projectMemoryDBName)
}

const registrySchemaSQL = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	canonical_path TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	initialized_at TEXT
)`

// OpenProjectRegistry opens (creating if needed) the registry of a central
// store root. The root and registry are private (0700/0600).
func OpenProjectRegistry(root string) (*sql.DB, error) {
	if err := ensurePrivateDir(root); err != nil {
		return nil, fmt.Errorf("create projects root: %w", err)
	}
	registryPath := filepath.Join(root, registryDBName)
	db, err := openSQLite(registryPath)
	if err != nil {
		return nil, fmt.Errorf("open project registry: %w", err)
	}
	if _, err := db.Exec(registrySchemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("init project registry: %w", err)
	}
	hardenSQLiteFiles(registryPath)
	return db, nil
}

// LookupProject returns the registry entry for a canonical path, or
// ErrProjectNotRegistered.
func LookupProject(registry *sql.DB, canonicalPath string) (ProjectRecord, error) {
	return scanProjectRecord(registry.QueryRow(
		`SELECT id, canonical_path, created_at, updated_at, initialized_at IS NOT NULL FROM projects WHERE canonical_path = ?`,
		canonicalPath,
	))
}

// ListProjects returns all registry entries ordered by path.
func ListProjects(registry *sql.DB) ([]ProjectRecord, error) {
	rows, err := registry.Query(`SELECT id, canonical_path, created_at, updated_at, initialized_at IS NOT NULL FROM projects ORDER BY canonical_path`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var records []ProjectRecord
	for rows.Next() {
		var record ProjectRecord
		if err := rows.Scan(&record.ID, &record.CanonicalPath, &record.CreatedAt, &record.UpdatedAt, &record.Initialized); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// registerProject returns the id for canonicalPath, inserting a new
// uninitialized entry when absent. Concurrent registrations of the same path
// (also across processes) converge on the single row that wins the UNIQUE
// constraint.
func registerProject(registry *sql.DB, canonicalPath string) (ProjectRecord, error) {
	id, err := newProjectID(canonicalPath)
	if err != nil {
		return ProjectRecord{}, err
	}
	if _, err := registry.Exec(
		`INSERT INTO projects (id, canonical_path) VALUES (?, ?) ON CONFLICT(canonical_path) DO NOTHING`,
		id, canonicalPath,
	); err != nil {
		return ProjectRecord{}, fmt.Errorf("register project: %w", err)
	}
	return LookupProject(registry, canonicalPath)
}

func markProjectInitialized(registry *sql.DB, id string) error {
	if _, err := registry.Exec(
		`UPDATE projects SET initialized_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND initialized_at IS NULL`,
		id,
	); err != nil {
		return fmt.Errorf("mark project initialized: %w", err)
	}
	return nil
}

// MoveProject re-points a registry entry from oldPath to newPath. oldPath is
// matched after absolute/clean normalization because the old directory
// usually no longer exists; newPath must canonicalize to an existing directory.
func MoveProject(registry *sql.DB, oldPath string, newPath string) (ProjectRecord, error) {
	oldKey, err := registryKeyForPossiblyMissingPath(oldPath)
	if err != nil {
		return ProjectRecord{}, err
	}
	newKey, err := CanonicalProjectPath(newPath)
	if err != nil {
		return ProjectRecord{}, err
	}
	if oldKey == newKey {
		return ProjectRecord{}, fmt.Errorf("old and new project paths are the same: %s", newKey)
	}
	tx, err := registry.Begin()
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("begin project move: %w", err)
	}
	defer tx.Rollback()
	var existing string
	switch err := tx.QueryRow(`SELECT id FROM projects WHERE canonical_path = ?`, newKey).Scan(&existing); {
	case err == nil:
		return ProjectRecord{}, fmt.Errorf("destination path is already registered as %s: %s", existing, newKey)
	case !errors.Is(err, sql.ErrNoRows):
		return ProjectRecord{}, fmt.Errorf("check destination path: %w", err)
	}
	result, err := tx.Exec(
		`UPDATE projects SET canonical_path = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE canonical_path = ?`,
		newKey, oldKey,
	)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("move project: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ProjectRecord{}, fmt.Errorf("move project: %w", err)
	} else if affected == 0 {
		return ProjectRecord{}, fmt.Errorf("%w: %s", ErrProjectNotRegistered, oldKey)
	}
	if err := tx.Commit(); err != nil {
		return ProjectRecord{}, fmt.Errorf("commit project move: %w", err)
	}
	return LookupProject(registry, newKey)
}

// registryKeyForPossiblyMissingPath canonicalizes paths that still exist and
// falls back to absolute/clean normalization for paths that are gone.
func registryKeyForPossiblyMissingPath(project string) (string, error) {
	if canonical, err := CanonicalProjectPath(project); err == nil {
		return canonical, nil
	}
	if strings.TrimSpace(project) == "" {
		return "", fmt.Errorf("project path is empty")
	}
	resolved, err := filepath.Abs(ResolveProjectPath(project, ""))
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// LegacyProjectDBPath returns the legacy <project>/.memory/memory.db location
// for a canonical project path.
func LegacyProjectDBPath(canonicalPath string) string {
	return filepath.Join(canonicalPath, projectMemoryDirName, projectMemoryDBName)
}

// legacyProjectDBExists reports whether a legacy DB (or any of its SQLite
// sidecars) exists for the project. Any stat error other than "not exist" is
// treated as existing so the caller fails closed.
func legacyProjectDBExists(canonicalPath string) bool {
	base := LegacyProjectDBPath(canonicalPath)
	for _, path := range []string{base, base + "-wal", base + "-journal"} {
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func legacyProjectDBError(canonicalPath string) error {
	return fmt.Errorf(
		"project has a legacy memory DB at %s and is not registered in the central project store; import it with `workmem project import --from %s --path %s` (no legacy fallback in central mode)",
		LegacyProjectDBPath(canonicalPath), LegacyProjectDBPath(canonicalPath), canonicalPath,
	)
}

// ResolveExistingProjectDB returns the DB path of an already existing project
// store under the configured policy without creating anything. It is used by
// CLI commands that operate on existing project memory (reconcile). The label
// identifies the project in reports.
func ResolveExistingProjectDB(project string) (label string, dbPath string, err error) {
	config := CurrentProjectStore()
	switch config.Mode {
	case ProjectModeDisabled:
		return "", "", ErrProjectScopeDisabled
	case ProjectModeCentral:
		canonical, err := CanonicalProjectPath(project)
		if err != nil {
			return "", "", err
		}
		registry, err := OpenProjectRegistry(config.Root)
		if err != nil {
			return "", "", err
		}
		defer registry.Close()
		record, err := LookupProject(registry, canonical)
		if err != nil {
			if errors.Is(err, ErrProjectNotRegistered) && legacyProjectDBExists(canonical) {
				return "", "", legacyProjectDBError(canonical)
			}
			return "", "", err
		}
		return canonical, ProjectDBPath(config.Root, record.ID), nil
	default:
		resolved, legacyPath := ResolveProjectDBPath(project, "")
		return filepath.Clean(resolved), legacyPath, nil
	}
}

// openCentralProjectDB resolves (registering on first use) and opens the
// central store DB for a canonical project path. Caller holds projectDBMu.
func openCentralProjectDB(root string, canonicalPath string) (string, *sql.DB, error) {
	registry, err := OpenProjectRegistry(root)
	if err != nil {
		return "", nil, err
	}
	defer registry.Close()

	record, err := LookupProject(registry, canonicalPath)
	switch {
	case errors.Is(err, ErrProjectNotRegistered):
		if legacyProjectDBExists(canonicalPath) {
			return "", nil, legacyProjectDBError(canonicalPath)
		}
		record, err = registerProject(registry, canonicalPath)
		if err != nil {
			return "", nil, err
		}
	case err != nil:
		return "", nil, err
	}

	dbPath := ProjectDBPath(root, record.ID)
	if record.Initialized {
		// A registered, initialized store whose file is gone is data loss or a
		// manual move; recreating it empty would hide that.
		if _, err := os.Stat(dbPath); err != nil {
			return "", nil, fmt.Errorf("registered project DB for %s (%s) is missing: %w", canonicalPath, record.ID, err)
		}
	}
	if err := ensurePrivateDir(filepath.Dir(dbPath)); err != nil {
		return "", nil, fmt.Errorf("create project store dir: %w", err)
	}
	db, err := InitDB(dbPath)
	if err != nil {
		return "", nil, err
	}
	if !record.Initialized {
		if err := markProjectInitialized(registry, record.ID); err != nil {
			db.Close()
			return "", nil, err
		}
	}
	return record.ID, db, nil
}

func scanProjectRecord(row *sql.Row) (ProjectRecord, error) {
	var record ProjectRecord
	if err := row.Scan(&record.ID, &record.CanonicalPath, &record.CreatedAt, &record.UpdatedAt, &record.Initialized); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRecord{}, ErrProjectNotRegistered
		}
		return ProjectRecord{}, fmt.Errorf("lookup project: %w", err)
	}
	return record, nil
}

// newProjectID builds an opaque id "<slug>-<random hex>". The slug only aids
// humans browsing the root; identity is the random suffix, never the path.
func newProjectID(canonicalPath string) (string, error) {
	suffix := make([]byte, projectIDRandomBytes)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate project id: %w", err)
	}
	return projectSlug(filepath.Base(canonicalPath)) + "-" + hex.EncodeToString(suffix), nil
}

func projectSlug(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			if b.Len() >= projectIDSlugMaxLen {
				break
			}
			continue
		}
		pendingDash = true
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

// ensurePrivateDir creates dir (and parents) with 0700 and tightens it only
// when this call created it, matching the legacy project directory behavior.
func ensurePrivateDir(dir string) error {
	created := false
	if _, err := os.Stat(dir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		created = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if created {
		_ = os.Chmod(dir, 0o700)
	}
	return nil
}

// ImportProject copies an existing memory DB into the central store rooted at
// root and registers it for projectPath. The source is opened read-only and
// never modified or removed. The copy is taken with VACUUM INTO, migrated to
// the current schema, integrity-checked, and only then registered, so a
// running server can never observe a half-imported store. Importing a path
// that is already registered is refused.
func ImportProject(root string, sourceDB string, projectPath string) (ProjectRecord, error) {
	canonical, err := CanonicalProjectPath(projectPath)
	if err != nil {
		return ProjectRecord{}, err
	}
	source, err := existingRegularDBPath(sourceDB)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("import source: %w", err)
	}
	if inside, err := pathWithin(root, source); err != nil {
		return ProjectRecord{}, err
	} else if inside {
		return ProjectRecord{}, fmt.Errorf("import source is inside the central project store: %s", source)
	}

	registry, err := OpenProjectRegistry(root)
	if err != nil {
		return ProjectRecord{}, err
	}
	defer registry.Close()
	if record, err := LookupProject(registry, canonical); err == nil {
		return ProjectRecord{}, fmt.Errorf("project is already registered as %s: %s", record.ID, canonical)
	} else if !errors.Is(err, ErrProjectNotRegistered) {
		return ProjectRecord{}, err
	}

	id, err := newProjectID(canonical)
	if err != nil {
		return ProjectRecord{}, err
	}
	targetDir := filepath.Join(root, id)
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		return ProjectRecord{}, fmt.Errorf("create project store dir: %w", err)
	}
	registered := false
	defer func() {
		if !registered {
			_ = os.RemoveAll(targetDir)
		}
	}()
	target := ProjectDBPath(root, id)

	if err := copySQLiteSnapshot(source, target); err != nil {
		return ProjectRecord{}, err
	}
	imported, err := InitDB(target)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("migrate imported db: %w", err)
	}
	checkErr := sqliteIntegrityOK(imported)
	closeProjectDBs([]*sql.DB{imported})
	if checkErr != nil {
		return ProjectRecord{}, fmt.Errorf("imported db: %w", checkErr)
	}
	hardenSQLiteFiles(target)

	result, err := registry.Exec(
		`INSERT INTO projects (id, canonical_path, initialized_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) ON CONFLICT(canonical_path) DO NOTHING`,
		id, canonical,
	)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("register imported project: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ProjectRecord{}, fmt.Errorf("register imported project: %w", err)
	} else if affected == 0 {
		return ProjectRecord{}, fmt.Errorf("project was registered concurrently during import: %s", canonical)
	}
	registered = true
	return LookupProject(registry, canonical)
}

func copySQLiteSnapshot(source string, target string) error {
	src, err := OpenReadOnlyDB(source)
	if err != nil {
		return fmt.Errorf("open import source read-only: %w", err)
	}
	defer src.Close()
	if err := sqliteIntegrityOK(src); err != nil {
		return fmt.Errorf("import source: %w", err)
	}
	if _, err := src.Exec(`VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("copy import source: %w", err)
	}
	return nil
}

func sqliteIntegrityOK(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

// pathWithin reports whether path resolves inside root (symlinks resolved
// where they exist).
func pathWithin(root string, path string) (bool, error) {
	resolve := func(p string) (string, error) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real, nil
		}
		return filepath.Clean(abs), nil
	}
	rootReal, err := resolve(root)
	if err != nil {
		return false, fmt.Errorf("resolve projects root: %w", err)
	}
	pathReal, err := resolve(path)
	if err != nil {
		return false, fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil {
		return false, nil
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}
