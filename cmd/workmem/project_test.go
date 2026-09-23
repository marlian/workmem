package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd.Env = append(append(os.Environ(), "MEMORY_PROJECT_MODE=", "MEMORY_PROJECTS_ROOT=", "MEMORY_DB_PATH="), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// seedLegacyCLIProjectDB writes two duplicate observations into a legacy
// <project>/.memory/memory.db (legacy mode is the package default in tests).
func seedLegacyCLIProjectDB(t *testing.T, projectDir string) string {
	t.Helper()
	projectDB, release, err := store.AcquireDB(nil, projectDir)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	entityID, err := store.UpsertEntity(projectDB, "ImportedCLIEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(project) error = %v", err)
	}
	insertCLIRawObservation(t, projectDB, entityID, "imported duplicate content", time.Now().Add(-2*time.Hour))
	insertCLIRawObservation(t, projectDB, entityID, "imported duplicate content", time.Now().Add(-1*time.Hour))
	release()
	if err := store.ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}
	return store.LegacyProjectDBPath(projectDir)
}

func TestProjectCLIImportListReconcileAndMove(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDB := seedLegacyCLIProjectDB(t, projectDir)
	// Move the legacy DB out of the project, as a transfer package would hold it.
	staged := filepath.Join(base, "staged-memory.db")
	if err := os.Rename(legacyDB, staged); err != nil {
		t.Fatal(err)
	}
	globalDB := filepath.Join(base, "instance", "memory.db")
	central := []string{"MEMORY_PROJECT_MODE=central"}

	if output, err := runWorkmemCLI(t, nil, "project", "list", "--db", globalDB); err == nil || !strings.Contains(output, "requires MEMORY_PROJECT_MODE=central") {
		t.Fatalf("project list in legacy mode err=%v output:\n%s", err, output)
	}

	output, err := runWorkmemCLI(t, central, "project", "import", "--db", globalDB, "--from", staged, "--path", projectDir)
	if err != nil {
		t.Fatalf("project import error = %v\n%s", err, output)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("import removed its source: %v", err)
	}

	output, err = runWorkmemCLI(t, central, "project", "list", "--db", globalDB)
	if err != nil {
		t.Fatalf("project list error = %v\n%s", err, output)
	}
	if !strings.Contains(output, projectDir) || !strings.Contains(output, "project-") {
		t.Fatalf("project list missing imported project:\n%s", output)
	}

	// Reconcile must read the central store; there is no .memory left in the project.
	reportPath := filepath.Join(base, "report.md")
	output, err = runWorkmemCLI(t, append(central, "MEMORY_DB_PATH="+globalDB), "reconcile", "--scope", "project="+projectDir, "--output", reportPath)
	if err != nil {
		t.Fatalf("reconcile central project error = %v\n%s", err, output)
	}
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "imported duplicate content") {
		t.Fatalf("central reconcile report missing imported content:\n%s", content)
	}

	movedDir := filepath.Join(base, "renamed-project")
	if err := os.Rename(projectDir, movedDir); err != nil {
		t.Fatal(err)
	}
	output, err = runWorkmemCLI(t, central, "project", "move", "--db", globalDB, projectDir, movedDir)
	if err != nil {
		t.Fatalf("project move error = %v\n%s", err, output)
	}
	output, err = runWorkmemCLI(t, append(central, "MEMORY_DB_PATH="+globalDB), "reconcile", "--scope", "project="+movedDir, "--output", reportPath)
	if err != nil {
		t.Fatalf("reconcile after move error = %v\n%s", err, output)
	}
}

func TestReconcileCLIRejectsProjectScopeWhenDisabled(t *testing.T) {
	projectDir := t.TempDir()
	output, err := runWorkmemCLI(t, []string{"MEMORY_PROJECT_MODE=disabled"}, "reconcile", "--scope", "project="+projectDir, "--output", filepath.Join(t.TempDir(), "r.md"))
	if err == nil || !strings.Contains(output, "MEMORY_PROJECT_MODE=disabled") {
		t.Fatalf("reconcile in disabled mode err=%v output:\n%s", err, output)
	}
}
