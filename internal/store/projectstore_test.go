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

func TestProjectStoreConfigFromEnv(t *testing.T) {
	globalDB := filepath.Join(t.TempDir(), "global", "memory.db")
	cases := []struct {
		name    string
		mode    string
		root    string
		want    ProjectStoreConfig
		wantErr string
	}{
		{name: "unset defaults to legacy", want: ProjectStoreConfig{Mode: ProjectModeLegacy}},
		{name: "explicit legacy", mode: "legacy", want: ProjectStoreConfig{Mode: ProjectModeLegacy}},
		{name: "disabled", mode: " Disabled ", want: ProjectStoreConfig{Mode: ProjectModeDisabled}},
		{name: "central default root beside global db", mode: "central", want: ProjectStoreConfig{Mode: ProjectModeCentral, Root: filepath.Join(filepath.Dir(globalDB), "projects")}},
		{name: "central explicit root", mode: "central", root: filepath.Join(t.TempDir(), "x", "..", "store"), want: ProjectStoreConfig{Mode: ProjectModeCentral}},
		{name: "unknown mode fails", mode: "centralized", wantErr: "invalid MEMORY_PROJECT_MODE"},
		{name: "relative root fails", mode: "central", root: "relative/store", wantErr: "must be an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(projectModeEnv, tc.mode)
			t.Setenv(projectsRootEnv, tc.root)
			got, err := ProjectStoreConfigFromEnv(globalDB)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			want := tc.want
			if tc.root != "" {
				want.Root = filepath.Clean(tc.root)
			}
			if got != want {
				t.Fatalf("config = %+v, want %+v", got, want)
			}
		})
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
	if got := projectObservationCount(t, project, "Legacy"); got != 1 {
		t.Fatalf("imported observation count = %d, want 1", got)
	}
	rememberInProject(t, project, "Legacy", "written after import")
	if got := projectObservationCount(t, project, "Legacy"); got != 2 {
		t.Fatalf("observation count after write = %d, want 2", got)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("legacy source removed by import: %v", err)
	}
	if string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy source modified by import or later writes")
	}

	if _, err := ImportProject(root, legacyPath, project); err == nil || !strings.Contains(err.Error(), "already registered") {
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
	if err := ResetProjectDBs(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	registry, err := OpenProjectRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := MoveProject(registry, canonicalOld, newPath)
	registry.Close()
	if err != nil {
		t.Fatalf("MoveProject() error = %v", err)
	}
	if got := projectObservationCount(t, newPath, "Moved"); got != 1 {
		t.Fatalf("observation count after move = %d, want 1", got)
	}
	records := registryRecords(t, root)
	if len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("records after move = %+v, want only %s", records, record.ID)
	}

	other := t.TempDir()
	rememberInProject(t, other, "Other", "x")
	registry, err = OpenProjectRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err := MoveProject(registry, newPath, other); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("move onto registered path error = %v", err)
	}
	if _, err := MoveProject(registry, filepath.Join(base, "never-existed"), t.TempDir()); !errors.Is(err, ErrProjectNotRegistered) {
		t.Fatalf("move of unknown path error = %v, want ErrProjectNotRegistered", err)
	}
}

func TestResolveExistingProjectDBNeverCreates(t *testing.T) {
	root := useCentralStore(t)
	project := t.TempDir()

	if _, _, err := ResolveExistingProjectDB(project); !errors.Is(err, ErrProjectNotRegistered) {
		t.Fatalf("ResolveExistingProjectDB(unregistered) error = %v", err)
	}
	if records := registryRecords(t, root); len(records) != 0 {
		t.Fatalf("lookup registered a project: %+v", records)
	}
	rememberInProject(t, project, "E", "o")
	label, dbPath, err := ResolveExistingProjectDB(project)
	if err != nil {
		t.Fatalf("ResolveExistingProjectDB(registered) error = %v", err)
	}
	records := registryRecords(t, root)
	if dbPath != ProjectDBPath(root, records[0].ID) || label != records[0].CanonicalPath {
		t.Fatalf("resolved (%s, %s), want (%s, %s)", label, dbPath, records[0].CanonicalPath, ProjectDBPath(root, records[0].ID))
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
