package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func newConflictTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "conflict.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func TestDetectEntityConflicts_FindsNearDuplicate(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityID, err := UpsertEntity(db, "API", "")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	priorID, err := AddObservation(db, entityID, "rate limit is 100 per minute", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	hints, err := DetectEntityConflicts(db, entityID, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) == 0 {
		t.Fatalf("expected at least one conflict hint on near-duplicate content, got 0")
	}
	if hints[0].ObservationID != priorID {
		t.Fatalf("hint[0].ObservationID = %d, want %d", hints[0].ObservationID, priorID)
	}
	if hints[0].Similarity < conflictHintMinScore {
		t.Fatalf("hint[0].Similarity = %f, want >= %f", hints[0].Similarity, conflictHintMinScore)
	}
	if hints[0].Snippet == "" {
		t.Fatalf("expected non-empty snippet")
	}
}

func TestDetectEntityConflicts_IgnoresUnrelatedContent(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityID, err := UpsertEntity(db, "API", "")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if _, err := AddObservation(db, entityID, "uses Postgres for storage", "user", 1.0); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	hints, err := DetectEntityConflicts(db, entityID, "ephemeral request tracing header")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints on unrelated content, got %d: %+v", len(hints), hints)
	}
}

func TestDetectEntityConflicts_RespectsEntityScope(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityA, err := UpsertEntity(db, "EntityA", "")
	if err != nil {
		t.Fatalf("UpsertEntity A: %v", err)
	}
	if _, err := AddObservation(db, entityA, "rate limit is 100 per minute", "user", 1.0); err != nil {
		t.Fatalf("AddObservation A: %v", err)
	}

	entityB, err := UpsertEntity(db, "EntityB", "")
	if err != nil {
		t.Fatalf("UpsertEntity B: %v", err)
	}

	hints, err := DetectEntityConflicts(db, entityB, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints on different entity (detection must be scoped), got %d: %+v", len(hints), hints)
	}
}

func TestDetectEntityConflicts_IgnoresTombstonedObservations(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityID, err := UpsertEntity(db, "API", "")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	priorID, err := AddObservation(db, entityID, "rate limit is 100 per minute", "user", 1.0)
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	deleted, err := ForgetObservation(db, priorID)
	if err != nil {
		t.Fatalf("ForgetObservation: %v", err)
	}
	if !deleted {
		t.Fatalf("ForgetObservation returned deleted=false")
	}

	hints, err := DetectEntityConflicts(db, entityID, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints after forget (tombstone discipline), got %d: %+v", len(hints), hints)
	}
}

func TestDetectEntityConflicts_CapsAtMaxResults(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityID, err := UpsertEntity(db, "API", "")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	for i := range 5 {
		if _, err := AddObservation(db, entityID, "rate limit is 100 per minute", "user", 1.0); err != nil {
			t.Fatalf("AddObservation[%d]: %v", i, err)
		}
	}

	hints, err := DetectEntityConflicts(db, entityID, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) > conflictHintMaxResults {
		t.Fatalf("expected at most %d hints, got %d", conflictHintMaxResults, len(hints))
	}
	if len(hints) == 0 {
		t.Fatalf("expected at least 1 hint against 5 near-duplicates, got 0")
	}
}

func TestDetectEntityConflicts_EmptyInputsShortCircuit(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	entityID, err := UpsertEntity(db, "API", "")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if _, err := AddObservation(db, entityID, "rate limit is 100 per minute", "user", 1.0); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	hints, err := DetectEntityConflicts(db, entityID, "")
	if err != nil {
		t.Fatalf("empty content: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("empty content should yield 0 hints, got %d", len(hints))
	}

	hints, err = DetectEntityConflicts(db, entityID, "   \t\n  ")
	if err != nil {
		t.Fatalf("whitespace content: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("whitespace-only content should yield 0 hints, got %d", len(hints))
	}

	hints, err = DetectEntityConflicts(db, 0, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("zero entityID: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("zero entityID should yield 0 hints, got %d", len(hints))
	}

	hints, err = DetectEntityConflicts(db, -1, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("negative entityID: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("negative entityID should yield 0 hints, got %d", len(hints))
	}
}
