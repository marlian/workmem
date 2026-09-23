package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"workmem/internal/store"
)

// runWorkmemCLI runs `go run . <args>` with env overrides and returns
// combined output and error.
func runWorkmemCLI(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"run", "."}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// writeCentralEnvFile writes an instance .env the way a real central-mode
// deployment configures itself, and returns its path.
func writeCentralEnvFile(t *testing.T, globalDB string) string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "instance.env")
	content := "MEMORY_DB_PATH=" + globalDB + "\nMEMORY_PROJECT_MODE=central\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return envFile
}

// seedLegacyCLIProjectDB writes two duplicate observations into a legacy
// <project>/.memory/memory.db (legacy mode is the package default in tests)
// and returns the source/target observation ids.
func seedLegacyCLIProjectDB(t *testing.T, projectDir string, content string) (int64, int64) {
	t.Helper()
	projectDB, release, err := store.AcquireDB(nil, projectDir)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	entityID, err := store.UpsertEntity(projectDB, "ImportedCLIEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(project) error = %v", err)
	}
	sourceID := insertCLIRawObservation(t, projectDB, entityID, content, time.Now().Add(-2*time.Hour))
	targetID := insertCLIRawObservation(t, projectDB, entityID, content, time.Now().Add(-1*time.Hour))
	release()
	if err := store.ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}
	return sourceID, targetID
}

// stageLegacyProjectDB moves the legacy DB out of the project, as a transfer
// package would hold it, and returns the staged path.
func stageLegacyProjectDB(t *testing.T, projectDir string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "staged-memory.db")
	if err := os.Rename(store.LegacyProjectDBPath(projectDir), staged); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectDir, ".memory")); err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestProjectCLIImportListReconcileAndMove(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seedLegacyCLIProjectDB(t, projectDir, "imported duplicate content")
	staged := stageLegacyProjectDB(t, projectDir)
	envFile := writeCentralEnvFile(t, filepath.Join(base, "instance", "memory.db"))

	if output, err := runWorkmemCLI(t, nil, "project", "list"); err == nil || !strings.Contains(output, "requires MEMORY_PROJECT_MODE=central") {
		t.Fatalf("project list in legacy mode err=%v output:\n%s", err, output)
	}
	if output, err := runWorkmemCLI(t, nil, "project", "list", "-env-file", envFile); err == nil || !strings.Contains(output, "no project registry") {
		t.Fatalf("project list before any registration err=%v output:\n%s", err, output)
	}

	output, err := runWorkmemCLI(t, nil, "project", "import", "-env-file", envFile, "-from", staged, "-path", projectDir)
	if err != nil {
		t.Fatalf("project import error = %v\n%s", err, output)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("import removed its source: %v", err)
	}

	output, err = runWorkmemCLI(t, nil, "project", "list", "-env-file", envFile)
	if err != nil {
		t.Fatalf("project list error = %v\n%s", err, output)
	}
	// The registry lists canonical (symlink-resolved) paths, e.g. macOS /private/var.
	canonicalProject, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, canonicalProject) || !strings.Contains(output, "project-") || !strings.Contains(output, filepath.Join(base, "instance", "memory-projects")) {
		t.Fatalf("project list missing imported project or root:\n%s", output)
	}

	// Reconcile reads the central store through the same env-file.
	reportPath := filepath.Join(base, "report.md")
	output, err = runWorkmemCLI(t, nil, "reconcile", "-env-file", envFile, "--scope", "project="+projectDir, "--output", reportPath)
	if err != nil {
		t.Fatalf("reconcile central project error = %v\n%s", err, output)
	}
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "imported duplicate content") || !strings.Contains(string(content), "Scope: project:project-") {
		t.Fatalf("central reconcile report missing imported content or id scope:\n%s", content)
	}

	movedDir := filepath.Join(base, "renamed-project")
	if err := os.Rename(projectDir, movedDir); err != nil {
		t.Fatal(err)
	}
	output, err = runWorkmemCLI(t, nil, "project", "move", "-env-file", envFile, projectDir, movedDir)
	if err != nil {
		t.Fatalf("project move error = %v\n%s", err, output)
	}
	output, err = runWorkmemCLI(t, nil, "reconcile", "-env-file", envFile, "--scope", "project="+movedDir, "--output", reportPath)
	if err != nil {
		t.Fatalf("reconcile after move error = %v\n%s", err, output)
	}
}

func TestProjectCLIRollbackSurvivesProjectMove(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "before")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := seedLegacyCLIProjectDB(t, projectDir, "rollback across move")
	staged := stageLegacyProjectDB(t, projectDir)
	globalDB := filepath.Join(base, "instance", "memory.db")
	envFile := writeCentralEnvFile(t, globalDB)

	if output, err := runWorkmemCLI(t, nil, "project", "import", "-env-file", envFile, "-from", staged, "-path", projectDir); err != nil {
		t.Fatalf("project import error = %v\n%s", err, output)
	}
	output, err := runWorkmemCLI(t, nil, "reconcile", "-env-file", envFile, "--mode", "apply", "--scope", "project="+projectDir)
	if err != nil {
		t.Fatalf("reconcile apply error = %v\n%s", err, output)
	}
	runID := parseCLIReconcileRunID(t, output)

	movedDir := filepath.Join(base, "after")
	if err := os.Rename(projectDir, movedDir); err != nil {
		t.Fatal(err)
	}
	if output, err := runWorkmemCLI(t, nil, "project", "move", "-env-file", envFile, projectDir, movedDir); err != nil {
		t.Fatalf("project move error = %v\n%s", err, output)
	}
	output, err = runWorkmemCLI(t, nil, "reconcile", "rollback", "-env-file", envFile, "--scope", "project="+movedDir, strconv.FormatInt(runID, 10))
	if err != nil {
		t.Fatalf("rollback after move error = %v\n%s", err, output)
	}
	if !strings.Contains(output, "restored 1 supersession") {
		t.Fatalf("rollback after move did not restore:\n%s", output)
	}

	root := store.DefaultProjectsRoot(globalDB)
	registry, err := store.OpenProjectRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.ListProjects(registry)
	registry.Close()
	if err != nil || len(records) != 1 {
		t.Fatalf("registry records = %+v, err = %v", records, err)
	}
	checkDB, err := store.OpenExistingDB(store.ProjectDBPath(root, records[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	defer checkDB.Close()
	assertCLIObservationNotSuperseded(t, checkDB, sourceID)
}

func TestReconcileCLIRejectsProjectScopeWhenDisabled(t *testing.T) {
	projectDir := t.TempDir()
	output, err := runWorkmemCLI(t, []string{"MEMORY_PROJECT_MODE=disabled"}, "reconcile", "--scope", "project="+projectDir, "--output", filepath.Join(t.TempDir(), "r.md"))
	if err == nil || !strings.Contains(output, "MEMORY_PROJECT_MODE=disabled") {
		t.Fatalf("reconcile in disabled mode err=%v output:\n%s", err, output)
	}
}

func TestReconcileCLICentralLookupCreatesNothing(t *testing.T) {
	base := t.TempDir()
	globalDB := filepath.Join(base, "instance", "memory.db")
	envFile := writeCentralEnvFile(t, globalDB)
	output, err := runWorkmemCLI(t, nil, "reconcile", "-env-file", envFile, "--scope", "project="+t.TempDir(), "--output", filepath.Join(base, "r.md"))
	if err == nil || !strings.Contains(output, "no project registry") {
		t.Fatalf("reconcile on empty central store err=%v output:\n%s", err, output)
	}
	if _, err := os.Stat(store.DefaultProjectsRoot(globalDB)); !os.IsNotExist(err) {
		t.Fatalf("reconcile lookup created the projects root: stat err = %v", err)
	}
}

func TestServeRefusesUnsafeConfiguration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	cases := []struct {
		name    string
		env     []string
		args    []string
		wantErr string
	}{
		{name: "missing env-file", args: []string{"serve", "-db", dbPath, "-env-file", filepath.Join(t.TempDir(), "typo.env")}, wantErr: "env-file"},
		{name: "invalid mode", env: []string{"MEMORY_PROJECT_MODE=centralized"}, args: []string{"serve", "-db", dbPath}, wantErr: "invalid MEMORY_PROJECT_MODE"},
		{name: "invalid flag mode", args: []string{"serve", "-db", dbPath, "-project-mode", "off"}, wantErr: "invalid -project-mode"},
		{name: "root without central", env: []string{"MEMORY_PROJECTS_ROOT=" + t.TempDir()}, args: []string{"serve", "-db", dbPath}, wantErr: "only valid with central mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runWorkmemCLI(t, tc.env, tc.args...)
			if err == nil || !strings.Contains(output, tc.wantErr) {
				t.Fatalf("serve err=%v, want failure containing %q; output:\n%s", err, tc.wantErr, output)
			}
		})
	}
}

func TestCLIProjectPathResolvesRelativeToWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	got, err := cliProjectPath("repo")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "repo"); got != want {
		t.Fatalf("cliProjectPath(repo) = %s, want %s", got, want)
	}
	for _, keep := range []string{"~", "~/repo", filepath.Join(base, "abs")} {
		if got, _ := cliProjectPath(keep); got != keep {
			t.Fatalf("cliProjectPath(%s) = %s, want unchanged", keep, got)
		}
	}
}

func TestProjectCLIRollbackOfLegacyRunAfterImport(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := seedLegacyCLIProjectDB(t, projectDir, "applied while still legacy")

	// Apply in legacy mode: the run records the path-based scope label.
	output, err := runWorkmemCLI(t, nil, "reconcile", "--mode", "apply", "--scope", "project="+projectDir)
	if err != nil {
		t.Fatalf("legacy reconcile apply error = %v\n%s", err, output)
	}
	runID := parseCLIReconcileRunID(t, output)

	staged := stageLegacyProjectDB(t, projectDir)
	globalDB := filepath.Join(base, "instance", "memory.db")
	envFile := writeCentralEnvFile(t, globalDB)
	if output, err := runWorkmemCLI(t, nil, "project", "import", "-env-file", envFile, "-from", staged, "-path", projectDir); err != nil {
		t.Fatalf("project import error = %v\n%s", err, output)
	}

	output, err = runWorkmemCLI(t, nil, "reconcile", "rollback", "-env-file", envFile, "--scope", "project="+projectDir, strconv.FormatInt(runID, 10))
	if err != nil {
		t.Fatalf("rollback of legacy run after import error = %v\n%s", err, output)
	}
	if !strings.Contains(output, "restored 1 supersession") {
		t.Fatalf("rollback of legacy run did not restore:\n%s", output)
	}

	root := store.DefaultProjectsRoot(globalDB)
	registry, err := store.OpenProjectRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.ListProjects(registry)
	registry.Close()
	if err != nil || len(records) != 1 {
		t.Fatalf("registry records = %+v, err = %v", records, err)
	}
	checkDB, err := store.OpenExistingDB(store.ProjectDBPath(root, records[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	defer checkDB.Close()
	assertCLIObservationNotSuperseded(t, checkDB, sourceID)
}

func TestProjectCLIRefusesMissingEnvFileWithoutCreatingState(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source.db")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fallbackDB := filepath.Join(base, "fallback", "memory.db")
	missingEnv := filepath.Join(base, "typo.env")
	env := []string{"MEMORY_PROJECT_MODE=central", "MEMORY_DB_PATH=" + fallbackDB}

	for _, args := range [][]string{
		{"project", "import", "-env-file", missingEnv, "-from", source, "-path", projectDir},
		{"project", "move", "-env-file", missingEnv, projectDir, projectDir + "-new"},
		{"project", "list", "-env-file", missingEnv},
	} {
		output, err := runWorkmemCLI(t, env, args...)
		if err == nil || !strings.Contains(output, "env-file") {
			t.Fatalf("%v with missing env-file err=%v output:\n%s", args[:2], err, output)
		}
	}
	if _, err := os.Stat(store.DefaultProjectsRoot(fallbackDB)); !os.IsNotExist(err) {
		t.Fatalf("a projects root was created under the fallback global DB: stat err = %v", err)
	}
}
