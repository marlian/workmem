package store

import (
	"database/sql"
	"fmt"
	"strings"
)

type FactInput struct {
	Entity      string   `json:"entity"`
	EntityType  string   `json:"entity_type,omitempty"`
	Observation string   `json:"observation"`
	Source      string   `json:"source,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	EventID     *int64   `json:"event_id,omitempty"`
}

type ToolArgs struct {
	Entity         string      `json:"entity,omitempty"`
	EntityType     string      `json:"entity_type,omitempty"`
	Observation    string      `json:"observation,omitempty"`
	Source         string      `json:"source,omitempty"`
	Confidence     *float64    `json:"confidence,omitempty"`
	EventID        *int64      `json:"event_id,omitempty"`
	Project        string      `json:"project,omitempty"`
	Facts          []FactInput `json:"facts,omitempty"`
	Query          string      `json:"query,omitempty"`
	Limit          *int        `json:"limit,omitempty"`
	Compact        bool        `json:"compact,omitempty"`
	From           string      `json:"from,omitempty"`
	To             string      `json:"to,omitempty"`
	RelationType   string      `json:"relation_type,omitempty"`
	Context        string      `json:"context,omitempty"`
	ObservationID  *int64      `json:"observation_id,omitempty"`
	Label          string      `json:"label,omitempty"`
	EventDate      string      `json:"event_date,omitempty"`
	EventType      string      `json:"event_type,omitempty"`
	ExpiresAt      string      `json:"expires_at,omitempty"`
	Observations   []FactInput `json:"observations,omitempty"`
	ObservationIDs []int64     `json:"observation_ids,omitempty"`
	DateFrom       string      `json:"date_from,omitempty"`
	DateTo         string      `json:"date_to,omitempty"`
}

type RememberResult struct {
	Stored            bool           `json:"stored"`
	EntityID          int64          `json:"entity_id"`
	ObservationID     int64          `json:"observation_id"`
	EventID           *int64         `json:"event_id,omitempty"`
	Project           *string        `json:"project,omitempty"`
	PossibleConflicts []ConflictHint `json:"possible_conflicts,omitempty"`
}

type RememberBatchFactResult struct {
	Entity        string `json:"entity"`
	EntityID      int64  `json:"entity_id"`
	ObservationID int64  `json:"observation_id"`
	EventID       *int64 `json:"event_id,omitempty"`
}

type RememberBatchResult struct {
	Stored  int                       `json:"stored"`
	Facts   []RememberBatchFactResult `json:"facts"`
	Project *string                   `json:"project,omitempty"`
}

type RecallEntityResult struct {
	Found             bool                `json:"found"`
	Message           string              `json:"message,omitempty"`
	Hint              string              `json:"hint,omitempty"`
	Entity            EntityRecord        `json:"entity"`
	Observations      []EntityObservation `json:"observations,omitempty"`
	RelationsOutgoing []RelationOutgoing  `json:"relations_outgoing,omitempty"`
	RelationsIncoming []RelationIncoming  `json:"relations_incoming,omitempty"`
}

type RelateResult struct {
	Created      bool   `json:"created"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	RelationType string `json:"relation_type,omitempty"`
	Message      string `json:"message,omitempty"`
}

type ForgetResult struct {
	Deleted bool   `json:"deleted"`
	Type    string `json:"type,omitempty"`
	ID      int64  `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
}

type ListEntitiesResult struct {
	Entities []ListedEntity `json:"entities"`
	Total    int            `json:"total"`
}

type RememberEventResult struct {
	Created              bool                      `json:"created"`
	EventID              int64                     `json:"event_id"`
	Label                string                    `json:"label"`
	EventDate            string                    `json:"event_date,omitempty"`
	EventType            string                    `json:"event_type,omitempty"`
	ObservationsAttached int                       `json:"observations_attached"`
	Observations         []RememberBatchFactResult `json:"observations,omitempty"`
	Project              *string                   `json:"project,omitempty"`
}

type RecallEventsResult struct {
	Events []EventSearchResult `json:"events"`
	Total  int                 `json:"total"`
	Hint   string              `json:"hint,omitempty"`
}

type RecallEventResult struct {
	Found             bool               `json:"found"`
	Message           string             `json:"message,omitempty"`
	Event             EventRecord        `json:"event"`
	Entities          []EventEntityGroup `json:"entities,omitempty"`
	TotalObservations int                `json:"total_observations,omitempty"`
}

type GetEventObservationsToolResult struct {
	Found        bool                 `json:"found"`
	Message      string               `json:"message,omitempty"`
	Event        EventRecord          `json:"event"`
	Observations []FetchedObservation `json:"observations,omitempty"`
	Total        int                  `json:"total,omitempty"`
}

// HandleTool dispatches a tool call and discards the search metrics (if any).
// Callers that want access to ranking-pipeline metrics for telemetry should
// use HandleToolWithMetrics instead.
func HandleTool(defaultDB *sql.DB, name string, args ToolArgs) (any, error) {
	result, _, err := HandleToolWithMetrics(defaultDB, name, args)
	return result, err
}

// HandleToolWithMetrics dispatches a tool call and returns ranking-pipeline
// metrics when applicable (currently: "recall"). For other tools the metrics
// return is nil.
func HandleToolWithMetrics(defaultDB *sql.DB, name string, args ToolArgs) (any, *SearchMetrics, error) {
	var metrics *SearchMetrics
	result, err := dispatchTool(defaultDB, name, args, &metrics)
	return result, metrics, err
}

func dispatchTool(defaultDB *sql.DB, name string, args ToolArgs, outMetrics **SearchMetrics) (any, error) {
	if err := validateToolArgs(name, args); err != nil {
		return nil, err
	}

	db, err := GetDB(defaultDB, args.Project)
	if err != nil {
		return nil, err
	}
	halfLife := memoryHalfLifeWeeks()
	if args.Project != "" {
		halfLife = projectMemoryHalfLifeWeeks()
	}

	switch name {
	case "remember":
		entityID, err := UpsertEntity(db, args.Entity, args.EntityType)
		if err != nil {
			return nil, err
		}
		// Detect conflicts BEFORE insert so the detector scans an
		// already-consistent state and self-match filtering is
		// unnecessary. See DECISION_LOG 2026-04-22. Pass the
		// scope-aware halfLife computed above so project memory uses
		// its own (longer) decay rate when scoring potential conflicts
		// — see Kimi general-review finding (PR #11 follow-up).
		conflicts, err := DetectEntityConflicts(db, entityID, args.Observation, halfLife)
		if err != nil {
			return nil, err
		}
		observationID, err := AddObservation(db, entityID, args.Observation, defaultSource(args.Source), defaultConfidence(args.Confidence), optionalEventID(args.EventID)...)
		if err != nil {
			return nil, err
		}
		return RememberResult{Stored: true, EntityID: entityID, ObservationID: observationID, EventID: args.EventID, Project: stringPointer(args.Project), PossibleConflicts: conflicts}, nil

	case "remember_batch":
		// Scope decision (DECISION_LOG 2026-04-22): conflict detection is
		// intentionally NOT extended to remember_batch. The batch surface
		// raises its own design questions — within-batch duplicates,
		// per-fact hints in a single response, transaction vs individual
		// detection — that deserve their own decision rather than being
		// smuggled in under this one. Revisit in a future Step if
		// telemetry shows batches dominate writes and silent overwrites
		// inside batches become material.
		tx, err := db.Begin()
		if err != nil {
			return nil, fmt.Errorf("begin remember_batch: %w", err)
		}
		defer tx.Rollback()
		results := make([]RememberBatchFactResult, 0, len(args.Facts))
		for _, fact := range args.Facts {
			entityID, err := UpsertEntity(tx, fact.Entity, fact.EntityType)
			if err != nil {
				return nil, err
			}
			observationID, err := AddObservation(tx, entityID, fact.Observation, defaultSource(fact.Source), defaultConfidence(fact.Confidence), optionalEventID(fact.EventID)...)
			if err != nil {
				return nil, err
			}
			results = append(results, RememberBatchFactResult{Entity: fact.Entity, EntityID: entityID, ObservationID: observationID, EventID: fact.EventID})
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit remember_batch: %w", err)
		}
		return RememberBatchResult{Stored: len(results), Facts: results, Project: stringPointer(args.Project)}, nil

	case "recall":
		limit := 20
		if args.Limit != nil {
			limit = *args.Limit
		}
		results, m, err := SearchMemory(db, args.Query, limit, halfLife)
		if err != nil {
			return nil, err
		}
		m.Compact = args.Compact
		if outMetrics != nil {
			*outMetrics = &m
		}
		return GroupResults(results, args.Compact), nil

	case "recall_entity":
		graph, err := GetEntityGraph(db, args.Entity, halfLife)
		if err != nil {
			return nil, err
		}
		if graph == nil {
			return RecallEntityResult{
				Found:   false,
				Message: fmt.Sprintf("Entity %q not found", args.Entity),
				Hint:    "Use list_entities to browse available entities, or recall with a broader search query.",
			}, nil
		}
		return RecallEntityResult{Found: true, Entity: graph.Entity, Observations: graph.Observations, RelationsOutgoing: graph.RelationsOutgoing, RelationsIncoming: graph.RelationsIncoming}, nil

	case "relate":
		fromID, err := UpsertEntity(db, args.From, "")
		if err != nil {
			return nil, err
		}
		toID, err := UpsertEntity(db, args.To, "")
		if err != nil {
			return nil, err
		}
		_, err = db.Exec(`INSERT INTO relations (from_entity_id, to_entity_id, relation_type, context) VALUES (?, ?, ?, ?)`, fromID, toID, args.RelationType, nullableString(args.Context, ""))
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return RelateResult{Created: false, Message: "Relation already exists"}, nil
			}
			return nil, fmt.Errorf("insert relation: %w", err)
		}
		return RelateResult{Created: true, From: args.From, To: args.To, RelationType: args.RelationType}, nil

	case "forget":
		if args.ObservationID != nil {
			deleted, err := ForgetObservation(db, *args.ObservationID)
			if err != nil {
				return nil, err
			}
			return ForgetResult{Deleted: deleted, Type: "observation", ID: *args.ObservationID}, nil
		}
		if args.Entity != "" {
			deleted, err := ForgetEntity(db, args.Entity)
			if err != nil {
				return nil, err
			}
			if !deleted {
				return ForgetResult{Deleted: false, Message: fmt.Sprintf("Entity %q not found", args.Entity)}, nil
			}
			return ForgetResult{Deleted: true, Type: "entity", Name: args.Entity}, nil
		}
		return ForgetResult{Deleted: false, Message: "Provide observation_id or entity name"}, nil

	case "list_entities":
		limit := 50
		if args.Limit != nil {
			limit = *args.Limit
		}
		entities, err := ListEntities(db, args.EntityType, limit)
		if err != nil {
			return nil, err
		}
		return ListEntitiesResult{Entities: entities, Total: len(entities)}, nil

	case "remember_event":
		// Scope decision (DECISION_LOG 2026-04-22): conflict detection is
		// intentionally NOT extended to observations attached via
		// remember_event. Same reasoning as remember_batch — per-fact
		// hints inside an event payload is a separate design question.
		// A single remember call against an existing entity is the
		// targeted surface for this Step.
		tx, err := db.Begin()
		if err != nil {
			return nil, fmt.Errorf("begin remember_event: %w", err)
		}
		defer tx.Rollback()
		eventID, err := CreateEvent(tx, args.Label, args.EventDate, args.EventType, args.Context, args.ExpiresAt)
		if err != nil {
			return nil, err
		}
		attached := make([]RememberBatchFactResult, 0, len(args.Observations))
		for _, fact := range args.Observations {
			entityID, err := UpsertEntity(tx, fact.Entity, fact.EntityType)
			if err != nil {
				return nil, err
			}
			observationID, err := AddObservation(tx, entityID, fact.Observation, defaultSource(fact.Source), defaultConfidence(fact.Confidence), eventID)
			if err != nil {
				return nil, err
			}
			attached = append(attached, RememberBatchFactResult{Entity: fact.Entity, EntityID: entityID, ObservationID: observationID})
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit remember_event: %w", err)
		}
		return RememberEventResult{Created: true, EventID: eventID, Label: args.Label, EventDate: args.EventDate, EventType: args.EventType, ObservationsAttached: len(attached), Observations: attached, Project: stringPointer(args.Project)}, nil

	case "recall_events":
		events, err := SearchEvents(db, args.Query, args.EventType, args.DateFrom, args.DateTo, resolveLimit(args.Limit, 20))
		if err != nil {
			return nil, err
		}
		hint := "No events found. Try broader search terms or different date range."
		if len(events) > 0 {
			hint = "Use recall_event with an event_id to get the full observation block."
		}
		return RecallEventsResult{Events: events, Total: len(events), Hint: hint}, nil

	case "recall_event":
		if args.EventID == nil {
			return nil, fmt.Errorf("event_id is required")
		}
		result, err := GetFullEvent(db, *args.EventID, halfLife)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return RecallEventResult{Found: false, Message: fmt.Sprintf("Event with id %d not found", *args.EventID)}, nil
		}
		return RecallEventResult{Found: true, Event: result.Event, Entities: result.Entities, TotalObservations: result.TotalObservations}, nil

	case "get_observations":
		return GetObservationsByIDs(db, args.ObservationIDs, halfLife)

	case "get_event_observations":
		if args.EventID == nil {
			return nil, fmt.Errorf("event_id is required")
		}
		result, err := GetEventObservations(db, *args.EventID, halfLife)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return GetEventObservationsToolResult{Found: false, Message: fmt.Sprintf("Event with id %d not found", *args.EventID)}, nil
		}
		return GetEventObservationsToolResult{Found: true, Event: result.Event, Observations: result.Observations, Total: result.Total}, nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func defaultSource(source string) string {
	if source == "" {
		return "user"
	}
	return source
}

func defaultConfidence(value *float64) float64 {
	if value == nil {
		return 1.0
	}
	return *value
}

func optionalEventID(value *int64) []int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	return []int64{*value}
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func resolveLimit(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func validateToolArgs(name string, args ToolArgs) error {
	switch name {
	case "remember":
		if err := validateNonEmptyField("entity", args.Entity); err != nil {
			return err
		}
		if err := validateNonEmptyField("observation", args.Observation); err != nil {
			return err
		}
	case "remember_batch":
		for index, fact := range args.Facts {
			if err := validateFactInput(fact, fmt.Sprintf("facts[%d]", index)); err != nil {
				return err
			}
		}
	case "recall_entity":
		if err := validateNonEmptyField("entity", args.Entity); err != nil {
			return err
		}
	case "relate":
		if err := validateNonEmptyField("from", args.From); err != nil {
			return err
		}
		if err := validateNonEmptyField("to", args.To); err != nil {
			return err
		}
		if err := validateNonEmptyField("relation_type", args.RelationType); err != nil {
			return err
		}
	case "remember_event":
		if err := validateNonEmptyField("label", args.Label); err != nil {
			return err
		}
		for index, fact := range args.Observations {
			if err := validateFactInput(fact, fmt.Sprintf("observations[%d]", index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFactInput(fact FactInput, path string) error {
	if err := validateNonEmptyField(path+".entity", fact.Entity); err != nil {
		return err
	}
	if err := validateNonEmptyField(path+".observation", fact.Observation); err != nil {
		return err
	}
	return nil
}

func validateNonEmptyField(fieldName string, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be non-empty", fieldName)
	}
	return nil
}
