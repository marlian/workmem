package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
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
	projectModeEnv        = "MEMORY_PROJECT_MODE"
	projectsRootEnv       = "MEMORY_PROJECTS_ROOT"
	defaultRootSuffix     = "-projects"
	registryDBName        = "registry.db"
	discardedStoresDir    = "discarded"
	projectIDRandomBytes  = 6
	projectIDSlugMaxLen   = 40
	registryTimestampExpr = `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`
)

// projectIDPattern is the only shape a registry id may have. Ids read back
// from registry.db are validated before being joined onto the root, so a
// tampered registry cannot point a project at a path outside the root.
var projectIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ErrProjectScopeDisabled is returned when a project argument reaches an
// instance whose project mode is disabled.
var ErrProjectScopeDisabled = errors.New("project-scoped memory is disabled for this instance (MEMORY_PROJECT_MODE=disabled)")

// ErrProjectNotRegistered is returned when a lookup that must not create a
// store finds no registry entry for the project.
var ErrProjectNotRegistered = errors.New("project is not registered in the central project store")

// ErrProjectRegistryMissing is returned when an operation that must not
// create state finds no registry.db under the configured root.
var ErrProjectRegistryMissing = errors.New("no project registry under the central project store root")

// ProjectStoreConfig is the resolved project storage policy of one instance.
type ProjectStoreConfig struct {
	Mode ProjectMode
	// Root is the central store directory. It is only meaningful in central mode.
	Root string
}

func parseProjectMode(raw string, source string) (ProjectMode, error) {
	switch mode := ProjectMode(strings.ToLower(strings.TrimSpace(raw))); mode {
	case "":
		return "", nil
	case ProjectModeLegacy, ProjectModeCentral, ProjectModeDisabled:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid %s %q (use legacy, central or disabled)", source, strings.TrimSpace(raw))
	}
}

// ResolveProjectStoreConfig resolves the instance's project storage policy.
// modeOverride (the serve -project-mode flag) wins over MEMORY_PROJECT_MODE:
// client args are explicit per server entry, while environment variables can
// be inherited from a shell profile. globalDBPath is the instance's global DB;
// the default central root is derived from its file name so two global DBs in
// one directory never share a project store. Unknown modes, relative roots and
// a root without central mode are errors rather than silent fallbacks.
func ResolveProjectStoreConfig(modeOverride string, globalDBPath string) (ProjectStoreConfig, error) {
	mode, err := parseProjectMode(modeOverride, "-project-mode")
	if err != nil {
		return ProjectStoreConfig{}, err
	}
	if mode == "" {
		if mode, err = parseProjectMode(os.Getenv(projectModeEnv), projectModeEnv); err != nil {
			return ProjectStoreConfig{}, err
		}
	}
	if mode == "" {
		mode = ProjectModeLegacy
	}

	root := strings.TrimSpace(os.Getenv(projectsRootEnv))
	if mode != ProjectModeCentral {
		if root != "" {
			return ProjectStoreConfig{}, fmt.Errorf("%s is set but the project mode is %s; it is only valid with central mode", projectsRootEnv, mode)
		}
		return ProjectStoreConfig{Mode: mode}, nil
	}

	if root == "" {
		if strings.TrimSpace(globalDBPath) == "" {
			return ProjectStoreConfig{}, fmt.Errorf("%s is required when the global DB path is unknown", projectsRootEnv)
		}
		absGlobal, err := filepath.Abs(globalDBPath)
		if err != nil {
			return ProjectStoreConfig{}, fmt.Errorf("resolve global db path for default projects root: %w", err)
		}
		root = DefaultProjectsRoot(absGlobal)
	} else if !filepath.IsAbs(root) {
		return ProjectStoreConfig{}, fmt.Errorf("%s must be an absolute path, got %q", projectsRootEnv, root)
	}
	return ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Clean(root)}, nil
}

// DefaultProjectsRoot returns <dir>/<global DB file stem>-projects for an
// absolute global DB path, e.g. /data/memory.db -> /data/memory-projects.
func DefaultProjectsRoot(globalDBPath string) string {
	base := filepath.Base(globalDBPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		stem = base
	}
	return filepath.Join(filepath.Dir(globalDBPath), stem+defaultRootSuffix)
}

// ConfigureProjectStoreFromEnv resolves and installs the project storage
// policy. It is the single setup path for `serve` and every CLI command that
// takes a project scope.
func ConfigureProjectStoreFromEnv(modeOverride string, globalDBPath string) error {
	config, err := ResolveProjectStoreConfig(modeOverride, globalDBPath)
	if err != nil {
		return err
	}
	return ConfigureProjectStore(config)
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
	if config != projectStore && len(projectDBs) > 0 {
		projectDBMu.Unlock()
		return fmt.Errorf("cannot change project store configuration with %d cached project DB(s)", len(projectDBs))
	}
	projectStore = config
	registry := takeCentralRegistryLocked()
	projectDBMu.Unlock()
	if registry != nil {
		_ = registry.Close()
	}
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

// ProjectScopeLabel is the reconcile/report scope label of a central project.
// It is keyed by registry id, not path, so audit runs survive `project move`.
func ProjectScopeLabel(id string) string {
	return "project:" + id
}

const registrySchemaSQL = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	canonical_path TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	initialized_at TEXT
)`

// OpenProjectRegistry opens the registry of an existing central store. It
// never creates anything and returns ErrProjectRegistryMissing when the
// registry file does not exist.
func OpenProjectRegistry(root string) (*sql.DB, error) {
	registryPath := filepath.Join(root, registryDBName)
	if _, err := os.Stat(registryPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrProjectRegistryMissing, root)
		}
		return nil, fmt.Errorf("stat project registry: %w", err)
	}
	return openRegistryFile(registryPath)
}

// openOrCreateProjectRegistry opens the registry, creating the root and
// registry when neither exists yet. A missing registry beside existing
// project stores is an error: silently starting a fresh registry would
// orphan every store and serve empty memory in their place.
func openOrCreateProjectRegistry(root string) (*sql.DB, error) {
	registryPath := filepath.Join(root, registryDBName)
	if _, err := os.Stat(registryPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat project registry: %w", err)
		}
		stores, err := projectStoreDirs(root)
		if err != nil {
			return nil, err
		}
		if len(stores) > 0 {
			return nil, fmt.Errorf("project registry %s is missing but %d project store(s) exist under the root; restore registry.db from a backup instead of starting a new one", registryPath, len(stores))
		}
		if err := ensurePrivateDir(root); err != nil {
			return nil, fmt.Errorf("create projects root: %w", err)
		}
	}
	return openRegistryFile(registryPath)
}

func openRegistryFile(registryPath string) (*sql.DB, error) {
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

// projectStoreDirs lists directories under root that hold a project DB,
// excluding archived (discarded) stores.
func projectStoreDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects root: %w", err)
	}
	var stores []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == discardedStoresDir {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, entry.Name(), projectMemoryDBName)); err == nil {
			stores = append(stores, entry.Name())
		}
	}
	return stores, nil
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
		if err := validateProjectID(record.ID); err != nil {
			return nil, err
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
		`UPDATE projects SET initialized_at = `+registryTimestampExpr+` WHERE id = ? AND initialized_at IS NULL`,
		id,
	); err != nil {
		return fmt.Errorf("mark project initialized: %w", err)
	}
	return nil
}

// MoveResult reports a completed `project move`.
type MoveResult struct {
	Record ProjectRecord
	// DiscardedID is the id of an empty destination store that was archived
	// under <root>/discarded because replaceEmpty was set.
	DiscardedID string
}

// MoveProject re-points the registry entry of oldPath to newPath. oldPath is
// matched as given (absolute and cleaned) before symlink resolution, because
// the old directory is usually gone or has become a compatibility symlink to
// the new one. newPath must canonicalize to an existing directory. When
// newPath is already registered the move is refused, unless replaceEmpty is
// set and that destination store holds no memory (typically created by using
// the new path before running `project move`); it is then archived under
// <root>/discarded, never deleted.
func MoveProject(root string, oldPath string, newPath string, replaceEmpty bool) (MoveResult, error) {
	registry, err := OpenProjectRegistry(root)
	if err != nil {
		return MoveResult{}, err
	}
	defer registry.Close()

	oldRecord, err := lookupPossiblyMovedProject(registry, oldPath)
	if err != nil {
		return MoveResult{}, err
	}
	newKey, err := CanonicalProjectPath(newPath)
	if err != nil {
		return MoveResult{}, err
	}
	if oldRecord.CanonicalPath == newKey {
		return MoveResult{}, fmt.Errorf("project %s is already registered at %s", oldRecord.ID, newKey)
	}

	var discarded ProjectRecord
	switch dest, err := LookupProject(registry, newKey); {
	case err == nil:
		if !replaceEmpty {
			return MoveResult{}, fmt.Errorf("destination path is already registered as %s: %s; if that store was created by using the new path before `project move`, rerun with -replace-empty", dest.ID, newKey)
		}
		empty, err := projectStoreIsEmpty(root, dest)
		if err != nil {
			return MoveResult{}, err
		}
		if !empty {
			return MoveResult{}, fmt.Errorf("destination store %s for %s holds memory; refusing to replace it", dest.ID, newKey)
		}
		discarded = dest
	case !errors.Is(err, ErrProjectNotRegistered):
		return MoveResult{}, err
	}

	tx, err := registry.Begin()
	if err != nil {
		return MoveResult{}, fmt.Errorf("begin project move: %w", err)
	}
	defer tx.Rollback()
	if discarded.ID != "" {
		if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, discarded.ID); err != nil {
			return MoveResult{}, fmt.Errorf("unregister discarded store: %w", err)
		}
	}
	if _, err := tx.Exec(
		`UPDATE projects SET canonical_path = ?, updated_at = `+registryTimestampExpr+` WHERE id = ?`,
		newKey, oldRecord.ID,
	); err != nil {
		return MoveResult{}, fmt.Errorf("move project: %w", err)
	}
	var archivedFrom, archivedTo string
	if discarded.ID != "" {
		archivedFrom = filepath.Join(root, discarded.ID)
		archivedTo = filepath.Join(root, discardedStoresDir, fmt.Sprintf("%s-%s", discarded.ID, time.Now().UTC().Format("20060102T150405Z")))
		if err := ensurePrivateDir(filepath.Dir(archivedTo)); err != nil {
			return MoveResult{}, fmt.Errorf("create discarded dir: %w", err)
		}
		if err := os.Rename(archivedFrom, archivedTo); err != nil && !errors.Is(err, os.ErrNotExist) {
			return MoveResult{}, fmt.Errorf("archive discarded store: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		if archivedTo != "" {
			_ = os.Rename(archivedTo, archivedFrom)
		}
		return MoveResult{}, fmt.Errorf("commit project move: %w", err)
	}
	record, err := LookupProject(registry, newKey)
	if err != nil {
		return MoveResult{}, err
	}
	return MoveResult{Record: record, DiscardedID: discarded.ID}, nil
}

// lookupPossiblyMovedProject finds the registry entry for a path that may no
// longer exist or may now be a compatibility symlink to its new location.
// Registry keys have every symlink resolved, so candidates are tried in order:
// ancestors resolved but the final component kept (covers a vanished
// directory and a final-component symlink, including symlinked parents such
// as macOS /var), the absolute cleaned spelling, then the full canonical form.
func lookupPossiblyMovedProject(registry *sql.DB, project string) (ProjectRecord, error) {
	if strings.TrimSpace(project) == "" {
		return ProjectRecord{}, fmt.Errorf("project path is empty")
	}
	resolved, err := filepath.Abs(ResolveProjectPath(project, ""))
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("resolve project path: %w", err)
	}
	cleaned := filepath.Clean(resolved)
	candidates := []string{filepath.Join(resolveExistingAncestors(filepath.Dir(cleaned)), filepath.Base(cleaned)), cleaned}
	if canonical, canonErr := CanonicalProjectPath(project); canonErr == nil {
		candidates = append(candidates, canonical)
	}
	for _, candidate := range candidates {
		record, err := LookupProject(registry, candidate)
		if err == nil || !errors.Is(err, ErrProjectNotRegistered) {
			return record, err
		}
	}
	return ProjectRecord{}, fmt.Errorf("%w: %s", ErrProjectNotRegistered, cleaned)
}

// resolveExistingAncestors resolves symlinks in the longest existing prefix
// of an absolute path and re-appends the components that do not exist.
func resolveExistingAncestors(path string) string {
	var missing []string
	for current := path; ; {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			parts := append([]string{real}, missing...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

// projectStoreIsEmpty reports whether a store holds no entities,
// observations or events. A store whose DB was never created is empty.
func projectStoreIsEmpty(root string, record ProjectRecord) (bool, error) {
	dbPath := ProjectDBPath(root, record.ID)
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) && !record.Initialized {
			return true, nil
		}
		return false, fmt.Errorf("stat project store %s: %w", record.ID, err)
	}
	db, err := OpenReadOnlyDB(dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM entities) + (SELECT COUNT(*) FROM observations) + (SELECT COUNT(*) FROM events)`).Scan(&rows); err != nil {
		return false, fmt.Errorf("count project store %s: %w", record.ID, err)
	}
	return rows == 0, nil
}

// LegacyProjectDBPath returns the legacy <project>/.memory/memory.db location
// for a canonical project path.
func LegacyProjectDBPath(canonicalPath string) string {
	return filepath.Join(canonicalPath, projectMemoryDirName, projectMemoryDBName)
}

// LegacyProjectDBExists reports whether a legacy DB (or any of its SQLite
// sidecars) exists for the project. Any stat error other than "not exist" is
// treated as existing so callers fail closed.
func LegacyProjectDBExists(canonicalPath string) bool {
	base := LegacyProjectDBPath(canonicalPath)
	for _, path := range []string{base, base + "-wal", base + "-journal"} {
		_, err := os.Lstat(path)
		if err == nil {
			return true
		}
		// ENOTDIR: `.memory` is a regular file, so no legacy DB can exist.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return true
		}
	}
	return false
}

func legacyUnregisteredError(canonicalPath string) error {
	return fmt.Errorf(
		"project has a legacy memory DB at %s and is not registered in the central project store; import it with `workmem project import -from %s -path %s` using this instance's -env-file (central mode never reads legacy DBs)",
		LegacyProjectDBPath(canonicalPath), LegacyProjectDBPath(canonicalPath), canonicalPath,
	)
}

// legacyCoexistsError keeps a registered project unusable while a legacy DB
// is still present, so a leftover legacy writer (an old session, a legacy-mode
// instance) cannot keep writing memory that central mode would never see.
func legacyCoexistsError(canonicalPath string, id string) error {
	return fmt.Errorf(
		"project %s is registered in the central project store as %s but a legacy memory DB is still present at %s; stop legacy sessions, verify the central store, then move the legacy .memory directory out of the project (central mode never reads it)",
		canonicalPath, id, LegacyProjectDBPath(canonicalPath),
	)
}

// ExistingProjectDB locates an existing project store for CLI maintenance.
type ExistingProjectDB struct {
	// Label is the scope label recorded in reconcile runs and reports.
	Label string
	// Aliases are earlier labels of the same DB (the legacy path label of a
	// central project), accepted when matching recorded runs.
	Aliases []string
	DBPath  string
}

// ResolveExistingProjectDB locates an already existing project store under
// the configured policy without creating anything. It is used by CLI commands
// that operate on existing project memory (reconcile).
func ResolveExistingProjectDB(project string) (ExistingProjectDB, error) {
	label, aliases, dbPath, err := resolveExistingProjectDB(project)
	return ExistingProjectDB{Label: label, Aliases: aliases, DBPath: dbPath}, err
}

func resolveExistingProjectDB(project string) (string, []string, string, error) {
	config := CurrentProjectStore()
	switch config.Mode {
	case ProjectModeDisabled:
		return "", nil, "", ErrProjectScopeDisabled
	case ProjectModeCentral:
		canonical, err := CanonicalProjectPath(project)
		if err != nil {
			return "", nil, "", err
		}
		registry, err := OpenProjectRegistry(config.Root)
		if err != nil {
			if errors.Is(err, ErrProjectRegistryMissing) && LegacyProjectDBExists(canonical) {
				return "", nil, "", legacyUnregisteredError(canonical)
			}
			return "", nil, "", err
		}
		defer registry.Close()
		record, err := LookupProject(registry, canonical)
		if err != nil {
			if errors.Is(err, ErrProjectNotRegistered) {
				if LegacyProjectDBExists(canonical) {
					return "", nil, "", legacyUnregisteredError(canonical)
				}
				return "", nil, "", fmt.Errorf("%w: %s (root %s)", ErrProjectNotRegistered, canonical, config.Root)
			}
			return "", nil, "", err
		}
		if LegacyProjectDBExists(canonical) {
			return "", nil, "", legacyCoexistsError(canonical, record.ID)
		}
		// Legacy runs recorded "project:" + the cleaned path as given (symlinks
		// unresolved); accept that spelling and the canonical one.
		aliases := []string{"project:" + canonical}
		if given, _ := ResolveProjectDBPath(project, ""); filepath.Clean(given) != canonical {
			aliases = append(aliases, "project:"+filepath.Clean(given))
		}
		return ProjectScopeLabel(record.ID), aliases, ProjectDBPath(config.Root, record.ID), nil
	default:
		resolved, legacyPath := ResolveProjectDBPath(project, "")
		return "project:" + filepath.Clean(resolved), nil, legacyPath, nil
	}
}

// takeCentralRegistryLocked detaches the cached registry handle so the caller
// can close it outside the lock. Caller holds projectDBMu.
func takeCentralRegistryLocked() *sql.DB {
	registry := centralRegistry
	centralRegistry = nil
	centralRegistryRoot = ""
	return registry
}

// centralRegistryLocked returns the cached registry handle for root, opening
// (and creating when appropriate) it on first use. Caller holds projectDBMu.
func centralRegistryLocked(root string) (*sql.DB, error) {
	if centralRegistry != nil && centralRegistryRoot == root {
		return centralRegistry, nil
	}
	if stale := takeCentralRegistryLocked(); stale != nil {
		_ = stale.Close()
	}
	registry, err := openOrCreateProjectRegistry(root)
	if err != nil {
		return nil, err
	}
	centralRegistry = registry
	centralRegistryRoot = root
	return registry, nil
}

// resolveCentralProjectLocked maps a canonical project path to its registry
// entry, registering it on first use. The registry is consulted on every call
// (one indexed lookup) so a `project move` by another process is honored
// immediately instead of serving a stale path->id mapping. Caller holds
// projectDBMu.
func resolveCentralProjectLocked(root string, canonicalPath string) (ProjectRecord, error) {
	registry, err := centralRegistryLocked(root)
	if err != nil {
		return ProjectRecord{}, err
	}
	record, err := LookupProject(registry, canonicalPath)
	switch {
	case errors.Is(err, ErrProjectNotRegistered):
		if LegacyProjectDBExists(canonicalPath) {
			return ProjectRecord{}, legacyUnregisteredError(canonicalPath)
		}
		return registerProject(registry, canonicalPath)
	case err != nil:
		return ProjectRecord{}, err
	}
	if LegacyProjectDBExists(canonicalPath) {
		return ProjectRecord{}, legacyCoexistsError(canonicalPath, record.ID)
	}
	return record, nil
}

// openCentralProjectDBLocked opens the store of a resolved registry entry,
// creating it on first use. Caller holds projectDBMu.
func openCentralProjectDBLocked(root string, record ProjectRecord) (*sql.DB, error) {
	dbPath := ProjectDBPath(root, record.ID)
	if record.Initialized {
		// A registered, initialized store whose file is gone is data loss or a
		// manual move; recreating it empty would hide that.
		if _, err := os.Stat(dbPath); err != nil {
			return nil, fmt.Errorf("registered project DB for %s (%s) is missing: %w", record.CanonicalPath, record.ID, err)
		}
	}
	if err := ensurePrivateDir(filepath.Dir(dbPath)); err != nil {
		return nil, fmt.Errorf("create project store dir: %w", err)
	}
	db, err := InitDB(dbPath)
	if err != nil {
		return nil, err
	}
	if !record.Initialized {
		if err := markProjectInitialized(centralRegistry, record.ID); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func scanProjectRecord(row *sql.Row) (ProjectRecord, error) {
	var record ProjectRecord
	if err := row.Scan(&record.ID, &record.CanonicalPath, &record.CreatedAt, &record.UpdatedAt, &record.Initialized); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRecord{}, ErrProjectNotRegistered
		}
		return ProjectRecord{}, fmt.Errorf("lookup project: %w", err)
	}
	if err := validateProjectID(record.ID); err != nil {
		return ProjectRecord{}, err
	}
	return record, nil
}

func validateProjectID(id string) error {
	if !projectIDPattern.MatchString(id) {
		return fmt.Errorf("invalid project id %q in registry", id)
	}
	return nil
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
// root and registers it for projectPath. The source is opened read-only; its
// database content is never modified and it is never removed (SQLite may
// still create -shm/-wal sidecars beside a WAL-mode source). The copy is taken
// with VACUUM INTO, migrated to the current schema, integrity-checked, and
// only then registered, so a running server can never observe a half-imported
// store. Importing a path that is already registered is refused.
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

	registry, err := openOrCreateProjectRegistry(root)
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
		`INSERT INTO projects (id, canonical_path, initialized_at) VALUES (?, ?, `+registryTimestampExpr+`) ON CONFLICT(canonical_path) DO NOTHING`,
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
