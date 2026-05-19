package store

import (
	"bytes"
	"testing"
	"time"
)

func TestObservationEmbeddingCacheIdentity(t *testing.T) {
	db := newTestDB(t, "embedding-cache-identity.db")
	entityID, err := UpsertEntity(db, "EmbeddingCacheEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	observationID, err := AddObservation(db, entityID, "cache identity content", "test", 1.0)
	if err != nil {
		t.Fatalf("AddObservation() error = %v", err)
	}
	key := EmbeddingCacheKey{Provider: "openai-compatible", EndpointKey: "http://localhost:1235/v1", ModelID: "local-model", Dimensions: 2}
	blob := []byte{1, 2, 3, 4}
	if err := UpsertObservationEmbedding(db, observationID, key, blob); err != nil {
		t.Fatalf("UpsertObservationEmbedding() error = %v", err)
	}
	loaded, err := LoadObservationEmbeddings(db, []int64{observationID}, key)
	if err != nil {
		t.Fatalf("LoadObservationEmbeddings() error = %v", err)
	}
	if !bytes.Equal(loaded[observationID], blob) {
		t.Fatalf("loaded embedding = %v, want %v", loaded[observationID], blob)
	}
	missKey := key
	missKey.EndpointKey = "http://localhost:2235/v1"
	misses, err := LoadObservationEmbeddings(db, []int64{observationID}, missKey)
	if err != nil {
		t.Fatalf("LoadObservationEmbeddings(miss) error = %v", err)
	}
	if len(misses) != 0 {
		t.Fatalf("endpoint-key miss loaded %d embeddings, want 0", len(misses))
	}
	updated := []byte{4, 3, 2, 1}
	if err := UpsertObservationEmbedding(db, observationID, key, updated); err != nil {
		t.Fatalf("UpsertObservationEmbedding(update) error = %v", err)
	}
	loaded, err = LoadObservationEmbeddings(db, []int64{observationID}, key)
	if err != nil {
		t.Fatalf("LoadObservationEmbeddings(update) error = %v", err)
	}
	if !bytes.Equal(loaded[observationID], updated) {
		t.Fatalf("updated embedding = %v, want %v", loaded[observationID], updated)
	}
}

func TestSelectSemanticReconcileObservationsUsesLifecycleGuards(t *testing.T) {
	db := newTestDB(t, "semantic-select-lifecycle.db")
	now := time.Now().UTC()
	entityID, err := UpsertEntity(db, "SemanticLifecycleEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	activeA := insertRawObservationForReconcileTest(t, db, entityID, "semantic active a", now.Add(-2*time.Hour))
	activeB := insertRawObservationForReconcileTest(t, db, entityID, "semantic active b", now.Add(-1*time.Hour))
	deletedID := insertRawObservationForReconcileTest(t, db, entityID, "semantic deleted", now.Add(-30*time.Minute))
	if _, err := db.Exec(`UPDATE observations SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, deletedID); err != nil {
		t.Fatalf("tombstone observation error = %v", err)
	}
	supersededID := insertRawObservationForReconcileTest(t, db, entityID, "semantic superseded", now.Add(-20*time.Minute))
	if _, err := db.Exec(`UPDATE observations SET superseded_by = ? WHERE id = ?`, activeB, supersededID); err != nil {
		t.Fatalf("supersede observation error = %v", err)
	}
	expiredEventID, err := CreateEvent(db, "Expired semantic event", "", "test", "", now.Add(-time.Hour).Format(sqliteTimestampLayout))
	if err != nil {
		t.Fatalf("CreateEvent(expired) error = %v", err)
	}
	insertRawObservationForReconcileTest(t, db, entityID, "semantic expired event", now.Add(-10*time.Minute), expiredEventID)

	signals, observations, err := SelectSemanticReconcileObservations(db, SemanticObservationSelectOptions{
		GeneratedAt:       now,
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
	})
	if err != nil {
		t.Fatalf("SelectSemanticReconcileObservations() error = %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(signals))
	}
	ids := map[int64]bool{}
	for _, observation := range observations {
		ids[observation.ID] = true
	}
	if !ids[activeA] || !ids[activeB] {
		t.Fatalf("active observations missing from semantic selection: ids=%v", ids)
	}
	if ids[deletedID] || ids[supersededID] {
		t.Fatalf("inactive observation included in semantic selection: ids=%v", ids)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2 active observations", len(observations))
	}
}
