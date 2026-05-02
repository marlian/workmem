package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildReconcileProposeReportFindsExactDuplicatesWithoutMutation(t *testing.T) {
	db := newTestDB(t, "reconcile-propose.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "DuplicateEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(duplicate) error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "exact duplicate content", now.Add(-2*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "exact duplicate content", now.Add(-1*time.Hour))
	insertRawObservationForReconcileTest(t, db, entityID, "different content", now.Add(-30*time.Minute))

	otherEntityID, err := UpsertEntity(db, "OtherEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(other) error = %v", err)
	}
	insertRawObservationForReconcileTest(t, db, otherEntityID, "exact duplicate content", now.Add(-1*time.Hour))
	insertRawObservationForReconcileTest(t, db, otherEntityID, "other unique content", now.Add(-30*time.Minute))

	observationRowsBefore := countRowsForReconcileTest(t, db, "observations")
	reconcileRowsBefore := countRowsForReconcileTest(t, db, "reconcile_runs")
	decisionRowsBefore := countRowsForReconcileTest(t, db, "reconcile_decisions")

	report, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
	})
	if err != nil {
		t.Fatalf("BuildReconcileProposeReport() error = %v", err)
	}

	if countRowsForReconcileTest(t, db, "observations") != observationRowsBefore {
		t.Fatalf("propose report mutated observations table")
	}
	if countRowsForReconcileTest(t, db, "reconcile_runs") != reconcileRowsBefore {
		t.Fatalf("propose report inserted reconcile run rows")
	}
	if countRowsForReconcileTest(t, db, "reconcile_decisions") != decisionRowsBefore {
		t.Fatalf("propose report inserted reconcile decision rows")
	}
	if got := sumAccessCountForReconcileTest(t, db); got != 0 {
		t.Fatalf("propose report touched access_count = %d, want 0", got)
	}
	if len(report.DuplicateGroups) != 1 {
		t.Fatalf("DuplicateGroups len = %d, want 1: %#v", len(report.DuplicateGroups), report.DuplicateGroups)
	}
	group := report.DuplicateGroups[0]
	if group.EntityID != entityID {
		t.Fatalf("duplicate group entity = %d, want %d", group.EntityID, entityID)
	}
	if group.Target.ID != newerID {
		t.Fatalf("duplicate target = %d, want newest %d", group.Target.ID, newerID)
	}
	if group.Action != ReconcileActionProposed || group.Rationale != ReconcileRationaleExactDuplicateSameEntity {
		t.Fatalf("duplicate metadata = (%q, %q), want proposed exact duplicate", group.Action, group.Rationale)
	}
	if len(group.Sources) != 1 || group.Sources[0].ID != olderID {
		t.Fatalf("duplicate sources = %#v, want older %d", group.Sources, olderID)
	}
	if report.CandidatesProposed != 1 {
		t.Fatalf("CandidatesProposed = %d, want 1", report.CandidatesProposed)
	}
}

func TestBuildReconcileProposeReportIgnoresInactiveDuplicates(t *testing.T) {
	db := newTestDB(t, "reconcile-inactive.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "InactiveDuplicateEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	activeID := insertRawObservationForReconcileTest(t, db, entityID, "inactive duplicate content", now.Add(-1*time.Hour))
	deletedID := insertRawObservationForReconcileTest(t, db, entityID, "inactive duplicate content", now.Add(-2*time.Hour))
	if _, err := db.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, deletedID); err != nil {
		t.Fatalf("tombstone duplicate error = %v", err)
	}
	targetID := insertRawObservationForReconcileTest(t, db, entityID, "superseded duplicate content", now.Add(-1*time.Hour))
	sourceID := insertRawObservationForReconcileTest(t, db, entityID, "superseded duplicate content", now.Add(-2*time.Hour))
	markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")
	expiredEventID, err := CreateEvent(db, "Expired reconcile duplicate", "", "test", "", "")
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	expiredID := insertRawObservationForReconcileTest(t, db, entityID, "event duplicate content", now.Add(-2*time.Hour), expiredEventID)
	insertRawObservationForReconcileTest(t, db, entityID, "event duplicate content", now.Add(-1*time.Hour))
	if _, err := db.Exec(`UPDATE events SET expires_at = ? WHERE id = ?`, now.Add(-30*time.Minute).Format(sqliteTimestampLayout), expiredEventID); err != nil {
		t.Fatalf("expire event error = %v", err)
	}
	if activeID == 0 || expiredID == 0 {
		t.Fatalf("seed ids unexpectedly zero")
	}

	report, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
	})
	if err != nil {
		t.Fatalf("BuildReconcileProposeReport() error = %v", err)
	}
	if len(report.DuplicateGroups) != 0 {
		t.Fatalf("inactive duplicates produced groups: %#v", report.DuplicateGroups)
	}
}

func TestBuildReconcileProposeReportIncludesOlderSourcesForRecentEntities(t *testing.T) {
	db := newTestDB(t, "reconcile-since-boundary.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "SinceBoundaryEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "old source duplicate", now.Add(-45*24*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "old source duplicate", now.Add(-1*time.Hour))

	report, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
	})
	if err != nil {
		t.Fatalf("BuildReconcileProposeReport() error = %v", err)
	}
	if len(report.DuplicateGroups) != 1 {
		t.Fatalf("DuplicateGroups len = %d, want 1: %#v", len(report.DuplicateGroups), report.DuplicateGroups)
	}
	group := report.DuplicateGroups[0]
	if group.EntityID != entityID || group.Target.ID != newerID {
		t.Fatalf("group = %#v, want entity %d target %d", group, entityID, newerID)
	}
	if len(group.Sources) != 1 || group.Sources[0].ID != olderID {
		t.Fatalf("sources = %#v, want older source %d even outside since window", group.Sources, olderID)
	}
}

func TestBuildReconcileProposeReportRejectsTooManyEntities(t *testing.T) {
	db := newTestDB(t, "reconcile-entity-limit.db")
	_, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: ReconcileMaxEntitiesPerRunLimit + 1,
		Scope:             "global",
	})
	if err == nil {
		t.Fatalf("BuildReconcileProposeReport(over limit) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("BuildReconcileProposeReport(over limit) error = %v, want limit message", err)
	}
}

func TestBuildReconcileProposeReportUsesGeneratedAtAsEventExpiryAsOf(t *testing.T) {
	db := newTestDB(t, "reconcile-as-of-expiry.db")
	generatedAt := time.Now().UTC().Add(-2 * time.Hour)

	entityID, err := UpsertEntity(db, "AsOfExpiryEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	eventID, err := CreateEvent(db, "As-of expiry duplicate", "", "test", "", "")
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if _, err := db.Exec(`UPDATE events SET expires_at = ? WHERE id = ?`, generatedAt.Add(30*time.Minute).Format(sqliteTimestampLayout), eventID); err != nil {
		t.Fatalf("expire event error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "as-of duplicate content", generatedAt.Add(-10*time.Minute), eventID)
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "as-of duplicate content", generatedAt.Add(-5*time.Minute), eventID)

	report, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       generatedAt,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
	})
	if err != nil {
		t.Fatalf("BuildReconcileProposeReport() error = %v", err)
	}
	if len(report.DuplicateGroups) != 1 {
		t.Fatalf("DuplicateGroups len = %d, want 1 as of generated_at: %#v", len(report.DuplicateGroups), report.DuplicateGroups)
	}
	group := report.DuplicateGroups[0]
	if group.Target.ID != newerID || len(group.Sources) != 1 || group.Sources[0].ID != olderID {
		t.Fatalf("group = %#v, want target %d source %d", group, newerID, olderID)
	}
}

func TestApplyExactDuplicateReconcileSupersedesSourcesAndAudits(t *testing.T) {
	db := newTestDB(t, "reconcile-apply.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "ApplyDuplicateEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "apply duplicate content", now.Add(-2*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "apply duplicate content", now.Add(-1*time.Hour))

	result, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}
	if result.RunID == 0 || result.SupersessionsApplied != 1 || result.DecisionsRecorded != 1 || result.CandidatesProposed != 1 {
		t.Fatalf("ApplyExactDuplicateReconcile() = %#v, want one applied decision", result)
	}

	assertObservationSupersededByRun(t, db, olderID, newerID, result.RunID)
	assertExactDuplicateDecision(t, db, result.RunID, entityID, newerID, []int64{olderID}, false)
	byID, err := GetObservationsByIDs(db, []int64{olderID, newerID}, 12)
	if err != nil {
		t.Fatalf("GetObservationsByIDs() error = %v", err)
	}
	if byID.Total != 1 || len(byID.Observations) != 1 || byID.Observations[0].ID != newerID {
		t.Fatalf("GetObservationsByIDs() = %#v, want only target %d", byID, newerID)
	}
	graph, err := GetEntityGraph(db, "ApplyDuplicateEntity", 12)
	if err != nil {
		t.Fatalf("GetEntityGraph() error = %v", err)
	}
	if graph == nil || len(graph.Observations) != 1 || graph.Observations[0].ID != newerID {
		t.Fatalf("GetEntityGraph() = %#v, want only target %d", graph, newerID)
	}
	report, err := BuildReconcileProposeReport(db, ReconcileProposeOptions{
		GeneratedAt:       now.Add(time.Minute),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
	})
	if err != nil {
		t.Fatalf("BuildReconcileProposeReport(after apply) error = %v", err)
	}
	if len(report.DuplicateGroups) != 0 {
		t.Fatalf("propose after apply found duplicates: %#v", report.DuplicateGroups)
	}
}

func TestRollbackReconcileRunRestoresSourcesAndMarksDecisions(t *testing.T) {
	db := newTestDB(t, "reconcile-rollback.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "RollbackDuplicateEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "rollback duplicate content", now.Add(-2*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "rollback duplicate content", now.Add(-1*time.Hour))
	applyResult, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}

	rollbackResult, err := RollbackReconcileRun(db, ReconcileRollbackOptions{
		RunID:         applyResult.RunID,
		TriggerSource: "test",
	})
	if err != nil {
		t.Fatalf("RollbackReconcileRun() error = %v", err)
	}
	if rollbackResult.RunID == 0 || rollbackResult.RolledBackRunID != applyResult.RunID || rollbackResult.DecisionsReverted != 1 || rollbackResult.SupersessionsRestored != 1 {
		t.Fatalf("RollbackReconcileRun() = %#v, want one restored decision", rollbackResult)
	}
	assertObservationNotSuperseded(t, db, olderID)
	assertExactDuplicateDecision(t, db, applyResult.RunID, entityID, newerID, []int64{olderID}, true)
	byID, err := GetObservationsByIDs(db, []int64{olderID, newerID}, 12)
	if err != nil {
		t.Fatalf("GetObservationsByIDs() error = %v", err)
	}
	if byID.Total != 2 || len(byID.Observations) != 2 {
		t.Fatalf("GetObservationsByIDs() = %#v, want both observations visible", byID)
	}
	var rollbackMode string
	if err := db.QueryRow(`SELECT mode FROM reconcile_runs WHERE id = ?`, rollbackResult.RunID).Scan(&rollbackMode); err != nil {
		t.Fatalf("select rollback run mode error = %v", err)
	}
	if rollbackMode != "rollback" {
		t.Fatalf("rollback run mode = %q, want rollback", rollbackMode)
	}
}

func TestRollbackReconcileRunRejectsChangedStateWithoutPartialAudit(t *testing.T) {
	db := newTestDB(t, "reconcile-rollback-stale.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "RollbackStaleEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "stale rollback duplicate", now.Add(-2*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "stale rollback duplicate", now.Add(-1*time.Hour))
	applyResult, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}
	if _, err := db.Exec(`UPDATE observations SET deleted_at = ? WHERE id = ?`, now.Add(30*time.Second).Format(sqliteTimestampLayout), olderID); err != nil {
		t.Fatalf("tombstone applied source error = %v", err)
	}
	runsBefore := countRowsForReconcileTest(t, db, "reconcile_runs")
	decisionsBefore := countRowsForReconcileTest(t, db, "reconcile_decisions")
	_, err = RollbackReconcileRun(db, ReconcileRollbackOptions{
		RunID:         applyResult.RunID,
		TriggerSource: "test",
	})
	if err == nil {
		t.Fatalf("RollbackReconcileRun(stale source) error = nil, want error")
	}
	if countRowsForReconcileTest(t, db, "reconcile_runs") != runsBefore {
		t.Fatalf("failed rollback inserted a reconcile run")
	}
	if countRowsForReconcileTest(t, db, "reconcile_decisions") != decisionsBefore {
		t.Fatalf("failed rollback mutated decisions")
	}
	assertObservationSupersededByRun(t, db, olderID, newerID, applyResult.RunID)
}

func TestRollbackReconcileRunRejectsTamperedDecisionMetadata(t *testing.T) {
	db := newTestDB(t, "reconcile-rollback-tamper.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "RollbackTamperEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	olderID := insertRawObservationForReconcileTest(t, db, entityID, "tamper rollback duplicate", now.Add(-2*time.Hour))
	newerID := insertRawObservationForReconcileTest(t, db, entityID, "tamper rollback duplicate", now.Add(-1*time.Hour))
	applyResult, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}
	if _, err := db.Exec(`UPDATE reconcile_decisions SET rationale = ? WHERE run_id = ?`, "tampered", applyResult.RunID); err != nil {
		t.Fatalf("tamper decision rationale error = %v", err)
	}
	runsBefore := countRowsForReconcileTest(t, db, "reconcile_runs")
	_, err = RollbackReconcileRun(db, ReconcileRollbackOptions{
		RunID:         applyResult.RunID,
		TriggerSource: "test",
	})
	if err == nil {
		t.Fatalf("RollbackReconcileRun(tampered decision) error = nil, want error")
	}
	if countRowsForReconcileTest(t, db, "reconcile_runs") != runsBefore {
		t.Fatalf("failed tampered rollback inserted a reconcile run")
	}
	assertObservationSupersededByRun(t, db, olderID, newerID, applyResult.RunID)
}

func TestRollbackReconcileRunRejectsTamperedSourceListOmission(t *testing.T) {
	db := newTestDB(t, "reconcile-rollback-source-omission.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "RollbackSourceOmissionEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	oldestID := insertRawObservationForReconcileTest(t, db, entityID, "source omission duplicate", now.Add(-3*time.Hour))
	middleID := insertRawObservationForReconcileTest(t, db, entityID, "source omission duplicate", now.Add(-2*time.Hour))
	newestID := insertRawObservationForReconcileTest(t, db, entityID, "source omission duplicate", now.Add(-1*time.Hour))
	applyResult, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}
	if applyResult.SupersessionsApplied != 2 {
		t.Fatalf("SupersessionsApplied = %d, want 2", applyResult.SupersessionsApplied)
	}
	if _, err := db.Exec(`UPDATE observations SET deleted_at = ? WHERE id = ?`, now.Add(30*time.Second).Format(sqliteTimestampLayout), oldestID); err != nil {
		t.Fatalf("tombstone omitted source error = %v", err)
	}
	encodedOmission, err := json.Marshal([]int64{middleID})
	if err != nil {
		t.Fatalf("Marshal(omission) error = %v", err)
	}
	if _, err := db.Exec(`UPDATE reconcile_decisions SET source_obs_ids = ? WHERE run_id = ?`, string(encodedOmission), applyResult.RunID); err != nil {
		t.Fatalf("tamper source_obs_ids error = %v", err)
	}
	runsBefore := countRowsForReconcileTest(t, db, "reconcile_runs")
	_, err = RollbackReconcileRun(db, ReconcileRollbackOptions{
		RunID:         applyResult.RunID,
		TriggerSource: "test",
	})
	if err == nil {
		t.Fatalf("RollbackReconcileRun(tampered source list) error = nil, want error")
	}
	if countRowsForReconcileTest(t, db, "reconcile_runs") != runsBefore {
		t.Fatalf("failed source-list rollback inserted a reconcile run")
	}
	assertObservationSupersededByRun(t, db, oldestID, newestID, applyResult.RunID)
	assertObservationSupersededByRun(t, db, middleID, newestID, applyResult.RunID)
}

func TestRollbackReconcileRunRejectsPartiallyRevertedRun(t *testing.T) {
	db := newTestDB(t, "reconcile-rollback-partial.db")
	now := time.Now().UTC()

	entityID, err := UpsertEntity(db, "RollbackPartialEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	firstSourceID := insertRawObservationForReconcileTest(t, db, entityID, "partial rollback first", now.Add(-2*time.Hour))
	firstTargetID := insertRawObservationForReconcileTest(t, db, entityID, "partial rollback first", now.Add(-1*time.Hour))
	secondSourceID := insertRawObservationForReconcileTest(t, db, entityID, "partial rollback second", now.Add(-2*time.Hour))
	secondTargetID := insertRawObservationForReconcileTest(t, db, entityID, "partial rollback second", now.Add(-1*time.Hour))
	applyResult, err := ApplyExactDuplicateReconcile(db, ReconcileApplyOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		TriggerSource:     "test",
	})
	if err != nil {
		t.Fatalf("ApplyExactDuplicateReconcile() error = %v", err)
	}
	if applyResult.DecisionsRecorded != 2 || applyResult.SupersessionsApplied != 2 {
		t.Fatalf("ApplyExactDuplicateReconcile() = %#v, want two decisions", applyResult)
	}
	var firstDecisionID int64
	if err := db.QueryRow(`SELECT id FROM reconcile_decisions WHERE run_id = ? ORDER BY id LIMIT 1`, applyResult.RunID).Scan(&firstDecisionID); err != nil {
		t.Fatalf("select first decision error = %v", err)
	}
	if _, err := db.Exec(`UPDATE reconcile_decisions SET reverted_at = ?, reverted_by_run = ? WHERE id = ?`, now.Add(30*time.Second).Format(sqliteTimestampLayout), applyResult.RunID, firstDecisionID); err != nil {
		t.Fatalf("mark decision partially reverted error = %v", err)
	}
	runsBefore := countRowsForReconcileTest(t, db, "reconcile_runs")
	_, err = RollbackReconcileRun(db, ReconcileRollbackOptions{
		RunID:         applyResult.RunID,
		TriggerSource: "test",
	})
	if err == nil {
		t.Fatalf("RollbackReconcileRun(partial reverted run) error = nil, want error")
	}
	if countRowsForReconcileTest(t, db, "reconcile_runs") != runsBefore {
		t.Fatalf("failed partial rollback inserted a reconcile run")
	}
	assertObservationSupersededByRun(t, db, firstSourceID, firstTargetID, applyResult.RunID)
	assertObservationSupersededByRun(t, db, secondSourceID, secondTargetID, applyResult.RunID)
}

func insertRawObservationForReconcileTest(t *testing.T, db *sql.DB, entityID int64, content string, createdAt time.Time, eventID ...int64) int64 {
	t.Helper()
	var entityType sql.NullString
	if err := db.QueryRow(`SELECT entity_type FROM entities WHERE id = ?`, entityID).Scan(&entityType); err != nil {
		t.Fatalf("select entity type error = %v", err)
	}
	var eventValue any
	if len(eventID) > 0 && eventID[0] > 0 {
		eventValue = eventID[0]
	}
	result, err := db.Exec(
		`INSERT INTO observations (entity_id, content, source, confidence, event_id, entity_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entityID,
		content,
		"test",
		1.0,
		eventValue,
		nullableString(entityType.String, ""),
		createdAt.UTC().Format(sqliteTimestampLayout),
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

func assertObservationSupersededByRun(t *testing.T, db *sql.DB, sourceID int64, targetID int64, runID int64) {
	t.Helper()
	var supersededBy sql.NullInt64
	var supersededAt sql.NullString
	var reason sql.NullString
	var supersededByRun sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by, superseded_at, superseded_reason, superseded_by_run FROM observations WHERE id = ?`, sourceID).Scan(&supersededBy, &supersededAt, &reason, &supersededByRun); err != nil {
		t.Fatalf("select supersession fields error = %v", err)
	}
	if !supersededBy.Valid || supersededBy.Int64 != targetID {
		t.Fatalf("superseded_by = %v, want %d", supersededBy, targetID)
	}
	if !supersededAt.Valid || supersededAt.String == "" {
		t.Fatalf("superseded_at = %v, want timestamp", supersededAt)
	}
	if !reason.Valid || reason.String != ReconcileRationaleExactDuplicateSameEntity {
		t.Fatalf("superseded_reason = %v, want %q", reason, ReconcileRationaleExactDuplicateSameEntity)
	}
	if !supersededByRun.Valid || supersededByRun.Int64 != runID {
		t.Fatalf("superseded_by_run = %v, want %d", supersededByRun, runID)
	}
}

func assertObservationNotSuperseded(t *testing.T, db *sql.DB, observationID int64) {
	t.Helper()
	var supersededBy sql.NullInt64
	var supersededAt sql.NullString
	var reason sql.NullString
	var supersededByRun sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by, superseded_at, superseded_reason, superseded_by_run FROM observations WHERE id = ?`, observationID).Scan(&supersededBy, &supersededAt, &reason, &supersededByRun); err != nil {
		t.Fatalf("select supersession fields error = %v", err)
	}
	if supersededBy.Valid || supersededAt.Valid || reason.Valid || supersededByRun.Valid {
		t.Fatalf("observation %d supersession fields = (%v, %v, %v, %v), want all NULL", observationID, supersededBy, supersededAt, reason, supersededByRun)
	}
}

func assertExactDuplicateDecision(t *testing.T, db *sql.DB, runID int64, entityID int64, targetID int64, sourceIDs []int64, wantReverted bool) {
	t.Helper()
	var encodedSources string
	var kind string
	var decisionEntityID sql.NullInt64
	var decisionTargetID sql.NullInt64
	var similarity sql.NullFloat64
	var action string
	var rationale sql.NullString
	var revertedAt sql.NullString
	var revertedByRun sql.NullInt64
	if err := db.QueryRow(`
		SELECT kind, entity_id, source_obs_ids, target_obs_id, similarity, action, rationale, reverted_at, reverted_by_run
		FROM reconcile_decisions WHERE run_id = ?
	`, runID).Scan(&kind, &decisionEntityID, &encodedSources, &decisionTargetID, &similarity, &action, &rationale, &revertedAt, &revertedByRun); err != nil {
		t.Fatalf("select reconcile decision error = %v", err)
	}
	if kind != ReconcileDecisionKindExactDuplicate || action != ReconcileActionApplied {
		t.Fatalf("decision kind/action = (%q, %q), want exact_duplicate/applied", kind, action)
	}
	if !decisionEntityID.Valid || decisionEntityID.Int64 != entityID {
		t.Fatalf("decision entity_id = %v, want %d", decisionEntityID, entityID)
	}
	if !decisionTargetID.Valid || decisionTargetID.Int64 != targetID {
		t.Fatalf("decision target_obs_id = %v, want %d", decisionTargetID, targetID)
	}
	if !similarity.Valid || similarity.Float64 != 1.0 {
		t.Fatalf("decision similarity = %v, want 1.0", similarity)
	}
	if !rationale.Valid || rationale.String != ReconcileRationaleExactDuplicateSameEntity {
		t.Fatalf("decision rationale = %v, want %q", rationale, ReconcileRationaleExactDuplicateSameEntity)
	}
	var decodedSources []int64
	if err := json.Unmarshal([]byte(encodedSources), &decodedSources); err != nil {
		t.Fatalf("decode source_obs_ids %q error = %v", encodedSources, err)
	}
	if len(decodedSources) != len(sourceIDs) {
		t.Fatalf("source_obs_ids = %#v, want %#v", decodedSources, sourceIDs)
	}
	for i := range sourceIDs {
		if decodedSources[i] != sourceIDs[i] {
			t.Fatalf("source_obs_ids = %#v, want %#v", decodedSources, sourceIDs)
		}
	}
	if wantReverted {
		if !revertedAt.Valid || !revertedByRun.Valid {
			t.Fatalf("decision reverted fields = (%v, %v), want set", revertedAt, revertedByRun)
		}
	} else if revertedAt.Valid || revertedByRun.Valid {
		t.Fatalf("decision reverted fields = (%v, %v), want NULL", revertedAt, revertedByRun)
	}
}

func countRowsForReconcileTest(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s error = %v", table, err)
	}
	return count
}

func sumAccessCountForReconcileTest(t *testing.T, db *sql.DB) int {
	t.Helper()
	var sum sql.NullInt64
	if err := db.QueryRow(`SELECT SUM(access_count) FROM observations`).Scan(&sum); err != nil {
		t.Fatalf("sum access_count error = %v", err)
	}
	if !sum.Valid {
		return 0
	}
	return int(sum.Int64)
}
