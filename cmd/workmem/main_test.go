package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestReconcileApplyAndRollbackCLI(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "memory.db")
	db, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	entityID, err := store.UpsertEntity(db, "CLIApplyRollbackEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	sourceID := insertCLIRawObservation(t, db, entityID, "cli apply rollback duplicate", time.Now().Add(-2*time.Hour))
	targetID := insertCLIRawObservation(t, db, entityID, "cli apply rollback duplicate", time.Now().Add(-1*time.Hour))
	if err := db.Close(); err != nil {
		t.Fatalf("Close(seed db) error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	applyCmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "--mode", "apply", "--db", dbPath)
	applyOutput, err := applyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile apply error = %v\noutput:\n%s", err, string(applyOutput))
	}
	runID := parseCLIReconcileRunID(t, string(applyOutput))
	checkDB, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB(check apply) error = %v", err)
	}
	assertCLIObservationSuperseded(t, checkDB, sourceID, targetID, runID)
	if err := checkDB.Close(); err != nil {
		t.Fatalf("Close(check apply db) error = %v", err)
	}

	rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rollbackCancel()
	rollbackCmd := exec.CommandContext(rollbackCtx, "go", "run", ".", "reconcile", "rollback", "--db", dbPath, strconv.FormatInt(runID, 10))
	rollbackOutput, err := rollbackCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile rollback error = %v\noutput:\n%s", err, string(rollbackOutput))
	}
	if !strings.Contains(string(rollbackOutput), "restored 1 supersession") {
		t.Fatalf("rollback stdout missing restoration summary:\n%s", string(rollbackOutput))
	}
	finalDB, err := store.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB(final) error = %v", err)
	}
	defer finalDB.Close()
	assertCLIObservationNotSuperseded(t, finalDB, sourceID)
}

func TestReconcileApplyAndRollbackCLISupportsProjectScope(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	projectDB, release, err := store.AcquireDB(nil, projectDir)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	entityID, err := store.UpsertEntity(projectDB, "ProjectApplyRollbackEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(project) error = %v", err)
	}
	sourceID := insertCLIRawObservation(t, projectDB, entityID, "project apply rollback duplicate", time.Now().Add(-2*time.Hour))
	targetID := insertCLIRawObservation(t, projectDB, entityID, "project apply rollback duplicate", time.Now().Add(-1*time.Hour))
	release()
	if err := store.ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	applyCmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "--mode", "apply", "--scope", "project="+projectDir)
	applyOutput, err := applyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile project apply error = %v\noutput:\n%s", err, string(applyOutput))
	}
	runID := parseCLIReconcileRunID(t, string(applyOutput))
	_, projectDBPath := store.ResolveProjectDBPath(projectDir, "")
	checkDB, err := store.OpenExistingDB(projectDBPath)
	if err != nil {
		t.Fatalf("OpenExistingDB(project check) error = %v", err)
	}
	assertCLIObservationSuperseded(t, checkDB, sourceID, targetID, runID)
	if err := checkDB.Close(); err != nil {
		t.Fatalf("Close(project check db) error = %v", err)
	}

	rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rollbackCancel()
	rollbackCmd := exec.CommandContext(rollbackCtx, "go", "run", ".", "reconcile", "rollback", "--scope", "project="+projectDir, strconv.FormatInt(runID, 10))
	rollbackOutput, err := rollbackCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile project rollback error = %v\noutput:\n%s", err, string(rollbackOutput))
	}
	if !strings.Contains(string(rollbackOutput), "restored 1 supersession") {
		t.Fatalf("project rollback stdout missing restoration summary:\n%s", string(rollbackOutput))
	}
	finalDB, err := store.OpenExistingDB(projectDBPath)
	if err != nil {
		t.Fatalf("OpenExistingDB(project final) error = %v", err)
	}
	defer finalDB.Close()
	assertCLIObservationNotSuperseded(t, finalDB, sourceID)
}

func TestReconcileSemanticCLIDefaultsToNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "semantic")
	cmd.Env = cleanCLIEmbeddingEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile semantic error = %v\noutput:\n%s", err, string(output))
	}
	if !strings.Contains(string(output), "provider=none") || !strings.Contains(string(output), "0 network call(s)") {
		t.Fatalf("semantic stdout missing safe default summary:\n%s", string(output))
	}
}

func TestReconcileSemanticCLIRejectsRemoteOpenAIWithoutOptIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "semantic",
		"--embedding-provider", "openai",
		"--embedding-base-url", "https://api.openai.example/v1",
		"--embedding-model", "text-embedding-3-large",
		"--embedding-dimensions", "3072",
	)
	cmd.Env = cleanCLIEmbeddingEnv()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go run reconcile semantic remote openai error = nil, want failure\noutput:\n%s", string(output))
	}
	if !strings.Contains(string(output), "--allow-remote-embeddings") {
		t.Fatalf("semantic stderr missing --allow-remote-embeddings failure:\n%s", string(output))
	}
}

func TestReconcileSemanticCLIIgnoresRemoteOptInEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "semantic")
	cmd.Env = append(cleanCLIEmbeddingEnv(),
		"WORKMEM_EMBEDDING_PROVIDER=openai",
		"WORKMEM_EMBEDDING_BASE_URL=https://api.openai.example/v1",
		"WORKMEM_EMBEDDING_MODEL=text-embedding-3-large",
		"WORKMEM_EMBEDDING_DIMENSIONS=3072",
		"WORKMEM_EMBEDDING_ALLOW_REMOTE=true",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go run reconcile semantic env remote opt-in error = nil, want failure\noutput:\n%s", string(output))
	}
	if !strings.Contains(string(output), "--allow-remote-embeddings") {
		t.Fatalf("semantic stderr missing --allow-remote-embeddings failure:\n%s", string(output))
	}
}

func TestReconcileSemanticCLIAcceptsExplicitRemoteOptInAndOverridesEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "semantic",
		"--embedding-provider", "openai",
		"--embedding-base-url", "https://api.openai.example/v1",
		"--embedding-model", "text-embedding-3-large",
		"--embedding-dimensions", "3072",
		"--allow-remote-embeddings",
	)
	cmd.Env = append(cleanCLIEmbeddingEnv(),
		"WORKMEM_EMBEDDING_PROVIDER=openai-compatible",
		"WORKMEM_EMBEDDING_BASE_URL=http://localhost:1235/v1",
		"WORKMEM_EMBEDDING_MODEL=env-model-should-be-overridden",
		"WORKMEM_EMBEDDING_DIMENSIONS=not-an-int",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run reconcile semantic explicit remote opt-in error = %v\noutput:\n%s", err, string(output))
	}
	stdout := string(output)
	if !strings.Contains(stdout, "provider=openai") || !strings.Contains(stdout, "model=text-embedding-3-large") || !strings.Contains(stdout, "dimensions=3072") {
		t.Fatalf("semantic stdout missing explicit remote config:\n%s", stdout)
	}
	if strings.Contains(stdout, "env-model-should-be-overridden") {
		t.Fatalf("semantic stdout used env model despite CLI override:\n%s", stdout)
	}
}

func TestReconcileSemanticCLIRejectsProposalMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "reconcile", "semantic", "--mode", "propose")
	cmd.Env = cleanCLIEmbeddingEnv()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go run reconcile semantic --mode propose error = nil, want failure\noutput:\n%s", string(output))
	}
	if !strings.Contains(string(output), "validation-only") {
		t.Fatalf("semantic stderr missing validation-only failure:\n%s", string(output))
	}
}

func TestOpenReconcileDBGlobalReadOnlyDoesNotCreateMissingDB(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "missing.db")
	_, _, _, err := openReconcileDB("global", dbPath, true)
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
	_, _, _, err := openReconcileDB("project="+projectDir, "", true)
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
	_, _, _, err := openReconcileDB("project="+projectDir, filepath.Join(t.TempDir(), "memory.db"), true)
	if err == nil {
		t.Fatalf("openReconcileDB(project with db path) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "--db is only valid") {
		t.Fatalf("openReconcileDB(project with db path) error = %v, want --db validation", err)
	}
}

func TestOpenReconcileDBProjectScopeCanonicalizesScopeLabel(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	projectDB, release, err := store.AcquireDB(nil, projectDir)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	_ = projectDB
	release()
	if err := store.ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() error = %v", err)
	}

	db, closeDB, scopeLabel, err := openReconcileDB("project="+projectDir+string(os.PathSeparator)+".", "", true)
	if err != nil {
		t.Fatalf("openReconcileDB(project variant) error = %v", err)
	}
	defer closeDB()
	if db == nil {
		t.Fatalf("openReconcileDB(project variant) returned nil db")
	}
	if scopeLabel != "project:"+filepath.Clean(projectDir) {
		t.Fatalf("scope label = %q, want %q", scopeLabel, "project:"+filepath.Clean(projectDir))
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

func insertCLIRawObservation(t *testing.T, db *sql.DB, entityID int64, content string, createdAt time.Time) int64 {
	t.Helper()
	var entityType sql.NullString
	if err := db.QueryRow(`SELECT entity_type FROM entities WHERE id = ?`, entityID).Scan(&entityType); err != nil {
		t.Fatalf("select entity type error = %v", err)
	}
	result, err := db.Exec(
		`INSERT INTO observations (entity_id, content, source, confidence, entity_type, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		entityID,
		content,
		"test",
		1.0,
		nullableCLIString(entityType),
		createdAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert raw observation error = %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("raw observation LastInsertId error = %v", err)
	}
	return id
}

func nullableCLIString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func parseCLIReconcileRunID(t *testing.T, output string) int64 {
	t.Helper()
	fields := strings.Fields(output)
	for i, field := range fields {
		if field == "run" && i+1 < len(fields) {
			runID, err := strconv.ParseInt(fields[i+1], 10, 64)
			if err == nil && runID > 0 {
				return runID
			}
		}
	}
	t.Fatalf("could not parse reconcile run id from output:\n%s", output)
	return 0
}

func assertCLIObservationSuperseded(t *testing.T, db *sql.DB, sourceID int64, targetID int64, runID int64) {
	t.Helper()
	var supersededBy sql.NullInt64
	var supersededByRun sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by, superseded_by_run FROM observations WHERE id = ?`, sourceID).Scan(&supersededBy, &supersededByRun); err != nil {
		t.Fatalf("select supersession fields error = %v", err)
	}
	if !supersededBy.Valid || supersededBy.Int64 != targetID {
		t.Fatalf("superseded_by = %v, want %d", supersededBy, targetID)
	}
	if !supersededByRun.Valid || supersededByRun.Int64 != runID {
		t.Fatalf("superseded_by_run = %v, want %d", supersededByRun, runID)
	}
}

func assertCLIObservationNotSuperseded(t *testing.T, db *sql.DB, observationID int64) {
	t.Helper()
	var supersededBy sql.NullInt64
	var supersededByRun sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by, superseded_by_run FROM observations WHERE id = ?`, observationID).Scan(&supersededBy, &supersededByRun); err != nil {
		t.Fatalf("select supersession fields error = %v", err)
	}
	if supersededBy.Valid || supersededByRun.Valid {
		t.Fatalf("supersession fields = (%v, %v), want NULL", supersededBy, supersededByRun)
	}
}

func cleanCLIEmbeddingEnv() []string {
	env := os.Environ()
	cleaned := make([]string, 0, len(env)+5)
	for _, entry := range env {
		if strings.HasPrefix(entry, "WORKMEM_EMBEDDING_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
