package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workmem/internal/store"
)

func TestReconcileProposeCLIWritesReportWithoutAuditRows(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "memory.db")
	db, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	entityID, err := store.UpsertEntity(db, "CLIReconcileEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	insertCLIRawObservation(t, db, entityID, "cli duplicate content", time.Now().Add(-2*time.Hour))
	insertCLIRawObservation(t, db, entityID, "cli duplicate content", time.Now().Add(-1*time.Hour))
	if err := db.Close(); err != nil {
		t.Fatalf("Close(seed db) error = %v", err)
	}

	reportPath := filepath.Join(tmp, "reconcile-report.md")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "--db", dbPath, "--output", reportPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile error = %v\noutput:\n%s", err, string(output))
	}
	if !strings.Contains(string(output), "0 mutation(s)") {
		t.Fatalf("stdout missing mutation summary:\n%s", string(output))
	}
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(report) error = %v", err)
	}
	if !strings.Contains(string(content), "cli duplicate content") {
		t.Fatalf("report missing duplicate content:\n%s", string(content))
	}

	checkDB, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB(check) error = %v", err)
	}
	defer checkDB.Close()
	var reconcileRuns int
	if err := checkDB.QueryRow(`SELECT COUNT(*) FROM reconcile_runs`).Scan(&reconcileRuns); err != nil {
		t.Fatalf("count reconcile_runs error = %v", err)
	}
	if reconcileRuns != 0 {
		t.Fatalf("reconcile_runs = %d, want 0 for propose", reconcileRuns)
	}
	var reconcileDecisions int
	if err := checkDB.QueryRow(`SELECT COUNT(*) FROM reconcile_decisions`).Scan(&reconcileDecisions); err != nil {
		t.Fatalf("count reconcile_decisions error = %v", err)
	}
	if reconcileDecisions != 0 {
		t.Fatalf("reconcile_decisions = %d, want 0 for propose", reconcileDecisions)
	}
}

func TestReconcileProposeCLISupportsProjectScope(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	projectDB, release, err := store.AcquireDB(nil, projectDir)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	entityID, err := store.UpsertEntity(projectDB, "ProjectReconcileEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(project) error = %v", err)
	}
	insertCLIRawObservation(t, projectDB, entityID, "project duplicate content", time.Now().Add(-2*time.Hour))
	insertCLIRawObservation(t, projectDB, entityID, "project duplicate content", time.Now().Add(-1*time.Hour))
	release()
	if err := store.ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}

	reportPath := filepath.Join(t.TempDir(), "project-reconcile.md")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "--scope", "project="+projectDir, "--output", reportPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile project error = %v\noutput:\n%s", err, string(output))
	}
	content, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(project report) error = %v", err)
	}
	if !strings.Contains(string(content), "Scope: project:"+projectDir) {
		t.Fatalf("project report missing scope:\n%s", string(content))
	}
	if !strings.Contains(string(content), "project duplicate content") {
		t.Fatalf("project report missing duplicate content:\n%s", string(content))
	}
}

func TestOpenReconcileDBGlobalReadOnlyDoesNotCreateMissingDB(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "missing.db")
	_, _, _, err := openReconcileDB("global", dbPath)
	if err == nil {
		t.Fatalf("openReconcileDB(global missing) error = nil, want error")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing global db was created or stat failed: %v", statErr)
	}
}

func TestOpenReconcileDBProjectReadOnlyDoesNotCreateMemoryDir(t *testing.T) {
	t.Parallel()

	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	_, _, _, err := openReconcileDB("project="+projectDir, "")
	if err == nil {
		t.Fatalf("openReconcileDB(project missing) error = nil, want error")
	}
	memoryDir := filepath.Join(projectDir, ".memory")
	if _, statErr := os.Stat(memoryDir); !os.IsNotExist(statErr) {
		t.Fatalf("missing project .memory dir was created or stat failed: %v", statErr)
	}
}

func TestOpenReconcileDBRejectsDBPathForProjectScope(t *testing.T) {
	t.Parallel()

	projectDir := filepath.Join(t.TempDir(), "project")
	_, _, _, err := openReconcileDB("project="+projectDir, filepath.Join(t.TempDir(), "memory.db"))
	if err == nil {
		t.Fatalf("openReconcileDB(project with db path) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "--db is only valid") {
		t.Fatalf("openReconcileDB(project with db path) error = %v, want --db validation", err)
	}
}

func TestParseReconcileSinceSupportsDays(t *testing.T) {
	t.Parallel()

	duration, err := parseReconcileSince("30d")
	if err != nil {
		t.Fatalf("parseReconcileSince(30d) error = %v", err)
	}
	if duration != 30*24*time.Hour {
		t.Fatalf("duration = %s, want 720h", duration)
	}
	if _, err := parseReconcileSince("0d"); err == nil {
		t.Fatalf("parseReconcileSince(0d) error = nil, want error")
	}
}

func insertCLIRawObservation(t *testing.T, db *sql.DB, entityID int64, content string, createdAt time.Time) {
	t.Helper()
	var entityType sql.NullString
	if err := db.QueryRow(`SELECT entity_type FROM entities WHERE id = ?`, entityID).Scan(&entityType); err != nil {
		t.Fatalf("select entity type error = %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO observations (entity_id, content, source, confidence, entity_type, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		entityID,
		content,
		"test",
		1.0,
		nullableCLIString(entityType),
		createdAt.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		t.Fatalf("insert raw observation error = %v", err)
	}
}

func nullableCLIString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
