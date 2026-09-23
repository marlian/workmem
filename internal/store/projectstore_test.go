package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// useProjectStore installs config for one test and restores legacy mode and
// an empty handle cache afterwards.
func useProjectStore(t *testing.T, config ProjectStoreConfig) {
	t.Helper()
	if err := ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}
	if err := ConfigureProjectStore(config); err != nil {
		t.Fatalf("ConfigureProjectStore(%+v) error = %v", config, err)
	}
	t.Cleanup(func() {
		if err := ResetProjectDBs(); err != nil {
			t.Errorf("ResetProjectDBs() error = %v", err)
		}
		if err := ConfigureProjectStore(ProjectStoreConfig{Mode: ProjectModeLegacy}); err != nil {
			t.Errorf("restore legacy project store: %v", err)
		}
	})
}

func useCentralStore(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	useProjectStore(t, ProjectStoreConfig{Mode: ProjectModeCentral, Root: root})
	return root
}

func rememberInProject(t *testing.T, project string, entity string, observation string) {
	t.Helper()
	if _, _, err := HandleToolWithMetrics(nil, "remember", ToolArgs{Entity: entity, Observation: observation, Project: project}); err != nil {
		t.Fatalf("remember(project=%s) error = %v", project, err)
	}
}

func projectObservationCount(t *testing.T, project string, entity string) int {
	t.Helper()
	result, _, err := HandleToolWithMetrics(nil, "recall_entity", ToolArgs{Entity: entity, Project: project})
	if err != nil {
		t.Fatalf("recall_entity(project=%s) error = %v", project, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal recall_entity result: %v", err)
	}
	var decoded struct {
		Found        bool              `json:"found"`
		Observations []json.RawMessage `json:"observations"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode recall_entity result: %v", err)
	}
	if !decoded.Found {
		return 0
	}
	return len(decoded.Observations)
}

func registryRecords(t *testing.T, root string) []ProjectRecord {
	t.Helper()
	registry, err := OpenProjectRegistry(root)
	if errors.Is(err, ErrProjectRegistryMissing) {
		return nil
	}
	if err != nil {
		t.Fatalf("OpenProjectRegistry() error = %v", err)
	}
	defer registry.Close()
	records, err := ListProjects(registry)
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	return records
}

func TestResolveProjectStoreConfig(t *testing.T) {
	globalDB := filepath.Join(t.TempDir(), "global", "memory.db")
	explicitRoot := filepath.Join(t.TempDir(), "x", "..", "store")
	cases := []struct {
		name     string
		override string
		mode     string
		root     string
		want     ProjectStoreConfig
		wantErr  string
	}{
		{name: "unset defaults to legacy", want: ProjectStoreConfig{Mode: ProjectModeLegacy}},
		{name: "explicit legacy", mode: "legacy", want: ProjectStoreConfig{Mode: ProjectModeLegacy}},
		{name: "disabled", mode: " Disabled ", want: ProjectStoreConfig{Mode: ProjectModeDisabled}},
		{name: "central default root derived from global db file", mode: "central", want: ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Join(filepath.Dir(globalDB), "memory-projects")}},
		{name: "central explicit root", mode: "central", root: explicitRoot, want: ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Clean(explicitRoot)}},
		{name: "override wins over env", override: "disabled", mode: "central", want: ProjectStoreConfig{Mode: ProjectModeDisabled}},
		{name: "override central uses env root", override: "central", mode: "legacy", root: explicitRoot, want: ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Clean(explicitRoot)}},
		{name: "unknown mode fails", mode: "centralized", wantErr: "invalid MEMORY_PROJECT_MODE"},
		{name: "unknown override fails", override: "off", wantErr: "invalid -project-mode"},
		{name: "relative root fails", mode: "central", root: "relative/store", wantErr: "must be an absolute path"},
		{name: "root without central fails", mode: "legacy", root: explicitRoot, wantErr: "only valid with central mode"},
		{name: "root with disabled fails", override: "disabled", root: explicitRoot, wantErr: "only valid with central mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(projectModeEnv, tc.mode)
			t.Setenv(projectsRootEnv, tc.root)
			got, err := ResolveProjectStoreConfig(tc.override, globalDB)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("config = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultProjectsRootIsUniquePerGlobalDB(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "data")
	ops := DefaultProjectsRoot(filepath.Join(dir, "memory.db"))
	private := DefaultProjectsRoot(filepath.Join(dir, "private.db"))
	if ops == private {
		t.Fatalf("two global DBs in one directory share root %s", ops)
	}
	if want := filepath.Join(dir, "memory-projects"); ops != want {
		t.Fatalf("DefaultProjectsRoot = %s, want %s", ops, want)
	}
	if got := DefaultProjectsRoot(filepath.Join(dir, "noext")); got != filepath.Join(dir, "noext-projects") {
		t.Fatalf("DefaultProjectsRoot(no extension) = %s", got)
	}
}

func TestDisabledProjectModeRejectsProjectScope(t *testing.T) {
	useProjectStore(t, ProjectStoreConfig{Mode: ProjectModeDisabled})
	project := t.TempDir()

	_, _, err := HandleToolWithMetrics(nil, "remember", ToolArgs{Entity: "e", Observation: "o", Project: project})
	if !errors.Is(err, ErrProjectScopeDisabled) {
		t.Fatalf("remember(project) error = %v, want ErrProjectScopeDisabled", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled mode touched project .memory: stat err = %v", err)
	}

	globalDB, err := InitDB(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("InitDB(global) error = %v", err)
	}
	defer globalDB.Close()
	if _, _, err := HandleToolWithMetrics(globalDB, "remember", ToolArgs{Entity: "e", Observation: "global is fine"}); err != nil {
		t.Fatalf("remember(global) in disabled mode error = %v", err)
	}
}

func TestCentralModeStoresProjectMemoryOutsideProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}
	root := useCentralStore(t)
	project := t.TempDir()

	rememberInProject(t, project, "Widget", "lives in the central store")
	if got := projectObservationCount(t, project, "Widget"); got != 1 {
		t.Fatalf("observation count = %d, want 1", got)
	}

	if _, err := os.Stat(filepath.Join(project, ".memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("central mode created project .memory: stat err = %v", err)
	}
	records := registryRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1", len(records))
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].CanonicalPath != canonical || !records[0].Initialized {
		t.Fatalf("record = %+v, want initialized entry for %s", records[0], canonical)
	}
	if !strings.HasPrefix(records[0].ID, projectSlug(filepath.Base(canonical))+"-") {
		t.Fatalf("id %q does not carry the project slug", records[0].ID)
	}
	for path, want := range map[string]os.FileMode{
		root:                                0o700,
		filepath.Join(root, records[0].ID):  0o700,
		filepath.Join(root, registryDBName): 0o600,
		ProjectDBPath(root, records[0].ID):  0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestCentralModeSharesOneStoreAcrossPathSpellings(t *testing.T) {
	root := useCentralStore(t)
	base := t.TempDir()
	project := filepath.Join(base, "repo")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "repo-link")
	if err := os.Symlink(project, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	rememberInProject(t, project, "Shared", "via real path")
	rememberInProject(t, project+string(filepath.Separator)+".", "Shared", "via dot suffix")
	rememberInProject(t, link, "Shared", "via symlink")

	if got := projectObservationCount(t, link, "Shared"); got != 3 {
		t.Fatalf("observation count = %d, want 3 in one store", got)
	}
	if records := registryRecords(t, root); len(records) != 1 {
		t.Fatalf("registry records = %d, want 1", len(records))
	}
}

func TestCentralModeRejectsMissingProjectDirectory(t *testing.T) {
	root := useCentralStore(t)
	missing := filepath.Join(t.TempDir(), "typo")

	_, _, err := HandleToolWithMetrics(nil, "remember", ToolArgs{Entity: "e", Observation: "o", Project: missing})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("remember(missing project) error = %v, want does-not-exist", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("central mode created the missing project directory")
	}
	if records := registryRecords(t, root); len(records) != 0 {
		t.Fatalf("registry records = %d, want 0", len(records))
	}
}

// seedLegacyProjectDB writes one observation into <project>/.memory/memory.db
// through legacy mode, then restores the central config passed in.
func seedLegacyProjectDB(t *testing.T, project string, restore ProjectStoreConfig) {
	t.Helper()
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureProjectStore(ProjectStoreConfig{Mode: ProjectModeLegacy}); err != nil {
		t.Fatal(err)
	}
	rememberInProject(t, project, "Legacy", "written before central mode")
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureProjectStore(restore); err != nil {
		t.Fatal(err)
	}
}

func TestCentralModeRefusesUnregisteredLegacyDBThenImportServesIt(t *testing.T) {
	root := useCentralStore(t)
	central := CurrentProjectStore()
	project := t.TempDir()
	seedLegacyProjectDB(t, project, central)
	legacyPath := LegacyProjectDBPath(project)
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = HandleToolWithMetrics(nil, "recall_entity", ToolArgs{Entity: "Legacy", Project: project})
	if err == nil || !strings.Contains(err.Error(), "workmem project import") {
		t.Fatalf("recall_entity with unregistered legacy DB error = %v, want import hint", err)
	}
	if records := registryRecords(t, root); len(records) != 0 {
		t.Fatalf("refused call registered the project: %+v", records)
	}

	record, err := ImportProject(root, legacyPath, project)
	if err != nil {
		t.Fatalf("ImportProject() error = %v", err)
	}
	if !record.Initialized {
		t.Fatalf("imported record not initialized: %+v", record)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("legacy source removed by import: %v", err)
	}
	if string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy source content modified by import")
	}

	// Registered + legacy still present: one authoritative file is enforced.
	_, _, err = HandleToolWithMetrics(nil, "remember", ToolArgs{Entity: "Legacy", Observation: "late legacy-era write", Project: project})
	if err == nil || !strings.Contains(err.Error(), "legacy memory DB is still present") {
		t.Fatalf("remember with coexisting legacy DB error = %v, want coexistence refusal", err)
	}
	if _, _, err := ResolveExistingProjectDB(project); err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("ResolveExistingProjectDB with coexisting legacy DB error = %v", err)
	}

	if err := os.Rename(filepath.Join(project, ".memory"), filepath.Join(t.TempDir(), "archived-memory")); err != nil {
		t.Fatal(err)
	}
	if got := projectObservationCount(t, project, "Legacy"); got != 1 {
		t.Fatalf("imported observation count = %d, want 1", got)
	}
	rememberInProject(t, project, "Legacy", "written after import")
	if got := projectObservationCount(t, project, "Legacy"); got != 2 {
		t.Fatalf("observation count after write = %d, want 2", got)
	}

	if _, err := ImportProject(root, filepath.Join(root, "..", "nope.db"), project); err == nil {
		t.Fatalf("import of missing source succeeded")
	}
	other := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(other, legacyBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProject(root, other, project); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("second import error = %v, want already registered", err)
	}
}

func TestImportProjectRejectsSourceInsideStoreAndCleansUpOnFailure(t *testing.T) {
	root := useCentralStore(t)
	first := t.TempDir()
	rememberInProject(t, first, "A", "a")
	records := registryRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1", len(records))
	}
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}

	_, err := ImportProject(root, ProjectDBPath(root, records[0].ID), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "inside the central project store") {
		t.Fatalf("import from inside root error = %v", err)
	}

	notSQLite := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(notSQLite, []byte("definitely not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportProject(root, notSQLite, t.TempDir()); err == nil {
		t.Fatalf("import of non-SQLite source succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) != 1 || dirs[0] != records[0].ID {
		t.Fatalf("store dirs after failed imports = %v, want only %s", dirs, records[0].ID)
	}
}

func TestCentralModeFailsClosedWhenInitializedStoreIsMissing(t *testing.T) {
	root := useCentralStore(t)
	project := t.TempDir()
	rememberInProject(t, project, "Gone", "soon deleted")
	records := registryRecords(t, root)
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, records[0].ID)); err != nil {
		t.Fatal(err)
	}

	_, _, err := HandleToolWithMetrics(nil, "recall_entity", ToolArgs{Entity: "Gone", Project: project})
	if err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("recall_entity with missing store error = %v, want missing", err)
	}
	if _, err := os.Stat(ProjectDBPath(root, records[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing store was silently recreated")
	}
}

func TestMoveProjectKeepsMemoryAcrossDirectoryRename(t *testing.T) {
	root := useCentralStore(t)
	base := t.TempDir()
	oldPath := filepath.Join(base, "old-name")
	newPath := filepath.Join(base, "new-name")
	if err := os.Mkdir(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	rememberInProject(t, oldPath, "Moved", "before rename")
	canonicalOld, err := filepath.EvalSymlinks(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	// No cache reset: the running process must honor the move at once.
	result, err := MoveProject(root, canonicalOld, newPath, false)
	if err != nil {
		t.Fatalf("MoveProject() error = %v", err)
	}
	if got := projectObservationCount(t, newPath, "Moved"); got != 1 {
		t.Fatalf("observation count after move = %d, want 1", got)
	}
	records := registryRecords(t, root)
	if len(records) != 1 || records[0].ID != result.Record.ID {
		t.Fatalf("records after move = %+v, want only %s", records, result.Record.ID)
	}

	other := t.TempDir()
	rememberInProject(t, other, "Other", "x")
	if _, err := MoveProject(root, newPath, other, false); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("move onto registered path error = %v", err)
	}
	if _, err := MoveProject(root, newPath, other, true); err == nil || !strings.Contains(err.Error(), "holds memory") {
		t.Fatalf("replace-empty onto non-empty store error = %v", err)
	}
	if _, err := MoveProject(root, filepath.Join(base, "never-existed"), t.TempDir(), false); !errors.Is(err, ErrProjectNotRegistered) {
		t.Fatalf("move of unknown path error = %v, want ErrProjectNotRegistered", err)
	}
}

func TestRunningProcessHonorsMoveAndDoesNotLeakIntoRecreatedPath(t *testing.T) {
	useCentralStore(t)
	base := t.TempDir()
	app := filepath.Join(base, "app")
	archived := filepath.Join(base, "app-v1")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	rememberInProject(t, app, "Secret", "belongs to the first app")
	canonicalApp, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(app, archived); err != nil {
		t.Fatal(err)
	}
	if _, err := MoveProject(CurrentProjectStore().Root, canonicalApp, archived, false); err != nil {
		t.Fatalf("MoveProject() error = %v", err)
	}
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := projectObservationCount(t, app, "Secret"); got != 0 {
		t.Fatalf("recreated path sees %d observation(s) of the moved project", got)
	}
	rememberInProject(t, app, "Fresh", "belongs to the new app")
	if got := projectObservationCount(t, archived, "Fresh"); got != 0 {
		t.Fatalf("write for the new path landed in the moved store")
	}
	if got := projectObservationCount(t, archived, "Secret"); got != 1 {
		t.Fatalf("moved store observation count = %d, want 1", got)
	}
}

func TestMoveProjectReplaceEmptyArchivesAutoCreatedStore(t *testing.T) {
	root := useCentralStore(t)
	base := t.TempDir()
	oldPath := filepath.Join(base, "before")
	newPath := filepath.Join(base, "after")
	if err := os.Mkdir(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	rememberInProject(t, oldPath, "Kept", "real memory")
	canonicalOld, err := filepath.EvalSymlinks(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	// A client uses the new path before `project move`: an empty store appears.
	if got := projectObservationCount(t, newPath, "Kept"); got != 0 {
		t.Fatalf("new path unexpectedly sees old memory")
	}

	if _, err := MoveProject(root, canonicalOld, newPath, false); err == nil || !strings.Contains(err.Error(), "-replace-empty") {
		t.Fatalf("move onto auto-created store error = %v, want -replace-empty hint", err)
	}
	result, err := MoveProject(root, canonicalOld, newPath, true)
	if err != nil {
		t.Fatalf("MoveProject(replaceEmpty) error = %v", err)
	}
	if result.DiscardedID == "" {
		t.Fatalf("no store reported as discarded")
	}
	if got := projectObservationCount(t, newPath, "Kept"); got != 1 {
		t.Fatalf("observation count after replace-empty move = %d, want 1", got)
	}
	archived, err := filepath.Glob(filepath.Join(root, discardedStoresDir, result.DiscardedID+"-*"))
	if err != nil || len(archived) != 1 {
		t.Fatalf("discarded store not archived: %v %v", archived, err)
	}
	if records := registryRecords(t, root); len(records) != 1 {
		t.Fatalf("registry records = %d, want 1", len(records))
	}
}

func TestMoveProjectFromCompatibilitySymlink(t *testing.T) {
	root := useCentralStore(t)
	base := t.TempDir()
	oldPath := filepath.Join(base, "old")
	newPath := filepath.Join(base, "new")
	if err := os.Mkdir(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	rememberInProject(t, oldPath, "Linked", "x")
	canonicalOld, err := filepath.EvalSymlinks(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newPath, oldPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result, err := MoveProject(root, canonicalOld, newPath, false)
	if err != nil {
		t.Fatalf("MoveProject(old is now a symlink to new) error = %v", err)
	}
	canonicalNew, _ := filepath.EvalSymlinks(newPath)
	if result.Record.CanonicalPath != canonicalNew {
		t.Fatalf("moved to %s, want %s", result.Record.CanonicalPath, canonicalNew)
	}
	if got := projectObservationCount(t, oldPath, "Linked"); got != 1 {
		t.Fatalf("symlinked old path does not reach the moved store")
	}
}

func TestMissingRegistryBesideStoresFailsClosed(t *testing.T) {
	root := useCentralStore(t)
	project := t.TempDir()
	rememberInProject(t, project, "E", "o")
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(root, registryDBName+suffix))
	}

	_, _, err := HandleToolWithMetrics(nil, "recall_entity", ToolArgs{Entity: "E", Project: project})
	if err == nil || !strings.Contains(err.Error(), "restore registry.db") {
		t.Fatalf("acquire with missing registry error = %v, want fail-closed", err)
	}
	if _, err := os.Stat(filepath.Join(root, registryDBName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a fresh registry was created beside existing stores")
	}
}

func TestTamperedRegistryIDIsRejected(t *testing.T) {
	root := useCentralStore(t)
	project := t.TempDir()
	rememberInProject(t, project, "E", "o")
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenProjectRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Exec(`UPDATE projects SET id = '../../escape'`); err != nil {
		t.Fatal(err)
	}
	registry.Close()

	_, _, err = HandleToolWithMetrics(nil, "recall_entity", ToolArgs{Entity: "E", Project: project})
	if err == nil || !strings.Contains(err.Error(), "invalid project id") {
		t.Fatalf("acquire with tampered id error = %v", err)
	}
}

func TestInitDBIsNotBlockedByAnotherWriter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	writer, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO entities (name, entity_type) VALUES ('held', 'test')`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	reader, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB while another connection holds the write lock: %v", err)
	}
	reader.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("InitDB of an up-to-date DB waited %s on another writer", elapsed)
	}
}

func TestResolveExistingProjectDBNeverCreates(t *testing.T) {
	root := useCentralStore(t)
	project := t.TempDir()

	if _, _, err := ResolveExistingProjectDB(project); !errors.Is(err, ErrProjectRegistryMissing) {
		t.Fatalf("ResolveExistingProjectDB(no registry) error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup created the projects root: stat err = %v", err)
	}
	rememberInProject(t, project, "E", "o")
	unregistered := t.TempDir()
	if _, _, err := ResolveExistingProjectDB(unregistered); !errors.Is(err, ErrProjectNotRegistered) {
		t.Fatalf("ResolveExistingProjectDB(unregistered) error = %v", err)
	}
	if records := registryRecords(t, root); len(records) != 1 {
		t.Fatalf("lookup registered a project: %+v", records)
	}
	label, dbPath, err := ResolveExistingProjectDB(project)
	if err != nil {
		t.Fatalf("ResolveExistingProjectDB(registered) error = %v", err)
	}
	records := registryRecords(t, root)
	if dbPath != ProjectDBPath(root, records[0].ID) || label != ProjectScopeLabel(records[0].ID) {
		t.Fatalf("resolved (%s, %s), want (%s, %s)", label, dbPath, ProjectScopeLabel(records[0].ID), ProjectDBPath(root, records[0].ID))
	}

	useProjectStore(t, ProjectStoreConfig{Mode: ProjectModeDisabled})
	if _, _, err := ResolveExistingProjectDB(project); !errors.Is(err, ErrProjectScopeDisabled) {
		t.Fatalf("ResolveExistingProjectDB(disabled) error = %v", err)
	}
}

func TestConfigureProjectStoreRefusesSwitchWithCachedHandles(t *testing.T) {
	useCentralStore(t)
	project := t.TempDir()
	rememberInProject(t, project, "E", "o")

	if err := ConfigureProjectStore(ProjectStoreConfig{Mode: ProjectModeLegacy}); err == nil {
		t.Fatalf("switching mode with cached handles succeeded")
	}
	if err := ConfigureProjectStore(CurrentProjectStore()); err != nil {
		t.Fatalf("re-applying the same config error = %v", err)
	}
}

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"Business Plan2":          "business-plan2",
		"  --weird__name--  ":     "weird-name",
		"TREMIC":                  "tremic",
		"città":                   "citt",
		"...":                     "project",
		strings.Repeat("a", 100):  strings.Repeat("a", projectIDSlugMaxLen),
		"2026_100_ore_wintrade":   "2026-100-ore-wintrade",
		"offerte commerciali/x y": "offerte-commerciali-x-y",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

const registerHelperEnv = "WORKMEM_TEST_REGISTER_HELPER"

// TestRegisterHelperProcess is not a real test: it is the child process body
// for TestCentralRegistrationConvergesAcrossProcesses.
func TestRegisterHelperProcess(t *testing.T) {
	if os.Getenv(registerHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	root, project := os.Getenv("WORKMEM_TEST_ROOT"), os.Getenv("WORKMEM_TEST_PROJECT")
	if err := ConfigureProjectStore(ProjectStoreConfig{Mode: ProjectModeCentral, Root: root}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if _, _, err := HandleToolWithMetrics(nil, "remember", ToolArgs{Entity: "Race", Observation: os.Getenv("WORKMEM_TEST_OBS"), Project: project}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	if err := ResetProjectDBs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(5)
	}
	os.Exit(0)
}

func TestCentralRegistrationConvergesAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	root := filepath.Join(t.TempDir(), "projects")
	project := t.TempDir()
	const processes = 6

	var wg sync.WaitGroup
	errs := make([]error, processes)
	outputs := make([][]byte, processes)
	for i := 0; i < processes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRegisterHelperProcess$")
			cmd.Env = append(os.Environ(),
				registerHelperEnv+"=1",
				"WORKMEM_TEST_ROOT="+root,
				"WORKMEM_TEST_PROJECT="+project,
				fmt.Sprintf("WORKMEM_TEST_OBS=observation from process %d", i),
			)
			outputs[i], errs[i] = cmd.CombinedOutput()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("helper %d failed: %v\n%s", i, err, outputs[i])
		}
	}

	records := registryRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1: %+v", len(records), records)
	}
	useProjectStore(t, ProjectStoreConfig{Mode: ProjectModeCentral, Root: root})
	if got := projectObservationCount(t, project, "Race"); got != processes {
		t.Fatalf("observation count = %d, want %d in the single converged store", got, processes)
	}
}
