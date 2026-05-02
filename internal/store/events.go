package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const sqliteTimestampLayout = "2006-01-02 15:04:05"

func activeEventSQL(alias string) string {
	return fmt.Sprintf(`(%s.expires_at IS NULL OR datetime(%s.expires_at) > CURRENT_TIMESTAMP)`, alias, alias)
}

func activeObservationSQL(alias string) string {
	return fmt.Sprintf(`%s.deleted_at IS NULL AND %s.superseded_by IS NULL AND (
		%s.event_id IS NULL OR EXISTS (
			SELECT 1 FROM events ev_active
			WHERE ev_active.id = %s.event_id AND %s
		)
	)`, alias, alias, alias, alias, activeEventSQL("ev_active"))
}

func normalizeExpiresAt(value string) (any, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999",
		sqliteTimestampLayout,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		var parsed time.Time
		var err error
		if layout == time.RFC3339Nano {
			parsed, err = time.Parse(layout, trimmed)
		} else {
			parsed, err = time.ParseInLocation(layout, trimmed, time.UTC)
		}
		if err == nil {
			return parsed.UTC().Format(sqliteTimestampLayout), nil
		}
	}
	return nil, fmt.Errorf("expires_at must be RFC3339 or SQLite timestamp, got %q", value)
}

type EntityRecord struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	DeletedAt  string `json:"deleted_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type EntityObservation struct {
	ID                  int64   `json:"id"`
	EntityID            int64   `json:"entity_id"`
	Content             string  `json:"content"`
	Source              string  `json:"source"`
	Confidence          float64 `json:"confidence"`
	AccessCount         int64   `json:"access_count"`
	LastAccessed        string  `json:"last_accessed,omitempty"`
	DeletedAt           string  `json:"deleted_at,omitempty"`
	CreatedAt           string  `json:"created_at"`
	EventID             *int64  `json:"event_id,omitempty"`
	EventLabel          string  `json:"event_label,omitempty"`
	EventDate           string  `json:"event_date,omitempty"`
	EventType           string  `json:"ev_type,omitempty"`
	EffectiveConfidence float64 `json:"effective_confidence"`
	EntityType          string  `json:"entity_type,omitempty"`
}

type RelationOutgoing struct {
	ID           int64  `json:"id"`
	FromEntityID int64  `json:"from_entity_id"`
	ToEntityID   int64  `json:"to_entity_id"`
	RelationType string `json:"relation_type"`
	Context      string `json:"context,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	TargetName   string `json:"target_name"`
	TargetType   string `json:"target_type,omitempty"`
}

type RelationIncoming struct {
	ID           int64  `json:"id"`
	FromEntityID int64  `json:"from_entity_id"`
	ToEntityID   int64  `json:"to_entity_id"`
	RelationType string `json:"relation_type"`
	Context      string `json:"context,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	SourceName   string `json:"source_name"`
	SourceType   string `json:"source_type,omitempty"`
}

type EntityGraph struct {
	Entity            EntityRecord        `json:"entity"`
	Observations      []EntityObservation `json:"observations"`
	RelationsOutgoing []RelationOutgoing  `json:"relations_outgoing"`
	RelationsIncoming []RelationIncoming  `json:"relations_incoming"`
}

type ListedEntity struct {
	EntityRecord
	ObservationCount int64 `json:"observation_count"`
}

type EventRecord struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	EventDate string `json:"event_date,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Context   string `json:"context,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type EventObservation struct {
	ID          int64   `json:"id"`
	Content     string  `json:"content"`
	Confidence  float64 `json:"confidence"`
	Source      string  `json:"source"`
	AccessCount int64   `json:"access_count"`
	CreatedAt   string  `json:"created_at"`
}

type EventEntityGroup struct {
	EntityName   string             `json:"entity_name"`
	EntityType   string             `json:"entity_type"`
	Observations []EventObservation `json:"observations"`
}

type FullEventResult struct {
	Event             EventRecord        `json:"event"`
	Entities          []EventEntityGroup `json:"entities"`
	TotalObservations int                `json:"total_observations"`
}

type EventSearchResult struct {
	EventRecord
	ObservationCount int64 `json:"observation_count"`
}

type GetObservationsResult struct {
	Observations []FetchedObservation `json:"observations"`
	Total        int                  `json:"total"`
	Requested    int                  `json:"requested"`
}

type FetchedObservation struct {
	ID                  int64   `json:"id"`
	EntityID            int64   `json:"entity_id"`
	EntityName          string  `json:"entity_name"`
	EntityType          string  `json:"entity_type,omitempty"`
	Content             string  `json:"content"`
	Source              string  `json:"source"`
	StoredConfidence    float64 `json:"stored_confidence"`
	Confidence          float64 `json:"confidence"`
	EffectiveConfidence float64 `json:"effective_confidence"`
	AccessCount         int64   `json:"access_count"`
	LastAccessed        string  `json:"last_accessed,omitempty"`
	DeletedAt           string  `json:"deleted_at,omitempty"`
	CreatedAt           string  `json:"created_at"`
	EventID             *int64  `json:"event_id,omitempty"`
	EventLabel          string  `json:"event_label,omitempty"`
	EventDate           string  `json:"event_date,omitempty"`
	EventType           string  `json:"ev_type,omitempty"`
}

type EventObservationsResult struct {
	Event        EventRecord          `json:"event"`
	Observations []FetchedObservation `json:"observations"`
	Total        int                  `json:"total"`
}

func CreateEvent(db dbtx, label, eventDate, eventType, context, expiresAt string) (int64, error) {
	normalizedExpiresAt, err := normalizeExpiresAt(expiresAt)
	if err != nil {
		return 0, err
	}
	result, err := db.Exec(
		`INSERT INTO events (label, event_date, event_type, context, expires_at) VALUES (?, ?, ?, ?, ?)`,
		label,
		nullableString(eventDate, ""),
		nullableString(eventType, ""),
		nullableString(context, ""),
		normalizedExpiresAt,
	)
	if err != nil {
		return 0, fmt.Errorf("create event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("event last insert id: %w", err)
	}
	return id, nil
}

func SearchEvents(db *sql.DB, query string, eventType string, dateFrom string, dateTo string, limit int) ([]EventSearchResult, error) {
	if limit <= 0 {
		limit = 20
	}

	sqlQuery := fmt.Sprintf(`SELECT e.id, e.label, e.event_date, e.event_type, e.context, e.expires_at, e.created_at, COUNT(oe.id) AS observation_count
		FROM events e
		LEFT JOIN observations o ON o.event_id = e.id AND %s
		LEFT JOIN entities oe ON oe.id = o.entity_id AND oe.deleted_at IS NULL
		WHERE %s`, activeObservationSQL("o"), activeEventSQL("e"))
	args := make([]any, 0, 5)
	if query != "" {
		sqlQuery += ` AND e.label LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLikePattern(query)+"%")
	}
	if eventType != "" {
		sqlQuery += ` AND e.event_type = ?`
		args = append(args, eventType)
	}
	if dateFrom != "" {
		sqlQuery += ` AND e.event_date >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		sqlQuery += ` AND e.event_date <= ?`
		args = append(args, dateTo)
	}
	sqlQuery += ` GROUP BY e.id ORDER BY e.event_date DESC, e.created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	defer rows.Close()

	results := make([]EventSearchResult, 0)
	for rows.Next() {
		item, err := scanEventSearchResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return results, nil
}

func GetFullEvent(db *sql.DB, eventID int64, halfLifeWeeks float64) (*FullEventResult, error) {
	event, err := getEventByID(db, eventID)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, nil
	}

	rows, err := db.Query(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.content, o.source, o.confidence, o.access_count, o.created_at,
		       e.name, e.entity_type
		FROM observations o
		JOIN entities e ON o.entity_id = e.id
		WHERE o.event_id = ? AND %s AND e.deleted_at IS NULL
		ORDER BY o.created_at ASC
	`, activeObservationSQL("o")), eventID)
	if err != nil {
		return nil, fmt.Errorf("query full event observations: %w", err)
	}
	defer rows.Close()

	groupIndexes := map[string]int{}
	groups := make([]EventEntityGroup, 0)
	touchIDs := make([]int64, 0)
	for rows.Next() {
		var observationID int64
		var entityID int64
		var content string
		var source string
		var confidence float64
		var accessCount int64
		var createdAt string
		var entityName string
		var entityType sql.NullString
		if err := rows.Scan(&observationID, &entityID, &content, &source, &confidence, &accessCount, &createdAt, &entityName, &entityType); err != nil {
			return nil, fmt.Errorf("scan full event observation: %w", err)
		}
		index, exists := groupIndexes[entityName]
		if !exists {
			index = len(groups)
			groupIndexes[entityName] = index
			groups = append(groups, EventEntityGroup{EntityName: entityName, EntityType: entityType.String})
		}
		groups[index].Observations = append(groups[index].Observations, EventObservation{
			ID:          observationID,
			Content:     content,
			Confidence:  DecayedConfidence(createdAt, confidence, accessCount, halfLifeWeeks),
			Source:      source,
			AccessCount: accessCount,
			CreatedAt:   createdAt,
		})
		touchIDs = append(touchIDs, observationID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate full event observations: %w", err)
	}
	if err := TouchObservations(db, touchIDs); err != nil {
		return nil, err
	}

	return &FullEventResult{Event: *event, Entities: groups, TotalObservations: len(touchIDs)}, nil
}

func GetObservationsByIDs(db *sql.DB, observationIDs []int64, halfLifeWeeks float64) (GetObservationsResult, error) {
	orderedIDs := uniquePositiveIDs(observationIDs)
	if len(orderedIDs) == 0 {
		return GetObservationsResult{Observations: []FetchedObservation{}, Total: 0, Requested: 0}, nil
	}

	byID := make(map[int64]FetchedObservation, len(orderedIDs))
	const chunkSize = 900
	for start := 0; start < len(orderedIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(orderedIDs) {
			end = len(orderedIDs)
		}
		chunk := orderedIDs[start:end]
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := db.Query(fmt.Sprintf(`
			SELECT o.id, o.entity_id, o.entity_type, o.content, o.source, o.confidence, o.access_count, o.last_accessed, o.deleted_at, o.created_at,
			       o.event_id, e.name, ev.label, ev.event_date, ev.event_type
			FROM observations o
			JOIN entities e ON o.entity_id = e.id
			LEFT JOIN events ev ON o.event_id = ev.id
			WHERE o.id IN (%s) AND %s AND e.deleted_at IS NULL
		`, placeholders(len(chunk)), activeObservationSQL("o")), args...)
		if err != nil {
			return GetObservationsResult{}, fmt.Errorf("query observations by ids: %w", err)
		}
		for rows.Next() {
			item, err := scanFetchedObservation(rows)
			if err != nil {
				rows.Close()
				return GetObservationsResult{}, err
			}
			item.StoredConfidence = item.Confidence
			item.Confidence = DecayedConfidence(item.CreatedAt, item.StoredConfidence, item.AccessCount, halfLifeWeeks)
			item.EffectiveConfidence = item.Confidence
			byID[item.ID] = item
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return GetObservationsResult{}, fmt.Errorf("iterate observations by ids: %w", err)
		}
		rows.Close()
	}

	observations := make([]FetchedObservation, 0, len(orderedIDs))
	touchIDs := make([]int64, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if item, ok := byID[id]; ok {
			observations = append(observations, item)
			touchIDs = append(touchIDs, id)
		}
	}
	if err := TouchObservations(db, touchIDs); err != nil {
		return GetObservationsResult{}, err
	}

	return GetObservationsResult{Observations: observations, Total: len(observations), Requested: len(orderedIDs)}, nil
}

func GetEventObservations(db *sql.DB, eventID int64, halfLifeWeeks float64) (*EventObservationsResult, error) {
	event, err := getEventByID(db, eventID)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, nil
	}

	rows, err := db.Query(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.entity_type, o.content, o.source, o.confidence, o.access_count, o.last_accessed, o.deleted_at, o.created_at,
		       o.event_id, e.name, ev.label, ev.event_date, ev.event_type
		FROM observations o
		JOIN entities e ON o.entity_id = e.id
		LEFT JOIN events ev ON o.event_id = ev.id
		WHERE o.event_id = ? AND %s AND e.deleted_at IS NULL
		ORDER BY o.created_at ASC, o.id ASC
	`, activeObservationSQL("o")), eventID)
	if err != nil {
		return nil, fmt.Errorf("query event observations: %w", err)
	}
	defer rows.Close()

	observations := make([]FetchedObservation, 0)
	touchIDs := make([]int64, 0)
	for rows.Next() {
		item, err := scanFetchedObservation(rows)
		if err != nil {
			return nil, err
		}
		item.StoredConfidence = item.Confidence
		item.Confidence = DecayedConfidence(item.CreatedAt, item.StoredConfidence, item.AccessCount, halfLifeWeeks)
		item.EffectiveConfidence = item.Confidence
		observations = append(observations, item)
		touchIDs = append(touchIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event observations: %w", err)
	}
	if err := TouchObservations(db, touchIDs); err != nil {
		return nil, err
	}

	return &EventObservationsResult{Event: *event, Observations: observations, Total: len(observations)}, nil
}

func GetEntityGraph(db *sql.DB, entityName string, halfLifeWeeks float64) (*EntityGraph, error) {
	var entity EntityRecord
	var entityType sql.NullString
	var deletedAt sql.NullString
	err := db.QueryRow(
		`SELECT id, name, entity_type, deleted_at, created_at, updated_at FROM entities WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`,
		entityName,
	).Scan(&entity.ID, &entity.Name, &entityType, &deletedAt, &entity.CreatedAt, &entity.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query entity graph entity: %w", err)
	}
	entity.EntityType = entityType.String
	entity.DeletedAt = deletedAt.String

	obsRows, err := db.Query(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.entity_type, o.content, o.source, o.confidence, o.access_count, o.last_accessed, o.deleted_at, o.created_at,
		       o.event_id, ev.label, ev.event_date, ev.event_type
		FROM observations o
		LEFT JOIN events ev ON o.event_id = ev.id
		WHERE o.entity_id = ? AND %s
		ORDER BY o.created_at DESC
	`, activeObservationSQL("o")), entity.ID)
	if err != nil {
		return nil, fmt.Errorf("query entity observations: %w", err)
	}
	defer obsRows.Close()

	observations := make([]EntityObservation, 0)
	touchIDs := make([]int64, 0)
	for obsRows.Next() {
		item, err := scanEntityObservation(obsRows)
		if err != nil {
			return nil, err
		}
		item.EffectiveConfidence = DecayedConfidence(item.CreatedAt, item.Confidence, item.AccessCount, halfLifeWeeks)
		observations = append(observations, item)
		touchIDs = append(touchIDs, item.ID)
	}
	if err := obsRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entity observations: %w", err)
	}
	if err := TouchObservations(db, touchIDs); err != nil {
		return nil, err
	}

	outgoing, err := queryRelationsOutgoing(db, entity.ID)
	if err != nil {
		return nil, err
	}
	incoming, err := queryRelationsIncoming(db, entity.ID)
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 && len(outgoing) == 0 && len(incoming) == 0 {
		return nil, nil
	}

	return &EntityGraph{
		Entity:            entity,
		Observations:      observations,
		RelationsOutgoing: outgoing,
		RelationsIncoming: incoming,
	}, nil
}

func ListEntities(db *sql.DB, entityType string, limit int) ([]ListedEntity, error) {
	if limit == 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	query := fmt.Sprintf(`
		WITH observation_counts AS (
			SELECT o.entity_id, COUNT(o.id) AS observation_count
			FROM observations o
			WHERE %s
			GROUP BY o.entity_id
		), relation_counts AS (
			SELECT entity_id, SUM(relation_count) AS relation_count
			FROM (
				SELECT r.from_entity_id AS entity_id, COUNT(r.id) AS relation_count
				FROM relations r
				JOIN entities src ON src.id = r.from_entity_id AND src.deleted_at IS NULL
				JOIN entities dst ON dst.id = r.to_entity_id AND dst.deleted_at IS NULL
				GROUP BY r.from_entity_id
				UNION ALL
				SELECT r.to_entity_id AS entity_id, COUNT(r.id) AS relation_count
				FROM relations r
				JOIN entities src ON src.id = r.from_entity_id AND src.deleted_at IS NULL
				JOIN entities dst ON dst.id = r.to_entity_id AND dst.deleted_at IS NULL
				GROUP BY r.to_entity_id
			) grouped_relations
			GROUP BY entity_id
		)
		SELECT e.id, e.name, e.entity_type, e.deleted_at, e.created_at, e.updated_at,
		       COALESCE(oc.observation_count, 0) AS observation_count
		FROM entities e
		LEFT JOIN observation_counts oc ON oc.entity_id = e.id
		LEFT JOIN relation_counts rc ON rc.entity_id = e.id
		WHERE e.deleted_at IS NULL%s
		  AND (COALESCE(oc.observation_count, 0) > 0 OR COALESCE(rc.relation_count, 0) > 0)
		ORDER BY e.updated_at DESC LIMIT ?
	`, activeObservationSQL("o"), nullableEntityTypeFilter(entityType))
	if entityType != "" {
		rows, err = db.Query(query, entityType, limit)
	} else {
		rows, err = db.Query(query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close()

	results := make([]ListedEntity, 0)
	for rows.Next() {
		var item ListedEntity
		var deletedAt sql.NullString
		var entityTypeValue sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &entityTypeValue, &deletedAt, &item.CreatedAt, &item.UpdatedAt, &item.ObservationCount); err != nil {
			return nil, fmt.Errorf("scan listed entity: %w", err)
		}
		item.EntityType = entityTypeValue.String
		item.DeletedAt = deletedAt.String
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed entities: %w", err)
	}
	return results, nil
}

func nullableEntityTypeFilter(entityType string) string {
	if entityType == "" {
		return ""
	}
	return " AND e.entity_type = ?"
}

func getEventByID(db *sql.DB, eventID int64) (*EventRecord, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT id, label, event_date, event_type, context, expires_at, created_at FROM events WHERE id = ? AND %s`, activeEventSQL("events")), eventID)
	if err != nil {
		return nil, fmt.Errorf("query event by id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanEventRecord(rows)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func scanEventRecord(rows *sql.Rows) (*EventRecord, error) {
	var item EventRecord
	var eventDate sql.NullString
	var eventType sql.NullString
	var context sql.NullString
	var expiresAt sql.NullString
	if err := rows.Scan(&item.ID, &item.Label, &eventDate, &eventType, &context, &expiresAt, &item.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan event record: %w", err)
	}
	item.EventDate = eventDate.String
	item.EventType = eventType.String
	item.Context = context.String
	item.ExpiresAt = expiresAt.String
	return &item, nil
}

func scanEventSearchResult(rows *sql.Rows) (EventSearchResult, error) {
	var item EventSearchResult
	var eventDate sql.NullString
	var eventType sql.NullString
	var context sql.NullString
	var expiresAt sql.NullString
	var observationCount int64
	if err := rows.Scan(&item.ID, &item.Label, &eventDate, &eventType, &context, &expiresAt, &item.CreatedAt, &observationCount); err != nil {
		return EventSearchResult{}, fmt.Errorf("scan event search result: %w", err)
	}
	item.EventDate = eventDate.String
	item.EventType = eventType.String
	item.Context = context.String
	item.ExpiresAt = expiresAt.String
	item.ObservationCount = observationCount
	return item, nil
}

func scanFetchedObservation(rows *sql.Rows) (FetchedObservation, error) {
	var item FetchedObservation
	var entityType sql.NullString
	var lastAccessed sql.NullString
	var deletedAt sql.NullString
	var eventID sql.NullInt64
	var eventLabel sql.NullString
	var eventDate sql.NullString
	var eventType sql.NullString
	if err := rows.Scan(
		&item.ID,
		&item.EntityID,
		&entityType,
		&item.Content,
		&item.Source,
		&item.Confidence,
		&item.AccessCount,
		&lastAccessed,
		&deletedAt,
		&item.CreatedAt,
		&eventID,
		&item.EntityName,
		&eventLabel,
		&eventDate,
		&eventType,
	); err != nil {
		return FetchedObservation{}, fmt.Errorf("scan fetched observation: %w", err)
	}
	item.EntityType = entityType.String
	item.LastAccessed = lastAccessed.String
	item.DeletedAt = deletedAt.String
	if eventID.Valid {
		id := eventID.Int64
		item.EventID = &id
	}
	item.EventLabel = eventLabel.String
	item.EventDate = eventDate.String
	item.EventType = eventType.String
	return item, nil
}

func scanEntityObservation(rows *sql.Rows) (EntityObservation, error) {
	var item EntityObservation
	var entityType sql.NullString
	var lastAccessed sql.NullString
	var deletedAt sql.NullString
	var eventID sql.NullInt64
	var eventLabel sql.NullString
	var eventDate sql.NullString
	var eventType sql.NullString
	if err := rows.Scan(
		&item.ID,
		&item.EntityID,
		&entityType,
		&item.Content,
		&item.Source,
		&item.Confidence,
		&item.AccessCount,
		&lastAccessed,
		&deletedAt,
		&item.CreatedAt,
		&eventID,
		&eventLabel,
		&eventDate,
		&eventType,
	); err != nil {
		return EntityObservation{}, fmt.Errorf("scan entity observation: %w", err)
	}
	item.EntityType = entityType.String
	item.LastAccessed = lastAccessed.String
	item.DeletedAt = deletedAt.String
	if eventID.Valid {
		id := eventID.Int64
		item.EventID = &id
	}
	item.EventLabel = eventLabel.String
	item.EventDate = eventDate.String
	item.EventType = eventType.String
	return item, nil
}

func queryRelationsOutgoing(db *sql.DB, entityID int64) ([]RelationOutgoing, error) {
	rows, err := db.Query(`
		SELECT r.id, r.from_entity_id, r.to_entity_id, r.relation_type, r.context, r.created_at, e.name, e.entity_type
		FROM relations r JOIN entities e ON r.to_entity_id = e.id
		WHERE r.from_entity_id = ? AND e.deleted_at IS NULL
	`, entityID)
	if err != nil {
		return nil, fmt.Errorf("query outgoing relations: %w", err)
	}
	defer rows.Close()
	results := make([]RelationOutgoing, 0)
	for rows.Next() {
		var item RelationOutgoing
		var context sql.NullString
		var targetType sql.NullString
		if err := rows.Scan(&item.ID, &item.FromEntityID, &item.ToEntityID, &item.RelationType, &context, &item.CreatedAt, &item.TargetName, &targetType); err != nil {
			return nil, fmt.Errorf("scan outgoing relation: %w", err)
		}
		item.Context = context.String
		item.TargetType = targetType.String
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outgoing relations: %w", err)
	}
	return results, nil
}

func queryRelationsIncoming(db *sql.DB, entityID int64) ([]RelationIncoming, error) {
	rows, err := db.Query(`
		SELECT r.id, r.from_entity_id, r.to_entity_id, r.relation_type, r.context, r.created_at, e.name, e.entity_type
		FROM relations r JOIN entities e ON r.from_entity_id = e.id
		WHERE r.to_entity_id = ? AND e.deleted_at IS NULL
	`, entityID)
	if err != nil {
		return nil, fmt.Errorf("query incoming relations: %w", err)
	}
	defer rows.Close()
	results := make([]RelationIncoming, 0)
	for rows.Next() {
		var item RelationIncoming
		var context sql.NullString
		var sourceType sql.NullString
		if err := rows.Scan(&item.ID, &item.FromEntityID, &item.ToEntityID, &item.RelationType, &context, &item.CreatedAt, &item.SourceName, &sourceType); err != nil {
			return nil, fmt.Errorf("scan incoming relation: %w", err)
		}
		item.Context = context.String
		item.SourceType = sourceType.String
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incoming relations: %w", err)
	}
	return results, nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := map[int64]struct{}{}
	ordered := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	return ordered
}
