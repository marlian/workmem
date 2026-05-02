package store

import (
	"database/sql"
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
