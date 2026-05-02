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

func TestSearchEventsIgnoresSupersededObservationsInCounts(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "events-superseded-count.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	eventID, err := CreateEvent(db, "Superseded count event", "2026-04-26", "meeting", "", "")
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	entityID, err := UpsertEntity(db, "SupersededCountEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	sourceID, err := AddObservation(db, entityID, "old count value", "user", 1.0, eventID)
	if err != nil {
		t.Fatalf("AddObservation(source) error = %v", err)
	}
	targetID, err := AddObservation(db, entityID, "new count value", "user", 1.0, eventID)
	if err != nil {
		t.Fatalf("AddObservation(target) error = %v", err)
	}
	markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")

	results, err := SearchEvents(db, "Superseded count event", "", "", "", 10)
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("SearchEvents() returned %d results, want 1", len(results))
	}
	if results[0].ObservationCount != 1 {
		t.Fatalf("SearchEvents() ObservationCount = %d, want 1", results[0].ObservationCount)
	}
}

func TestSearchEventsEscapesLikeWildcards(t *testing.T) {
	t.Parallel()

	db, err := InitDB(filepath.Join(t.TempDir(), "events-like-escape.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()

	labels := []string{
		"Launch 50% discount",
		"Launch 500 discount",
		"file_name rollout",
		"file-name rollout",
		`path\prod migration`,
		"path/prod migration",
	}
	for _, label := range labels {
		if _, err := CreateEvent(db, label, "2026-04-26", "test", "", ""); err != nil {
			t.Fatalf("CreateEvent(%q) error = %v", label, err)
		}
	}

	cases := []struct {
		name      string
		query     string
		wantLabel string
	}{
		{name: "percent is literal", query: "50%", wantLabel: "Launch 50% discount"},
		{name: "underscore is literal", query: "file_name", wantLabel: "file_name rollout"},
		{name: "backslash is literal", query: `path\prod`, wantLabel: `path\prod migration`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := SearchEvents(db, tc.query, "", "", "", 10)
			if err != nil {
				t.Fatalf("SearchEvents(%q) error = %v", tc.query, err)
			}
			if len(results) != 1 {
				t.Fatalf("SearchEvents(%q) returned %d results, want exactly 1: %#v", tc.query, len(results), results)
			}
			if results[0].Label != tc.wantLabel {
				t.Fatalf("SearchEvents(%q) label = %q, want %q", tc.query, results[0].Label, tc.wantLabel)
			}
		})
	}
}
