package store

import (
	"path/filepath"
	"testing"
)

func TestSearchEventsCountsObservationsCorrectly(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "events-count.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	eventID, err := CreateEvent(db, "Standup", "2026-04-26", "meeting", "", "")
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}

	entityA, err := UpsertEntity(db, "Alice", "person")
	if err != nil {
		t.Fatalf("UpsertEntity(Alice) error = %v", err)
	}
	entityB, err := UpsertEntity(db, "Bob", "person")
	if err != nil {
		t.Fatalf("UpsertEntity(Bob) error = %v", err)
	}

	if _, err := AddObservation(db, entityA, "discussed API design", "user", 1.0, eventID); err != nil {
		t.Fatalf("AddObservation(Alice,1) error = %v", err)
	}
	if _, err := AddObservation(db, entityA, "reviewed PR #42", "user", 1.0, eventID); err != nil {
		t.Fatalf("AddObservation(Alice,2) error = %v", err)
	}
	if _, err := AddObservation(db, entityB, "raised infra concern", "user", 1.0, eventID); err != nil {
		t.Fatalf("AddObservation(Bob) error = %v", err)
	}

	results, err := SearchEvents(db, "", "", "", "", 10)
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchEvents() returned %d results, want 1", len(results))
	}
	if results[0].ObservationCount != 3 {
		t.Fatalf("SearchEvents() ObservationCount = %d, want 3", results[0].ObservationCount)
	}
}
