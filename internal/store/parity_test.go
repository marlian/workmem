package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func listedEntityByName(listed []ListedEntity, name string) (ListedEntity, bool) {
	for _, entity := range listed {
		if entity.Name == name {
			return entity, true
		}
	}
	return ListedEntity{}, false
}

func markObservationSupersededForTest(t *testing.T, db *sql.DB, sourceID, targetID int64, reason string) {
	t.Helper()
	if reason == "" {
		reason = "test_supersession"
	}
	result, err := db.Exec(
		`UPDATE observations
		 SET superseded_by = ?, superseded_at = CURRENT_TIMESTAMP, superseded_reason = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		targetID,
		reason,
		sourceID,
	)
	if err != nil {
		t.Fatalf("mark observation superseded: %v", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("mark observation superseded rows affected: %v", err)
	}
	if rowsAffected != 1 {
		t.Fatalf("mark observation superseded rows affected = %d, want 1", rowsAffected)
	}
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

	t.Run("zero observation entities stay hidden unless relation-only", func(t *testing.T) {
		if _, err := UpsertEntity(db, "EmptyShellEntity", "test"); err != nil {
			t.Fatalf("UpsertEntity(empty shell) error = %v", err)
		}
		listed, err := ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities(empty shell) error = %v", err)
		}
		if _, ok := listedEntityByName(listed, "EmptyShellEntity"); ok {
			t.Fatalf("empty shell appears in ListEntities")
		}
		graph, err := GetEntityGraph(db, "EmptyShellEntity", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph(empty shell) error = %v", err)
		}
		if graph != nil {
			t.Fatalf("empty shell appears in GetEntityGraph: %#v", graph)
		}

		forgottenID, err := UpsertEntity(db, "ForgottenLastObservationEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity(forgotten last observation) error = %v", err)
		}
		observationID, err := AddObservation(db, forgottenID, "last observation to forget", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(last observation) error = %v", err)
		}
		listed, err = ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities(with observation) error = %v", err)
		}
		if _, ok := listedEntityByName(listed, "ForgottenLastObservationEntity"); !ok {
			t.Fatalf("entity with an active observation missing from ListEntities")
		}
		if _, err := HandleTool(db, "forget", ToolArgs{ObservationID: &observationID}); err != nil {
			t.Fatalf("HandleTool(forget last observation) error = %v", err)
		}
		listed, err = ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities(after last observation forget) error = %v", err)
		}
		if _, ok := listedEntityByName(listed, "ForgottenLastObservationEntity"); ok {
			t.Fatalf("entity with no active observations or relations appears in ListEntities")
		}
		graph, err = GetEntityGraph(db, "ForgottenLastObservationEntity", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph(after last observation forget) error = %v", err)
		}
		if graph != nil {
			t.Fatalf("entity with no active observations or relations appears in GetEntityGraph: %#v", graph)
		}

		if _, err := HandleTool(db, "relate", ToolArgs{From: "RelationOnlySource", To: "RelationOnlyTarget", RelationType: "depends_on"}); err != nil {
			t.Fatalf("HandleTool(relation-only relate) error = %v", err)
		}
		listed, err = ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities(relation-only) error = %v", err)
		}
		if source, ok := listedEntityByName(listed, "RelationOnlySource"); !ok {
			t.Fatalf("relation-only source missing from ListEntities")
		} else if source.ObservationCount != 0 {
			t.Fatalf("relation-only source observation_count = %d, want 0", source.ObservationCount)
		}
		if target, ok := listedEntityByName(listed, "RelationOnlyTarget"); !ok {
			t.Fatalf("relation-only target missing from ListEntities")
		} else if target.ObservationCount != 0 {
			t.Fatalf("relation-only target observation_count = %d, want 0", target.ObservationCount)
		}

		sourceGraph, err := GetEntityGraph(db, "RelationOnlySource", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph(relation-only source) error = %v", err)
		}
		if sourceGraph == nil || len(sourceGraph.Observations) != 0 || len(sourceGraph.RelationsOutgoing) != 1 || len(sourceGraph.RelationsIncoming) != 0 {
			t.Fatalf("relation-only source graph = %#v, want one outgoing relation and no observations", sourceGraph)
		}
		targetGraph, err := GetEntityGraph(db, "RelationOnlyTarget", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph(relation-only target) error = %v", err)
		}
		if targetGraph == nil || len(targetGraph.Observations) != 0 || len(targetGraph.RelationsIncoming) != 1 || len(targetGraph.RelationsOutgoing) != 0 {
			t.Fatalf("relation-only target graph = %#v, want one incoming relation and no observations", targetGraph)
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

	t.Run("write tools reject confidence outside contract before mutation", func(t *testing.T) {
		high := 1.01
		negative := -0.01
		cases := []struct {
			name       string
			tool       string
			args       ToolArgs
			entityName string
			eventLabel string
		}{
			{
				name:       "remember high confidence",
				tool:       "remember",
				args:       ToolArgs{Entity: "HighConfidenceRemember", Observation: "too confident", Confidence: &high},
				entityName: "HighConfidenceRemember",
			},
			{
				name:       "remember negative confidence",
				tool:       "remember",
				args:       ToolArgs{Entity: "NegativeConfidenceRemember", Observation: "negative confidence", Confidence: &negative},
				entityName: "NegativeConfidenceRemember",
			},
			{
				name:       "remember_batch high confidence",
				tool:       "remember_batch",
				args:       ToolArgs{Facts: []FactInput{{Entity: "HighConfidenceBatch", Observation: "too confident batch", Confidence: &high}}},
				entityName: "HighConfidenceBatch",
			},
			{
				name:       "remember_event high confidence",
				tool:       "remember_event",
				args:       ToolArgs{Label: "High confidence event", Observations: []FactInput{{Entity: "HighConfidenceEventEntity", Observation: "too confident event", Confidence: &high}}},
				entityName: "HighConfidenceEventEntity",
				eventLabel: "High confidence event",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := HandleTool(db, tc.tool, tc.args)
				if err == nil {
					t.Fatalf("HandleTool(%s) error = nil, want confidence validation error", tc.tool)
				}
				if !strings.Contains(err.Error(), "confidence must be between 0.0 and 1.0") {
					t.Fatalf("HandleTool(%s) error = %q, want confidence range validation", tc.tool, err)
				}

				var entityCount int
				if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = ?`, tc.entityName).Scan(&entityCount); err != nil {
					t.Fatalf("count entities after rejected %s error = %v", tc.tool, err)
				}
				if entityCount != 0 {
					t.Fatalf("entityCount = %d, want 0 after rejected %s", entityCount, tc.tool)
				}

				if tc.eventLabel != "" {
					var eventCount int
					if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE label = ?`, tc.eventLabel).Scan(&eventCount); err != nil {
						t.Fatalf("count events after rejected %s error = %v", tc.tool, err)
					}
					if eventCount != 0 {
						t.Fatalf("eventCount = %d, want 0 after rejected %s", eventCount, tc.tool)
					}
				}
			})
		}
	})

	t.Run("write tools accept confidence bounds", func(t *testing.T) {
		zero := 0.0
		one := 1.0
		if _, err := HandleTool(db, "remember", ToolArgs{Entity: "ZeroConfidenceEntity", Observation: "zero confidence is allowed", Confidence: &zero}); err != nil {
			t.Fatalf("HandleTool(remember zero confidence) error = %v", err)
		}
		if _, err := HandleTool(db, "remember", ToolArgs{Entity: "OneConfidenceEntity", Observation: "one confidence is allowed", Confidence: &one}); err != nil {
			t.Fatalf("HandleTool(remember one confidence) error = %v", err)
		}
	})

	t.Run("relate rejects case-insensitive self relation before mutation", func(t *testing.T) {
		_, err := HandleTool(db, "relate", ToolArgs{From: "SelfRelationEntity", To: "selfrelationentity", RelationType: "same_as"})
		if err == nil {
			t.Fatal("HandleTool(relate self) error = nil, want validation error")
		}
		if !strings.Contains(err.Error(), "from and to must be different") {
			t.Fatalf("HandleTool(relate self) error = %q, want self-relation validation", err)
		}

		var entityCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = 'SelfRelationEntity'`).Scan(&entityCount); err != nil {
			t.Fatalf("count entities after rejected self relate error = %v", err)
		}
		if entityCount != 0 {
			t.Fatalf("entityCount = %d, want 0 after rejected self relate", entityCount)
		}
		var relationCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM relations WHERE relation_type = 'same_as'`).Scan(&relationCount); err != nil {
			t.Fatalf("count relations after rejected self relate error = %v", err)
		}
		if relationCount != 0 {
			t.Fatalf("relationCount = %d, want 0 after rejected self relate", relationCount)
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

	t.Run("superseded observations disappear from normal read surfaces", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "SupersededSurfaceEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Superseded visibility event", "", "test", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		sourceID, err := AddObservation(db, entityID, "supersededonlytoken", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation(source) error = %v", err)
		}
		targetID, err := AddObservation(db, entityID, "active replacement token", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation(target) error = %v", err)
		}
		markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")

		searchResults, _, err := SearchMemory(db, "supersededonlytoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		if len(searchResults) != 0 {
			t.Fatalf("SearchMemory returned superseded observation: %#v", searchResults)
		}

		graph, err := GetEntityGraph(db, "SupersededSurfaceEntity", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph() error = %v", err)
		}
		if graph == nil || len(graph.Observations) != 1 || graph.Observations[0].ID != targetID {
			t.Fatalf("GetEntityGraph() = %#v, want only target observation %d", graph, targetID)
		}

		listed, err := ListEntities(db, "", 50)
		if err != nil {
			t.Fatalf("ListEntities() error = %v", err)
		}
		listedEntity, ok := listedEntityByName(listed, "SupersededSurfaceEntity")
		if !ok {
			t.Fatalf("superseded test entity missing from ListEntities")
		}
		if listedEntity.ObservationCount != 1 {
			t.Fatalf("ListEntities observation_count = %d, want 1", listedEntity.ObservationCount)
		}

		byID, err := GetObservationsByIDs(db, []int64{sourceID, targetID}, 12)
		if err != nil {
			t.Fatalf("GetObservationsByIDs() error = %v", err)
		}
		if byID.Total != 1 || len(byID.Observations) != 1 || byID.Observations[0].ID != targetID {
			t.Fatalf("GetObservationsByIDs() = %#v, want only target observation %d", byID, targetID)
		}

		fullEvent, err := GetFullEvent(db, eventID, 12)
		if err != nil {
			t.Fatalf("GetFullEvent() error = %v", err)
		}
		if fullEvent == nil || fullEvent.TotalObservations != 1 || len(fullEvent.Entities) != 1 {
			t.Fatalf("GetFullEvent() = %#v, want one active observation", fullEvent)
		}
		group := fullEvent.Entities[0]
		if len(group.Observations) != 1 || group.Observations[0].ID != targetID {
			t.Fatalf("GetFullEvent observations = %#v, want only target observation %d", group.Observations, targetID)
		}

		eventObservations, err := GetEventObservations(db, eventID, 12)
		if err != nil {
			t.Fatalf("GetEventObservations() error = %v", err)
		}
		if eventObservations == nil || eventObservations.Total != 1 || len(eventObservations.Observations) != 1 || eventObservations.Observations[0].ID != targetID {
			t.Fatalf("GetEventObservations() = %#v, want only target observation %d", eventObservations, targetID)
		}

		events, err := SearchEvents(db, "Superseded visibility event", "", "", "", 5)
		if err != nil {
			t.Fatalf("SearchEvents() error = %v", err)
		}
		if len(events) != 1 || events[0].ObservationCount != 1 {
			t.Fatalf("SearchEvents() = %#v, want one counted active observation", events)
		}
	})

	t.Run("tombstoned entity drift disappears from event counts and label candidates", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "EventLabelTombstoneEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Unique Tombstone Label", "", "session", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		if _, err := AddObservation(db, entityID, "content that does not contain event label token", "user", 1.0, eventID); err != nil {
			t.Fatalf("AddObservation() error = %v", err)
		}
		// Simulate legacy/partial drift: the entity is tombstoned but its
		// observation row remains live. Event surfaces and event-label
		// candidate collection must still respect entity tombstones.
		if _, err := db.Exec(`UPDATE entities SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, entityID); err != nil {
			t.Fatalf("tombstone entity only: %v", err)
		}

		events, err := SearchEvents(db, "Unique Tombstone Label", "", "", "", 5)
		if err != nil {
			t.Fatalf("SearchEvents() error = %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("SearchEvents() returned %d events, want 1: %#v", len(events), events)
		}
		if events[0].ObservationCount != 0 {
			t.Fatalf("SearchEvents() observation_count = %d, want 0 for tombstoned entity drift", events[0].ObservationCount)
		}

		candidates, err := CollectCandidates(db, "Unique Tombstone Label", 20, 10)
		if err != nil {
			t.Fatalf("CollectCandidates() error = %v", err)
		}
		if len(candidates) != 0 {
			t.Fatalf("CollectCandidates() returned tombstoned entity candidates: %#v", candidates)
		}
	})

	t.Run("expired event observations disappear from normal read surfaces", func(t *testing.T) {
		expiredAt := time.Now().Add(-1 * time.Hour).UTC().Format(sqliteTimestampLayout)
		futureAt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)

		expiredEntityID, err := UpsertEntity(db, "ExpiredEventEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity(expired) error = %v", err)
		}
		expiredEventID, err := CreateEvent(db, "Expired hidden event", "", "session", "", "")
		if err != nil {
			t.Fatalf("CreateEvent(expired) error = %v", err)
		}
		expiredObservationID, err := AddObservation(db, expiredEntityID, "expiredonlytoken", "user", 1.0, expiredEventID)
		if err != nil {
			t.Fatalf("AddObservation(expired) error = %v", err)
		}
		if _, err := db.Exec(`UPDATE events SET expires_at = ? WHERE id = ?`, expiredAt, expiredEventID); err != nil {
			t.Fatalf("expire event error = %v", err)
		}

		futureEntityID, err := UpsertEntity(db, "FutureEventEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity(future) error = %v", err)
		}
		futureEventID, err := CreateEvent(db, "Future visible event", "", "session", "", futureAt)
		if err != nil {
			t.Fatalf("CreateEvent(future) error = %v", err)
		}
		futureObservationID, err := AddObservation(db, futureEntityID, "futureonlytoken", "user", 1.0, futureEventID)
		if err != nil {
			t.Fatalf("AddObservation(future) error = %v", err)
		}

		plainEntityID, err := UpsertEntity(db, "PlainEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity(plain) error = %v", err)
		}
		if _, err := AddObservation(db, plainEntityID, "plainonlytoken", "user", 1.0); err != nil {
			t.Fatalf("AddObservation(plain) error = %v", err)
		}

		events, err := SearchEvents(db, "Expired hidden event", "", "", "", 5)
		if err != nil {
			t.Fatalf("SearchEvents(expired) error = %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("SearchEvents returned expired event: %#v", events)
		}

		fullExpired, err := GetFullEvent(db, expiredEventID, 12)
		if err != nil {
			t.Fatalf("GetFullEvent(expired) error = %v", err)
		}
		if fullExpired != nil {
			t.Fatalf("GetFullEvent returned expired event: %#v", fullExpired)
		}

		expiredEventObservations, err := GetEventObservations(db, expiredEventID, 12)
		if err != nil {
			t.Fatalf("GetEventObservations(expired) error = %v", err)
		}
		if expiredEventObservations != nil {
			t.Fatalf("GetEventObservations returned expired event observations: %#v", expiredEventObservations)
		}

		expiredByID, err := GetObservationsByIDs(db, []int64{expiredObservationID}, 12)
		if err != nil {
			t.Fatalf("GetObservationsByIDs(expired) error = %v", err)
		}
		if expiredByID.Total != 0 || len(expiredByID.Observations) != 0 {
			t.Fatalf("GetObservationsByIDs returned expired observation: %#v", expiredByID)
		}

		expiredRecall, _, err := SearchMemory(db, "expiredonlytoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory(expired) error = %v", err)
		}
		if len(expiredRecall) != 0 {
			t.Fatalf("SearchMemory returned expired observation: %#v", expiredRecall)
		}

		expiredGraph, err := GetEntityGraph(db, "ExpiredEventEntity", 12)
		if err != nil {
			t.Fatalf("GetEntityGraph(expired) error = %v", err)
		}
		if expiredGraph != nil {
			t.Fatalf("GetEntityGraph returned entity whose only observation is expired: %#v", expiredGraph)
		}

		futureRecall, _, err := SearchMemory(db, "futureonlytoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory(future) error = %v", err)
		}
		if len(futureRecall) != 1 || futureRecall[0].ID != futureObservationID {
			t.Fatalf("SearchMemory(future) = %#v, want future observation %d", futureRecall, futureObservationID)
		}

		plainRecall, _, err := SearchMemory(db, "plainonlytoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory(plain) error = %v", err)
		}
		if len(plainRecall) != 1 {
			t.Fatalf("SearchMemory(plain) = %#v, want non-event observation visible", plainRecall)
		}
	})

	t.Run("remember event rejects invalid expires_at", func(t *testing.T) {
		if _, err := CreateEvent(db, "Bad expiry event", "", "session", "", "not-a-timestamp"); err == nil {
			t.Fatalf("CreateEvent() error = nil, want invalid expires_at rejection")
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE label = 'Bad expiry event'`).Scan(&count); err != nil {
			t.Fatalf("count bad expiry events error = %v", err)
		}
		if count != 0 {
			t.Fatalf("invalid expires_at inserted %d event row(s), want 0", count)
		}
	})

	t.Run("expired event observations do not satisfy duplicate writes", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "ExpiredDuplicateEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		eventID, err := CreateEvent(db, "Expired duplicate event", "", "session", "", "")
		if err != nil {
			t.Fatalf("CreateEvent() error = %v", err)
		}
		expiredObservationID, err := AddObservation(db, entityID, "duplicateexpirytoken", "user", 1.0, eventID)
		if err != nil {
			t.Fatalf("AddObservation(event) error = %v", err)
		}
		if _, err := db.Exec(`UPDATE events SET expires_at = ? WHERE id = ?`, time.Now().Add(-1*time.Hour).UTC().Format(sqliteTimestampLayout), eventID); err != nil {
			t.Fatalf("expire event error = %v", err)
		}

		newObservationID, err := AddObservation(db, entityID, "duplicateexpirytoken", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(non-event duplicate) error = %v", err)
		}
		if newObservationID == expiredObservationID {
			t.Fatalf("AddObservation reused expired observation id %d", expiredObservationID)
		}

		results, _, err := SearchMemory(db, "duplicateexpirytoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		if len(results) != 1 || results[0].ID != newObservationID {
			t.Fatalf("SearchMemory() = %#v, want only new observation %d", results, newObservationID)
		}

		if _, err := AddObservation(db, entityID, "hiddenwritetoken", "user", 1.0, eventID); err == nil {
			t.Fatalf("AddObservation(expired event) error = nil, want rejection")
		}
		if _, err := AddObservation(db, entityID, "duplicateexpirytoken", "user", 1.0, eventID); err == nil {
			t.Fatalf("AddObservation(duplicate content with expired event) error = nil, want event validation before dedupe")
		}
	})

	t.Run("superseded observations do not satisfy duplicate writes", func(t *testing.T) {
		entityID, err := UpsertEntity(db, "SupersededDuplicateEntity", "test")
		if err != nil {
			t.Fatalf("UpsertEntity() error = %v", err)
		}
		sourceID, err := AddObservation(db, entityID, "duplicatesupersededtoken", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(source) error = %v", err)
		}
		targetID, err := AddObservation(db, entityID, "canonical replacement for duplicate superseded token", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(target) error = %v", err)
		}
		markObservationSupersededForTest(t, db, sourceID, targetID, "test_supersession")

		newObservationID, err := AddObservation(db, entityID, "duplicatesupersededtoken", "user", 1.0)
		if err != nil {
			t.Fatalf("AddObservation(non-superseded duplicate) error = %v", err)
		}
		if newObservationID == sourceID {
			t.Fatalf("AddObservation reused superseded observation id %d", sourceID)
		}

		results, _, err := SearchMemory(db, "duplicatesupersededtoken", 10, 12)
		if err != nil {
			t.Fatalf("SearchMemory() error = %v", err)
		}
		if len(results) != 1 || results[0].ID != newObservationID {
			t.Fatalf("SearchMemory() = %#v, want only new observation %d", results, newObservationID)
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

	projectDB, releaseProjectDB, err := AcquireDB(db, projectPath)
	if err != nil {
		t.Fatalf("AcquireDB(project) error = %v", err)
	}
	defer releaseProjectDB()
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

func TestRememberAtomicityOnObservationFailure(t *testing.T) {
	db := newTestDB(t, "remember-atomicity.db")

	if _, err := db.Exec(
		`CREATE TRIGGER fail_remember_observation BEFORE INSERT ON observations
		 WHEN NEW.content = 'remember tx fail'
		 BEGIN SELECT RAISE(ABORT, 'injected observation failure'); END;`,
	); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	_, err := HandleTool(db, "remember", ToolArgs{Entity: "RememberTxEntity", Observation: "remember tx fail"})
	if err == nil {
		t.Fatalf("HandleTool(remember) expected error from injected failure, got nil")
	}

	var entityCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = ?`, "RememberTxEntity").Scan(&entityCount); err != nil {
		t.Fatalf("count entity after failed remember: %v", err)
	}
	if entityCount != 0 {
		t.Fatalf("remember entity persisted despite rollback: count=%d", entityCount)
	}

	var observationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE content = ?`, "remember tx fail").Scan(&observationCount); err != nil {
		t.Fatalf("count observations after failed remember: %v", err)
	}
	if observationCount != 0 {
		t.Fatalf("remember observation persisted despite rollback: count=%d", observationCount)
	}
}

func TestRememberAtomicityOnFTSFailure(t *testing.T) {
	db := newTestDB(t, "remember-fts-atomicity.db")

	// Force AddObservation to fail after inserting the observation row but
	// before returning: the memory_fts insert is the final write in that helper.
	if _, err := db.Exec(`DROP TABLE memory_fts`); err != nil {
		t.Fatalf("drop memory_fts: %v", err)
	}

	_, err := HandleTool(db, "remember", ToolArgs{Entity: "RememberFTSFailureEntity", Observation: "fts failure should roll back"})
	if err == nil {
		t.Fatalf("HandleTool(remember) expected FTS failure, got nil")
	}

	var entityCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name = ?`, "RememberFTSFailureEntity").Scan(&entityCount); err != nil {
		t.Fatalf("count entity after FTS failure: %v", err)
	}
	if entityCount != 0 {
		t.Fatalf("remember entity persisted despite FTS rollback: count=%d", entityCount)
	}

	var observationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE content = ?`, "fts failure should roll back").Scan(&observationCount); err != nil {
		t.Fatalf("count observations after FTS failure: %v", err)
	}
	if observationCount != 0 {
		t.Fatalf("remember observation persisted despite FTS rollback: count=%d", observationCount)
	}
}

func TestRelateAtomicityOnRelationFailure(t *testing.T) {
	db := newTestDB(t, "relate-atomicity.db")

	if _, err := db.Exec(
		`CREATE TRIGGER fail_relation_insert BEFORE INSERT ON relations
		 WHEN NEW.relation_type = 'fails'
		 BEGIN SELECT RAISE(ABORT, 'injected relation failure'); END;`,
	); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	_, err := HandleTool(db, "relate", ToolArgs{From: "RollbackFrom", To: "RollbackTo", RelationType: "fails"})
	if err == nil {
		t.Fatalf("HandleTool(relate) expected error from injected failure, got nil")
	}

	var entityCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entities WHERE name IN (?, ?)`, "RollbackFrom", "RollbackTo").Scan(&entityCount); err != nil {
		t.Fatalf("count entities after failed relate: %v", err)
	}
	if entityCount != 0 {
		t.Fatalf("relate endpoint entities persisted despite rollback: count=%d", entityCount)
	}

	var relationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relations WHERE relation_type = ?`, "fails").Scan(&relationCount); err != nil {
		t.Fatalf("count relations after failed relate: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("relation persisted despite rollback: count=%d", relationCount)
	}
}

func TestRelateExistingRelationRemainsIdempotent(t *testing.T) {
	db := newTestDB(t, "relate-idempotent.db")

	firstAny, err := HandleTool(db, "relate", ToolArgs{From: "ExistingFrom", To: "ExistingTo", RelationType: "depends_on"})
	if err != nil {
		t.Fatalf("HandleTool(relate first) error = %v", err)
	}
	first := firstAny.(RelateResult)
	if !first.Created {
		t.Fatalf("first relate Created = false, want true: %#v", first)
	}

	secondAny, err := HandleTool(db, "relate", ToolArgs{From: "ExistingFrom", To: "ExistingTo", RelationType: "depends_on"})
	if err != nil {
		t.Fatalf("HandleTool(relate duplicate) error = %v", err)
	}
	second := secondAny.(RelateResult)
	if second.Created || second.Message != "Relation already exists" {
		t.Fatalf("duplicate relate result = %#v, want Created=false already-exists message", second)
	}

	var relationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relations WHERE relation_type = ?`, "depends_on").Scan(&relationCount); err != nil {
		t.Fatalf("count duplicate relation rows: %v", err)
	}
	if relationCount != 1 {
		t.Fatalf("relationCount = %d, want 1 after duplicate relate", relationCount)
	}
}

// SearchMetrics.Query must match the trimmed query actually executed against
// the index — otherwise two recall calls that only differ by leading or
// trailing whitespace look like distinct queries in telemetry even though
// they ran the same search.
func TestSearchMemoryMetricsQueryIsTrimmed(t *testing.T) {
	db := newTestDB(t, "metrics-trim.db")
	entityID, err := UpsertEntity(db, "TrimProbe", "test")
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if _, err := AddObservation(db, entityID, "trim probe observation", "user", 1.0); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	cases := []struct {
		name, input, want string
	}{
		{"no whitespace", "trim", "trim"},
		{"leading whitespace", "   trim", "trim"},
		{"trailing whitespace", "trim   ", "trim"},
		{"both sides", "  trim  ", "trim"},
		{"whitespace only", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, metrics, err := SearchMemory(db, tc.input, 5, 12)
			if err != nil {
				t.Fatalf("SearchMemory(%q): %v", tc.input, err)
			}
			if metrics.Query != tc.want {
				t.Fatalf("SearchMemory(%q).Metrics.Query = %q, want %q", tc.input, metrics.Query, tc.want)
			}
		})
	}
}
