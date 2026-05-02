package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	ReconcileActionProposed                    = "proposed"
	ReconcileRationaleExactDuplicateSameEntity = "exact_duplicate_same_entity"
)

type ReconcileProposeOptions struct {
	GeneratedAt       time.Time
	Since             time.Duration
	SinceLabel        string
	MinObsPerEntity   int
	MaxEntitiesPerRun int
	Scope             string
}

type ReconcileProposeReport struct {
	GeneratedAt        time.Time
	Mode               string
	Scope              string
	Since              time.Duration
	SinceLabel         string
	ScannedEntities    []ReconcileEntitySignal
	DuplicateGroups    []ReconcileDuplicateGroup
	CandidatesProposed int
}

type ReconcileEntitySignal struct {
	EntityID           int64
	Name               string
	EntityType         string
	ActiveObservations int
	RecentObservations int
	LastObservationAt  string
}

type ReconcileDuplicateGroup struct {
	EntityID   int64
	EntityName string
	EntityType string
	Content    string
	Action     string
	Rationale  string
	Target     ReconcileObservation
	Sources    []ReconcileObservation
}

type ReconcileObservation struct {
	ID         int64
	Source     string
	Confidence float64
	EventID    *int64
	CreatedAt  string
}

func BuildReconcileProposeReport(db *sql.DB, options ReconcileProposeOptions) (*ReconcileProposeReport, error) {
	if db == nil {
		return nil, fmt.Errorf("reconcile propose: nil db")
	}
	now := options.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := options.Since
	if since <= 0 {
		since = 30 * 24 * time.Hour
	}
	sinceLabel := strings.TrimSpace(options.SinceLabel)
	if sinceLabel == "" {
		sinceLabel = since.String()
	}
	minObsPerEntity := options.MinObsPerEntity
	if minObsPerEntity <= 0 {
		minObsPerEntity = 2
	}
	maxEntitiesPerRun := options.MaxEntitiesPerRun
	if maxEntitiesPerRun <= 0 {
		maxEntitiesPerRun = 50
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		scope = "global"
	}

	asOf := now.UTC().Format(sqliteTimestampLayout)
	cutoff := now.Add(-since).UTC().Format(sqliteTimestampLayout)
	signals, err := selectReconcileEntitySignals(db, cutoff, asOf, minObsPerEntity, maxEntitiesPerRun)
	if err != nil {
		return nil, err
	}
	groups, candidates, err := selectExactDuplicateGroups(db, signals, asOf)
	if err != nil {
		return nil, err
	}

	return &ReconcileProposeReport{
		GeneratedAt:        now.UTC(),
		Mode:               "propose",
		Scope:              scope,
		Since:              since,
		SinceLabel:         sinceLabel,
		ScannedEntities:    signals,
		DuplicateGroups:    groups,
		CandidatesProposed: candidates,
	}, nil
}

func selectReconcileEntitySignals(db *sql.DB, cutoff string, asOf string, minObsPerEntity int, maxEntitiesPerRun int) ([]ReconcileEntitySignal, error) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT e.id, e.name, e.entity_type,
		       COUNT(o.id) AS active_observations,
		       SUM(CASE WHEN datetime(o.created_at) >= datetime(?) THEN 1 ELSE 0 END) AS recent_observations,
		       MAX(o.created_at) AS last_observation_at
		FROM observations o
		JOIN entities e ON e.id = o.entity_id
		WHERE e.deleted_at IS NULL AND %s
		GROUP BY e.id
		HAVING COUNT(o.id) >= ? AND SUM(CASE WHEN datetime(o.created_at) >= datetime(?) THEN 1 ELSE 0 END) > 0
		ORDER BY datetime(last_observation_at) DESC, e.id DESC
		LIMIT ?
	`, activeObservationAsOfSQL("o")), cutoff, asOf, minObsPerEntity, cutoff, maxEntitiesPerRun)
	if err != nil {
		return nil, fmt.Errorf("select reconcile entity signals: %w", err)
	}
	defer rows.Close()

	signals := make([]ReconcileEntitySignal, 0)
	for rows.Next() {
		var signal ReconcileEntitySignal
		var entityType sql.NullString
		if err := rows.Scan(&signal.EntityID, &signal.Name, &entityType, &signal.ActiveObservations, &signal.RecentObservations, &signal.LastObservationAt); err != nil {
			return nil, fmt.Errorf("scan reconcile entity signal: %w", err)
		}
		signal.EntityType = nullableStringValue(entityType)
		signals = append(signals, signal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reconcile entity signals: %w", err)
	}
	return signals, nil
}

func selectExactDuplicateGroups(db *sql.DB, signals []ReconcileEntitySignal, asOf string) ([]ReconcileDuplicateGroup, int, error) {
	if len(signals) == 0 {
		return nil, 0, nil
	}
	entityIDs := make([]int64, 0, len(signals))
	entitySignals := make(map[int64]ReconcileEntitySignal, len(signals))
	for _, signal := range signals {
		entityIDs = append(entityIDs, signal.EntityID)
		entitySignals[signal.EntityID] = signal
	}
	placeholders := placeholders(len(entityIDs))
	args := make([]any, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		args = append(args, entityID)
	}
	args = append(args, asOf, asOf)

	rows, err := db.Query(fmt.Sprintf(`
		WITH duplicate_groups AS (
			SELECT o.entity_id, o.content
			FROM observations o
			JOIN entities e ON e.id = o.entity_id
			WHERE e.deleted_at IS NULL
			  AND o.entity_id IN (%s)
			  AND %s
			GROUP BY o.entity_id, o.content
			HAVING COUNT(o.id) > 1
		)
		SELECT o.id, o.entity_id, o.content, o.source, o.confidence, o.event_id, o.created_at
		FROM observations o
		JOIN duplicate_groups dg ON dg.entity_id = o.entity_id AND dg.content = o.content
		JOIN entities e ON e.id = o.entity_id
		WHERE e.deleted_at IS NULL AND %s
		ORDER BY o.entity_id, o.content, datetime(o.created_at) DESC, o.id DESC
	`, placeholders, activeObservationAsOfSQL("o"), activeObservationAsOfSQL("o")), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("select exact duplicate observations: %w", err)
	}
	defer rows.Close()

	groups := make([]ReconcileDuplicateGroup, 0)
	var currentEntityID int64
	var currentContent string
	var hasCurrent bool
	var current *ReconcileDuplicateGroup
	for rows.Next() {
		var entityID int64
		var content string
		var source sql.NullString
		var confidence sql.NullFloat64
		var eventID sql.NullInt64
		var observation ReconcileObservation
		if err := rows.Scan(&observation.ID, &entityID, &content, &source, &confidence, &eventID, &observation.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan exact duplicate observation: %w", err)
		}
		observation.Source = nullableStringValue(source)
		if confidence.Valid {
			observation.Confidence = confidence.Float64
		}
		if eventID.Valid {
			value := eventID.Int64
			observation.EventID = &value
		}

		if !hasCurrent || entityID != currentEntityID || content != currentContent {
			if current != nil {
				groups = append(groups, *current)
			}
			signal := entitySignals[entityID]
			hasCurrent = true
			currentEntityID = entityID
			currentContent = content
			current = &ReconcileDuplicateGroup{
				EntityID:   entityID,
				EntityName: signal.Name,
				EntityType: signal.EntityType,
				Content:    content,
				Action:     ReconcileActionProposed,
				Rationale:  ReconcileRationaleExactDuplicateSameEntity,
				Target:     observation,
				Sources:    make([]ReconcileObservation, 0, 1),
			}
			continue
		}
		current.Sources = append(current.Sources, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate exact duplicate observations: %w", err)
	}
	if current != nil {
		groups = append(groups, *current)
	}

	candidates := 0
	for _, group := range groups {
		candidates += len(group.Sources)
	}
	return groups, candidates, nil
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
