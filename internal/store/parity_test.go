package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func newTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		_ = db.Close()
	})
	return db
}

func TestCoreMemoryParity(t *testing.T) {
	db := newTestDB(t, "core.db")

	t.Run("remember and recall", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "TestEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		if _, err := AddObservation(db, entityID, "test observation", "user", 1.0); err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		results, _, err := SearchMemory(db, "test", 5, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		if len(results) == 0 {
			t.Fatalf("SearchMemory() returned no results")
		}
	})

	t.Run("forget observation hides from recall", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "ForgetObsEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		observationID, err := AddObservation(db, entityID, "observation to forget", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		resultAny, err := HandleTool(db, "forget", ToolArgs{ObservationID: &observationID})
		if err != nil {
			t.Fatalf("HandleTool(forget) error = %v", err)
		}
		result := resultAny.(ForgetResult)
		if !result.Deleted {
			t.Fatalf("ForgetResult.Deleted = false, want true")
		}
		tombstoned, err := ObservationDeletedAtIsSet(db, observationID)
		if err != nil {
			t.Fatalf("ObservationDeletedAtIsSet() error = %v", err)
		}
		if !tombstoned {
			t.Fatalf("observation tombstone not set")
		}
		recalled, _, err := SearchMemory(db, "observation to forget", 5, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		for _, item := range recalled {
			if item.ID == observationID {
				t.Fatalf("deleted observation still appears in recall")
			}
		}
	})

	t.Run("forget entity hides from list and recall entity", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "EntityToForget", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		obs1, err := AddObservation(db, entityID, "entity observation 1", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(obs1) error = %v", err)
		}
		obs2, err := AddObservation(db, entityID, "entity observation 2", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(obs2) error = %v", err)
		}
		resultAny, err := HandleTool(db, "forget", ToolArgs{Entity: "EntityToForget"})
		if err != nil {
			t.Fatalf("HandleTool(forget entity) error = %v", err)
		}
		result := resultAny.(ForgetResult)
		if !result.Deleted {
			t.Fatalf("ForgetResult.Deleted = false, want true")
		}
		listed, err := ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities() error = %v", err)
		}
		for _, entity := range listed {
			if entity.Name == "EntityToForget" {
				t.Fatalf("deleted entity still appears in list")
			}
		}
		graph, err := GetEntityGraph(db, "EntityToForget", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph() error = %v", err)
		}
		if graph != nil {
			t.Fatalf("deleted entity still appears in recall_entity")
		}
		var deletedCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE id IN (?, ?) AND deleted_at IS NOT NULL`, obs1, obs2).Scan(&deletedCount); err != nil {
			t.Fatalf("count deleted observations error = %v", err)
		}
		if deletedCount != 2 {
			t.Fatalf("deleted observations = %d, want 2", deletedCount)
		}
	})

	t.Run("re-remembering forgotten entity keeps old relations dead", func(t *testing.T) {
		if _, err := UpsertEntity(db, "RelationSource", "test"); err != nil {
			t.Fatalf("UpsertEntity(RelationSource) error = %v", err)
		}
		zombieID, err := UpsertEntity(db, "ZombieEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity(ZombieEntity) error = %v", err)
		}
		if _, err := AddObservation(db, zombieID, "zombie first life", "user", 1.0); err != nil {
			t.Fatalf("AddObservation(zombie first life) error = %v", err)
		}
		if _, err := HandleTool(db, "relate", ToolArgs{From: "RelationSource", To: "ZombieEntity", RelationType: "knows"}); err != nil {
			t.Fatalf("HandleTool(relate) error = %v", err)
		}
		if _, err := HandleTool(db, "forget", ToolArgs{Entity: "ZombieEntity"}); err != nil {
			t.Fatalf("HandleTool(forget zombie) error = %v", err)
		}
		if _, err := HandleTool(db, "remember", ToolArgs{Entity: "ZombieEntity", EntityType: "test", Observation: "zombie second life"}); err != nil {
			t.Fatalf("HandleTool(remember zombie) error = %v", err)
		}
		resultAny, err := HandleTool(db, "recall_entity", ToolArgs{Entity: "ZombieEntity"})
		if err != nil {
			t.Fatalf("HandleTool(recall_entity) error = %v", err)
		}
		result := resultAny.(RecallEntityResult)
		if !result.Found {
			t.Fatalf("RecallEntityResult.Found = false, want true")
		}
		if len(result.RelationsIncoming) != 0 || len(result.RelationsOutgoing) != 0 {
			t.Fatalf("old relations resurrected after re-remember")
		}
	})

	t.Run("remember rejects empty observation", func(t *testing.T) {
		_, err := HandleTool(db, "remember", ToolArgs{Entity: "EmptyObservationEntity", Observation: "   ", Source: "session"})
		if err == nil {
			t.Fatal("HandleTool(remember) error = nil, want validation error")
		}
		if !strings.Contains(err.Error(), "observation must be non-empty") {
			t.Fatalf("HandleTool(remember) error = %q, want non-empty validation", err)
		}

		var entityCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = 'EmptyObservationEntity'`).Scan(&entityCount); err != nil {
			t.Fatalf("count entities after rejected remember error = %v", err)
		}
		if entityCount != 0 {
			t.Fatalf("entityCount = %d, want 0 after rejected remember", entityCount)
		}
	})
}

func TestEventsAndProvenanceParity(t *testing.T) {
	db := newTestDB(t, "events.db")

	t.Run("remember event and recall it", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "EventTestEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Test event", "", "test", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		if _, err := AddObservation(db, entityID, "event observation", "user", 1.0, eventID); err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		fullEvent, err := GetFullEvent(db, eventID, 12)
		if err != nil {
			t.Fatalf("GetFullEvent() error = %v", err)
		}
		if fullEvent == nil || fullEvent.TotalObservations != 1 {
			t.Fatalf("GetFullEvent() = %#v, want 1 observation", fullEvent)
		}
	})

	t.Run("deleted event observations disappear from event surfaces", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "DeletedEventEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Forget event", "", "test", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		obsID, err := AddObservation(db, entityID, "event observation to delete", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		if _, err := HandleTool(db, "forget", ToolArgs{ObservationID: &obsID}); err != nil {
			t.Fatalf("HandleTool(forget) error = %v", err)
		}
		fullEvent, err := GetFullEvent(db, eventID, 12)
		if err != nil {
			t.Fatalf("GetFullEvent() error = %v", err)
		}
		if fullEvent == nil || fullEvent.TotalObservations != 0 {
			t.Fatalf("deleted event observation still appears in full event")
		}
		events, err := SearchEvents(db, "Forget event", "", "", "", 5)
		if err != nil {
			t.Fatalf("SearchEvents() error = %v", err)
		}
		if len(events) == 0 || events[0].ObservationCount != 0 {
			t.Fatalf("deleted event observation still counted in search events")
		}
	})

	t.Run("get observations preserves order and skips tombstones", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "OrderedEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		first, err := AddObservation(db, entityID, "first ordered fact", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(first) error = %v", err)
		}
		second, err := AddObservation(db, entityID, "second ordered fact", "user", 0.8)
		if err != nil {
			t.Fatalf("AddObservation(second) error = %v", err)
		}
		if _, err := HandleTool(db, "forget", ToolArgs{ObservationID: &first}); err != nil {
			t.Fatalf("HandleTool(forget first) error = %v", err)
		}
		result, err := GetObservationsByIDs(db, []int64{second, first}, 12)
		if err != nil {
			t.Fatalf("GetObservationsByIDs() error = %v", err)
		}
		if result.Total != 1 || len(result.Observations) != 1 || result.Observations[0].ID != second {
			t.Fatalf("GetObservationsByIDs() order/tombstone behavior drifted: %#v", result)
		}
	})

	t.Run("observation entity type snapshot survives entity mutation", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "SnapshotEntity", "original_type")
		if err != nil {
			t.Fatalf("UpsertEntity(original) error = %v", err)
		}
		obsID, err := AddObservation(db, entityID, "snapshot fact", "user", 0.9)
		if err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		if _, err := UpsertEntity(db, "SnapshotEntity", "updated_type"); err != nil {
			t.Fatalf("UpsertEntity(updated) error = %v", err)
		}
		result, err := GetObservationsByIDs(db, []int64{obsID}, 12)
		if err != nil {
			t.Fatalf("GetObservationsByIDs() error = %v", err)
		}
		if result.Total != 1 || result.Observations[0].EntityType != "original_type" {
			t.Fatalf("stored entity_type snapshot drifted: %#v", result)
		}
	})

	t.Run("event observations remain chronological", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "ChronoEventEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Chrono event", "2026-04-13", "session", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		first, err := AddObservation(db, entityID, "chronological first", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation(first) error = %v", err)
		}
		second, err := AddObservation(db, entityID, "chronological second", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation(second) error = %v", err)
		}
		result, err := GetEventObservations(db, eventID, 12)
		if err != nil {
			t.Fatalf("GetEventObservations() error = %v", err)
		}
		if result == nil || result.Total != 2 {
			t.Fatalf("GetEventObservations() = %#v, want 2 observations", result)
		}
		if result.Observations[0].ID != first || result.Observations[1].ID != second {
			t.Fatalf("event observation order drifted: %#v", result.Observations)
		}
	})

	t.Run("remember event rejects empty attached observation without creating event", func(t *testing.T) {
		_, err := HandleTool(db, "remember_event", ToolArgs{
			Label:        "Bad event",
			Observations: []FactInput{{Entity: "BadEventEntity", Observation: "\t  ", Source: "session"}},
		})
		if err == nil {
			t.Fatal("HandleTool(remember_event) error = nil, want validation error")
		}
		if !strings.Contains(err.Error(), "observations[0].observation must be non-empty") {
			t.Fatalf("HandleTool(remember_event) error = %q, want nested non-empty validation", err)
		}

		var eventCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE label = 'Bad event'`).Scan(&eventCount); err != nil {
			t.Fatalf("count events after rejected remember_event error = %v", err)
		}
		if eventCount != 0 {
			t.Fatalf("eventCount = %d, want 0 after rejected remember_event", eventCount)
		}
	})
}

func TestProjectIsolationParity(t *testing.T) {
	if err := ResetProjectDBs(); err != nil {
		t.Fatalf("ResetProjectDBs() pre-test error = %v", err)
	}

	db := newTestDB(t, "global.db")
	projectPath := filepath.Join(t.TempDir(), "fake-project")

	// Register AFTER t.TempDir() so LIFO ordering runs this BEFORE TempDir
	// cleanup — ensures the project DB is closed before Windows tries to
	// delete the directory.
	t.Cleanup(func() {
		_ = ResetProjectDBs()
	})

	projectDB, err := GetDB(db, projectPath)
	if err != nil {
		t.Fatalf("GetDB(project) error = %v", err)
	}
	entityID, err := UpsertEntity(projectDB, "ProjectOnly", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(project) error = %v", err)
	}
	if _, err := AddObservation(projectDB, entityID, "project fact", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(project) error = %v", err)
	}
	globalResults, _, err := SearchMemory(db, "ProjectOnly", 5, 12)
	if err != nil {
		t.Fatalf("SearchMemory(global) error = %v", err)
	}
	if len(globalResults) != 0 {
		t.Fatalf("project entity leaked into global DB: %#v", globalResults)
	}
}

func TestRankingAndRecallParity(t *testing.T) {
	db := newTestDB(t, "rank.db")

	zara, err := UpsertEntity(db, "Zara", "person")
	if err != nil {
		t.Fatalf("UpsertEntity(Zara) error = %v", err)
	}
	if _, err := AddObservation(db, zara, "Zara is the lead architect, handles deploy workflows", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(Zara deploy) error = %v", err)
	}
	if _, err := AddObservation(db, zara, "Zara prefers trunk-based development", "user", 0.9); err != nil {
		t.Fatalf("AddObservation(Zara trunk) error = %v", err)
	}
	zaraMobile, err := UpsertEntity(db, "ZaraMobile", "project")
	if err != nil {
		t.Fatalf("UpsertEntity(ZaraMobile) error = %v", err)
	}
	if _, err := AddObservation(db, zaraMobile, "A mobile app project, cross-platform", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(ZaraMobile) error = %v", err)
	}
	tom, err := UpsertEntity(db, "Tom", "person")
	if err != nil {
		t.Fatalf("UpsertEntity(Tom) error = %v", err)
	}
	if _, err := AddObservation(db, tom, "Tom once mentioned Zara in passing", "user", 1.0); err != nil {
		t.Fatalf("AddObservation(Tom) error = %v", err)
	}
	evID, err := CreateEvent(db, "Sprint with Zara - kickoff", "", "session", "", "")
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	sprintLog, err := UpsertEntity(db, "SprintLog", "log")
	if err != nil {
		t.Fatalf("UpsertEntity(SprintLog) error = %v", err)
	}
	if _, err := AddObservation(db, sprintLog, "First sprint notes", "user", 0.8, evID); err != nil {
		t.Fatalf("AddObservation(SprintLog) error = %v", err)
	}

	t.Run("composite score present on results", func(t *testing.T) {
		results, _, err := SearchMemory(db, "Zara", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		if len(results) == 0 {
			t.Fatalf("SearchMemory() returned no results")
		}
		for _, item := range results {
			if item.CompositeScore <= 0 {
				t.Fatalf("CompositeScore = %v, want > 0", item.CompositeScore)
			}
		}
	})

	t.Run("entity exact outranks entity like", func(t *testing.T) {
		results, _, err := SearchMemory(db, "Zara", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		bestExact := 0.0
		bestLike := 0.0
		for _, item := range results {
			switch item.EntityName {
			case "Zara":
				if item.CompositeScore > bestExact {
					bestExact = item.CompositeScore
				}
			case "ZaraMobile":
				if item.CompositeScore > bestLike {
					bestLike = item.CompositeScore
				}
			}
		}
		if bestExact <= bestLike {
			t.Fatalf("entity_exact score %v should be > entity_like %v", bestExact, bestLike)
		}
	})

	t.Run("recall handler exposes composite score", func(t *testing.T) {
		limit := 5
		resultAny, err := HandleTool(db, "recall", ToolArgs{Query: "Zara", Limit: &limit})
		if err != nil {
			t.Fatalf("HandleTool(recall) error = %v", err)
		}
		result := resultAny.(RecallResponse)
		if len(result.Results) == 0 || len(result.Results[0].Observations) == 0 {
			t.Fatalf("RecallResponse unexpectedly empty")
		}
		if result.Results[0].Observations[0].CompositeScore == 0 {
			t.Fatalf("Recall observation missing composite score")
		}
	})

	t.Run("compact recall truncates and flags", func(t *testing.T) {
		compactDB := newTestDB(t, "compact.db")
		entityID, err := UpsertEntity(compactDB, "CompactTarget", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		longContent := make([]byte, compactSnippetLength()+80)
		for i := range longContent {
			longContent[i] = 'a'
		}
		if _, err := AddObservation(compactDB, entityID, string(longContent), "user", 1.0); err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		resultAny, err := HandleTool(compactDB, "recall", ToolArgs{Query: "CompactTarget", Compact: true})
		if err != nil {
			t.Fatalf("HandleTool(recall compact) error = %v", err)
		}
		result := resultAny.(RecallResponse)
		if !result.Compact {
			t.Fatalf("RecallResponse.Compact = false, want true")
		}
		observation := result.Results[0].Observations[0]
		if utf8.RuneCountInString(observation.Content) > compactSnippetLength() || !observation.Truncated {
			t.Fatalf("compact recall did not truncate correctly: %#v", observation)
		}
		if observation.Content[len(observation.Content)-len("…"):] != "…" {
			t.Fatalf("truncated content missing ellipsis: %q", observation.Content)
		}
	})
}

func TestRememberEventAtomicityOnMidLoopFailure(t *testing.T) {
	db := newTestDB(t, "atomicity.db")

	if _, err := db.Exec(
		`CREATE TRIGGER fail_on_sentinel BEFORE INSERT ON entities
		 WHEN NEW.name = 'FAIL_ME'
		 BEGIN SELECT RAISE(ABORT, 'injected failure'); END;`,
	); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	var eventsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventsBefore); err != nil {
		t.Fatalf("count events before: %v", err)
	}

	_, err := HandleTool(db, "remember_event", ToolArgs{
		Label:     "Atomicity probe",
		EventType: "test",
		Observations: []FactInput{
			{Entity: "Alpha", Observation: "first observation"},
			{Entity: "FAIL_ME", Observation: "second observation triggers abort"},
		},
	})
	if err == nil {
		t.Fatalf("HandleTool(remember_event) expected error from injected failure, got nil")
	}

	var eventsAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after: %v", err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("events row count changed despite mid-loop failure: before=%d after=%d (orphan event row left behind)", eventsBefore, eventsAfter)
	}

	var alphaCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = ?`, "Alpha").Scan(&alphaCount); err != nil {
		t.Fatalf("count Alpha entity: %v", err)
	}
	if alphaCount != 0 {
		t.Fatalf("Alpha entity persisted despite rollback: count=%d", alphaCount)
	}
}
