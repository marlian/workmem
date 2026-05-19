package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type EmbeddingCacheKey struct {
	Provider    string
	EndpointKey string
	ModelID     string
	Dimensions  int
}

type SemanticObservationSelectOptions struct {
	GeneratedAt       time.Time
	Since             time.Duration
	SinceLabel        string
	MinObsPerEntity   int
	MaxEntitiesPerRun int
}

type SemanticObservation struct {
	ID         int64
	EntityID   int64
	EntityName string
	EntityType string
	Content    string
	Source     string
	Confidence float64
	EventID    *int64
	CreatedAt  string
}

func SelectSemanticReconcileObservations(db dbtx, options SemanticObservationSelectOptions) ([]ReconcileEntitySignal, []SemanticObservation, error) {
	if db == nil {
		return nil, nil, fmt.Errorf("semantic reconcile observations: nil db")
	}
	now := options.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := options.Since
	if since <= 0 {
		since = 30 * 24 * time.Hour
	}
	minObsPerEntity := options.MinObsPerEntity
	if minObsPerEntity <= 0 {
		minObsPerEntity = 2
	}
	maxEntitiesPerRun := options.MaxEntitiesPerRun
	if maxEntitiesPerRun <= 0 {
		maxEntitiesPerRun = 50
	}
	if maxEntitiesPerRun > ReconcileMaxEntitiesPerRunLimit {
		return nil, nil, fmt.Errorf("semantic reconcile: max entities per run %d exceeds limit %d", maxEntitiesPerRun, ReconcileMaxEntitiesPerRunLimit)
	}
	asOf := now.UTC().Format(sqliteTimestampLayout)
	cutoff := now.Add(-since).UTC().Format(sqliteTimestampLayout)
	signals, err := selectReconcileEntitySignals(db, cutoff, asOf, minObsPerEntity, maxEntitiesPerRun)
	if err != nil {
		return nil, nil, err
	}
	observations, err := selectSemanticObservationsForSignals(db, signals, asOf)
	if err != nil {
		return nil, nil, err
	}
	return signals, observations, nil
}

func LoadObservationEmbeddings(db dbtx, observationIDs []int64, key EmbeddingCacheKey) (map[int64][]byte, error) {
	if db == nil {
		return nil, fmt.Errorf("load observation embeddings: nil db")
	}
	if err := validateEmbeddingCacheKey(key); err != nil {
		return nil, err
	}
	result := make(map[int64][]byte)
	if len(observationIDs) == 0 {
		return result, nil
	}
	for start := 0; start < len(observationIDs); start += sqliteVariableChunkSize {
		end := start + sqliteVariableChunkSize
		if end > len(observationIDs) {
			end = len(observationIDs)
		}
		chunk := observationIDs[start:end]
		args := make([]any, 0, len(chunk)+4)
		for _, observationID := range chunk {
			args = append(args, observationID)
		}
		args = append(args, strings.TrimSpace(key.Provider), strings.TrimSpace(key.EndpointKey), strings.TrimSpace(key.ModelID), key.Dimensions)
		rows, err := db.Query(fmt.Sprintf(`
			SELECT observation_id, embedding
			FROM observation_embeddings
			WHERE observation_id IN (%s)
			  AND provider = ?
			  AND endpoint_key = ?
			  AND model_id = ?
			  AND dimensions = ?
		`, placeholders(len(chunk))), args...)
		if err != nil {
			return nil, fmt.Errorf("load observation embeddings: %w", err)
		}
		for rows.Next() {
			var observationID int64
			var blob []byte
			if err := rows.Scan(&observationID, &blob); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan observation embedding: %w", err)
			}
			result[observationID] = append([]byte(nil), blob...)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate observation embeddings: %w", err)
		}
		rows.Close()
	}
	return result, nil
}

func UpsertObservationEmbedding(db dbtx, observationID int64, key EmbeddingCacheKey, blob []byte) error {
	if db == nil {
		return fmt.Errorf("upsert observation embedding: nil db")
	}
	if observationID <= 0 {
		return fmt.Errorf("upsert observation embedding: observation id must be > 0")
	}
	if err := validateEmbeddingCacheKey(key); err != nil {
		return err
	}
	if len(blob) == 0 {
		return fmt.Errorf("upsert observation embedding: embedding blob is empty")
	}
	if _, err := db.Exec(`
		INSERT INTO observation_embeddings (observation_id, provider, endpoint_key, model_id, dimensions, embedding)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(observation_id, provider, endpoint_key, model_id, dimensions)
		DO UPDATE SET embedding = excluded.embedding
	`, observationID, strings.TrimSpace(key.Provider), strings.TrimSpace(key.EndpointKey), strings.TrimSpace(key.ModelID), key.Dimensions, blob); err != nil {
		return fmt.Errorf("upsert observation embedding: %w", err)
	}
	return nil
}

func selectSemanticObservationsForSignals(db dbtx, signals []ReconcileEntitySignal, asOf string) ([]SemanticObservation, error) {
	if len(signals) == 0 {
		return nil, nil
	}
	entityIDs := make([]int64, 0, len(signals))
	for _, signal := range signals {
		entityIDs = append(entityIDs, signal.EntityID)
	}
	args := make([]any, 0, len(entityIDs)+1)
	for _, entityID := range entityIDs {
		args = append(args, entityID)
	}
	args = append(args, asOf)
	rows, err := db.Query(fmt.Sprintf(`
		SELECT o.id, o.entity_id, e.name, o.entity_type, o.content, o.source, o.confidence, o.event_id, o.created_at
		FROM observations o
		JOIN entities e ON e.id = o.entity_id
		WHERE e.deleted_at IS NULL
		  AND o.entity_id IN (%s)
		  AND %s
		ORDER BY o.entity_id, datetime(o.created_at) DESC, o.id DESC
	`, placeholders(len(entityIDs)), activeObservationAsOfSQL("o")), args...)
	if err != nil {
		return nil, fmt.Errorf("select semantic observations: %w", err)
	}
	defer rows.Close()
	observations := make([]SemanticObservation, 0)
	for rows.Next() {
		var observation SemanticObservation
		var entityType sql.NullString
		var source sql.NullString
		var confidence sql.NullFloat64
		var eventID sql.NullInt64
		if err := rows.Scan(&observation.ID, &observation.EntityID, &observation.EntityName, &entityType, &observation.Content, &source, &confidence, &eventID, &observation.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan semantic observation: %w", err)
		}
		observation.EntityType = nullableStringValue(entityType)
		observation.Source = nullableStringValue(source)
		if confidence.Valid {
			observation.Confidence = confidence.Float64
		}
		if eventID.Valid {
			value := eventID.Int64
			observation.EventID = &value
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate semantic observations: %w", err)
	}
	return observations, nil
}

func validateEmbeddingCacheKey(key EmbeddingCacheKey) error {
	if strings.TrimSpace(key.Provider) == "" {
		return fmt.Errorf("embedding cache provider is required")
	}
	if strings.TrimSpace(key.EndpointKey) == "" {
		return fmt.Errorf("embedding cache endpoint key is required")
	}
	if strings.TrimSpace(key.ModelID) == "" {
		return fmt.Errorf("embedding cache model id is required")
	}
	if key.Dimensions <= 0 {
		return fmt.Errorf("embedding cache dimensions must be > 0")
	}
	return nil
}
