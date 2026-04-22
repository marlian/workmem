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
	// Five DISTINCT near-duplicates: AddObservation dedupes by exact
	// `entity_id + content` match (see sqlite.go), so seeding five copies
	// of the same string would leave only one row in the DB and make the
	// cap assertion vacuous. Each string must be unique but lexically
	// similar enough to all score above the conflict threshold.
	priorContents := []string{
		"rate limit is 100 per minute",
		"rate limit is 150 per minute",
		"rate limit is 250 per minute",
		"rate limit is 300 per minute",
		"rate limit is 500 per minute",
	}
	seeded := make([]int64, 0, len(priorContents))
	for i, content := range priorContents {
		id, err := AddObservation(db, entityID, content, "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation[%d]: %v", i, err)
		}
		seeded = append(seeded, id)
	}
	// Prove the seed actually landed five distinct rows before the cap
	// assertion — otherwise the cap test degenerates to "any number ≤ 3
	// is fine" which is tautological.
	unique := map[int64]struct{}{}
	for _, id := range seeded {
		unique[id] = struct{}{}
	}
	if len(unique) != len(priorContents) {
		t.Fatalf("expected %d distinct seeded observation IDs, got %d (AddObservation dedup may have collapsed them)", len(priorContents), len(unique))
	}

	hints, err := DetectEntityConflicts(db, entityID, "rate limit is 200 per minute")
	if err != nil {
		t.Fatalf("DetectEntityConflicts: %v", err)
	}
	if len(hints) != conflictHintMaxResults {
		t.Fatalf("expected exactly %d hints when more than %d near-duplicates qualify, got %d", conflictHintMaxResults, conflictHintMaxResults, len(hints))
	}
}

func TestHandleTool_RememberSurfacesConflictsOnNearDuplicate(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	// Seed a prior observation via HandleTool so we exercise the same
	// path a client would take, not just the low-level primitives.
	first, err := HandleTool(db, "remember", ToolArgs{
		Entity:      "API",
		Observation: "rate limit is 100 per minute",
	})
	if err != nil {
		t.Fatalf("HandleTool(remember, seed) error = %v", err)
	}
	seed, ok := first.(RememberResult)
	if !ok {
		t.Fatalf("first result type = %T, want RememberResult", first)
	}
	if len(seed.PossibleConflicts) != 0 {
		t.Fatalf("seed write should produce no conflicts, got %d: %+v", len(seed.PossibleConflicts), seed.PossibleConflicts)
	}

	// Follow-up write with near-duplicate content must surface the prior
	// observation as a possible conflict on the SAME entity.
	second, err := HandleTool(db, "remember", ToolArgs{
		Entity:      "API",
		Observation: "rate limit is 200 per minute",
	})
	if err != nil {
		t.Fatalf("HandleTool(remember, follow-up) error = %v", err)
	}
	res, ok := second.(RememberResult)
	if !ok {
		t.Fatalf("follow-up result type = %T, want RememberResult", second)
	}
	if !res.Stored {
		t.Fatalf("follow-up Stored = false, want true")
	}
	if len(res.PossibleConflicts) == 0 {
		t.Fatalf("expected PossibleConflicts on near-duplicate, got 0")
	}
	if res.PossibleConflicts[0].ObservationID != seed.ObservationID {
		t.Fatalf("PossibleConflicts[0].ObservationID = %d, want %d", res.PossibleConflicts[0].ObservationID, seed.ObservationID)
	}
	// The follow-up's own observation ID must not be in the hints
	// (verifies the before-insert ordering choice).
	for _, hint := range res.PossibleConflicts {
		if hint.ObservationID == res.ObservationID {
			t.Fatalf("hint references just-inserted observation %d — detection must run before insert", res.ObservationID)
		}
	}
}

func TestHandleTool_RememberOmitsConflictsFieldWhenNoneQualify(t *testing.T) {
	t.Parallel()
	db := newConflictTestDB(t)

	result, err := HandleTool(db, "remember", ToolArgs{
		Entity:      "API",
		Observation: "first fact ever recorded for this entity",
	})
	if err != nil {
		t.Fatalf("HandleTool(remember) error = %v", err)
	}
	res := result.(RememberResult)
	if len(res.PossibleConflicts) != 0 {
		t.Fatalf("first-ever observation should not surface conflicts, got %+v", res.PossibleConflicts)
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
