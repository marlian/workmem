package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"workmem/internal/embedding"
	"workmem/internal/store"
)

const (
	DefaultSimilarityThreshold      = 0.92
	DefaultMaxEmbeddingCalls        = 500
	DefaultMaxEmbeddingsPerRequest  = 64
	DefaultMaxObservationsPerEntity = 200
	DefaultMaxCandidatesPerEntity   = 100
)

type ReportOptions struct {
	GeneratedAt              time.Time
	Since                    time.Duration
	SinceLabel               string
	MinObsPerEntity          int
	MaxEntitiesPerRun        int
	Scope                    string
	Threshold                float64
	MaxEmbeddingCalls        int
	MaxEmbeddingsPerRequest  int
	MaxObservationsPerEntity int
	MaxCandidatesPerEntity   int
}

type Report struct {
	GeneratedAt              time.Time
	Mode                     string
	Scope                    string
	Since                    time.Duration
	SinceLabel               string
	Provider                 string
	EndpointKey              string
	Model                    string
	Dimensions               int
	Threshold                float64
	MaxEmbeddingCalls        int
	MaxEmbeddingsPerRequest  int
	MaxObservationsPerEntity int
	MaxCandidatesPerEntity   int
	ScannedEntities          []store.ReconcileEntitySignal
	Candidates               []Candidate
	EntityLimits             []EntityLimit
	EmbeddingsReused         int
	EmbeddingsCached         int
	EmbeddingRequests        int
	MemoryMutationsApplied   int
}

type EntityLimit struct {
	EntityID               int64
	EntityName             string
	EntityType             string
	ActiveObservations     int
	ObservationsConsidered int
	ObservationsOmitted    int
	CandidatesReturned     int
	CandidatesOmitted      int
}

type Candidate struct {
	EntityID      int64
	EntityName    string
	EntityType    string
	SourceObsID   int64
	TargetObsID   int64
	Similarity    float64
	SourceContent string
	TargetContent string
	SourceCreated string
	TargetCreated string
}

func BuildReport(ctx context.Context, db *sql.DB, cfg embedding.Config, embedder embedding.Client, options ReportOptions) (*Report, error) {
	if db == nil {
		return nil, fmt.Errorf("semantic report: nil db")
	}
	if cfg.Provider == embedding.ProviderNone {
		return nil, fmt.Errorf("semantic report: embedding provider is required")
	}
	if embedder == nil {
		return nil, fmt.Errorf("semantic report: nil embedding client")
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
	threshold := options.Threshold
	if threshold <= 0 || threshold > 1 {
		return nil, fmt.Errorf("semantic report: threshold must be > 0 and <= 1")
	}
	maxEmbeddingCalls := options.MaxEmbeddingCalls
	if maxEmbeddingCalls <= 0 {
		maxEmbeddingCalls = DefaultMaxEmbeddingCalls
	}
	maxEmbeddingsPerRequest := options.MaxEmbeddingsPerRequest
	if maxEmbeddingsPerRequest <= 0 {
		maxEmbeddingsPerRequest = DefaultMaxEmbeddingsPerRequest
	}
	maxObservationsPerEntity := options.MaxObservationsPerEntity
	if maxObservationsPerEntity <= 0 {
		maxObservationsPerEntity = DefaultMaxObservationsPerEntity
	}
	maxCandidatesPerEntity := options.MaxCandidatesPerEntity
	if maxCandidatesPerEntity <= 0 {
		maxCandidatesPerEntity = DefaultMaxCandidatesPerEntity
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		scope = "global"
	}
	key := store.EmbeddingCacheKey{
		Provider:    string(cfg.Provider),
		EndpointKey: cfg.EndpointKey,
		ModelID:     cfg.Model,
		Dimensions:  cfg.Dimensions,
	}

	signals, observations, err := store.SelectSemanticReconcileObservations(db, store.SemanticObservationSelectOptions{
		GeneratedAt:       now,
		Since:             since,
		MinObsPerEntity:   options.MinObsPerEntity,
		MaxEntitiesPerRun: options.MaxEntitiesPerRun,
	})
	if err != nil {
		return nil, err
	}
	observations, limitsByEntity := applyObservationCaps(observations, signals, maxObservationsPerEntity)
	observationIDs := make([]int64, 0, len(observations))
	for _, observation := range observations {
		observationIDs = append(observationIDs, observation.ID)
	}
	cached, err := store.LoadObservationEmbeddings(db, observationIDs, key)
	if err != nil {
		return nil, err
	}
	vectors := make(map[int64][]float32, len(observations))
	missing := make([]store.SemanticObservation, 0)
	for _, observation := range observations {
		blob, ok := cached[observation.ID]
		if !ok {
			missing = append(missing, observation)
			continue
		}
		vector, err := embedding.DecodeVector(blob, cfg.Dimensions)
		if err != nil {
			return nil, fmt.Errorf("semantic report: cached embedding %d: %w", observation.ID, err)
		}
		vectors[observation.ID] = vector
	}
	if len(missing) > maxEmbeddingCalls {
		return nil, fmt.Errorf("semantic report: %d uncached embedding(s) required exceeds limit %d", len(missing), maxEmbeddingCalls)
	}
	embeddingRequests := 0
	if len(missing) > 0 {
		encoded := make([][]byte, len(missing))
		for start := 0; start < len(missing); start += maxEmbeddingsPerRequest {
			end := start + maxEmbeddingsPerRequest
			if end > len(missing) {
				end = len(missing)
			}
			chunk := missing[start:end]
			texts := make([]string, 0, len(chunk))
			for _, observation := range chunk {
				texts = append(texts, observation.Content)
			}
			embedded, err := embedder.Embed(ctx, texts)
			if err != nil {
				return nil, err
			}
			embeddingRequests++
			if len(embedded) != len(chunk) {
				return nil, fmt.Errorf("semantic report: embedding response count mismatch")
			}
			for i, vector := range embedded {
				blob, err := embedding.EncodeVector(vector, cfg.Dimensions)
				if err != nil {
					return nil, err
				}
				index := start + i
				encoded[index] = blob
				vectors[missing[index].ID] = vector
			}
		}
		tx, err := db.Begin()
		if err != nil {
			return nil, fmt.Errorf("begin semantic embedding cache write: %w", err)
		}
		defer tx.Rollback()
		for i, observation := range missing {
			if err := store.UpsertObservationEmbedding(tx, observation.ID, key, encoded[i]); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit semantic embedding cache write: %w", err)
		}
	}

	candidates, err := buildCandidates(observations, vectors, threshold, maxCandidatesPerEntity, limitsByEntity)
	if err != nil {
		return nil, err
	}
	entityLimits := collectEntityLimits(limitsByEntity)
	return &Report{
		GeneratedAt:              now.UTC(),
		Mode:                     "report",
		Scope:                    scope,
		Since:                    since,
		SinceLabel:               sinceLabel,
		Provider:                 string(cfg.Provider),
		EndpointKey:              cfg.EndpointKey,
		Model:                    cfg.Model,
		Dimensions:               cfg.Dimensions,
		Threshold:                threshold,
		MaxEmbeddingCalls:        maxEmbeddingCalls,
		MaxEmbeddingsPerRequest:  maxEmbeddingsPerRequest,
		MaxObservationsPerEntity: maxObservationsPerEntity,
		MaxCandidatesPerEntity:   maxCandidatesPerEntity,
		ScannedEntities:          signals,
		Candidates:               candidates,
		EntityLimits:             entityLimits,
		EmbeddingsReused:         len(cached),
		EmbeddingsCached:         len(missing),
		EmbeddingRequests:        embeddingRequests,
		MemoryMutationsApplied:   0,
	}, nil
}

func applyObservationCaps(observations []store.SemanticObservation, signals []store.ReconcileEntitySignal, maxObservationsPerEntity int) ([]store.SemanticObservation, map[int64]*EntityLimit) {
	limitsByEntity := make(map[int64]*EntityLimit)
	activeByEntity := make(map[int64]int)
	for _, observation := range observations {
		activeByEntity[observation.EntityID]++
	}
	for _, signal := range signals {
		limitsByEntity[signal.EntityID] = &EntityLimit{
			EntityID:           signal.EntityID,
			EntityName:         signal.Name,
			EntityType:         signal.EntityType,
			ActiveObservations: signal.ActiveObservations,
		}
	}
	seenByEntity := make(map[int64]int)
	bounded := make([]store.SemanticObservation, 0, len(observations))
	for _, observation := range observations {
		limit := limitsByEntity[observation.EntityID]
		if limit == nil {
			limit = &EntityLimit{EntityID: observation.EntityID, EntityName: observation.EntityName, EntityType: observation.EntityType}
			limitsByEntity[observation.EntityID] = limit
		}
		if limit.EntityName == "" {
			limit.EntityName = observation.EntityName
		}
		if limit.EntityType == "" {
			limit.EntityType = observation.EntityType
		}
		if limit.ActiveObservations == 0 {
			limit.ActiveObservations = activeByEntity[observation.EntityID]
		}
		seenByEntity[observation.EntityID]++
		if seenByEntity[observation.EntityID] > maxObservationsPerEntity {
			limit.ObservationsOmitted++
			continue
		}
		limit.ObservationsConsidered++
		bounded = append(bounded, observation)
	}
	return bounded, limitsByEntity
}

func buildCandidates(observations []store.SemanticObservation, vectors map[int64][]float32, threshold float64, maxCandidatesPerEntity int, limitsByEntity map[int64]*EntityLimit) ([]Candidate, error) {
	byEntity := make(map[int64][]store.SemanticObservation)
	for _, observation := range observations {
		if _, ok := vectors[observation.ID]; !ok {
			return nil, fmt.Errorf("semantic report: missing vector for observation %d", observation.ID)
		}
		byEntity[observation.EntityID] = append(byEntity[observation.EntityID], observation)
	}
	candidates := make([]Candidate, 0)
	for _, entityObservations := range byEntity {
		entityCandidates := make([]Candidate, 0)
		for i := 0; i < len(entityObservations); i++ {
			for j := i + 1; j < len(entityObservations); j++ {
				target := entityObservations[i]
				source := entityObservations[j]
				if source.Content == target.Content {
					continue
				}
				similarity, err := embedding.CosineSimilarity(vectors[source.ID], vectors[target.ID])
				if err != nil {
					return nil, err
				}
				if similarity < threshold {
					continue
				}
				entityCandidates = append(entityCandidates, Candidate{
					EntityID:      target.EntityID,
					EntityName:    target.EntityName,
					EntityType:    target.EntityType,
					SourceObsID:   source.ID,
					TargetObsID:   target.ID,
					Similarity:    similarity,
					SourceContent: source.Content,
					TargetContent: target.Content,
					SourceCreated: source.CreatedAt,
					TargetCreated: target.CreatedAt,
				})
			}
		}
		sortCandidates(entityCandidates)
		limit := limitsByEntity[entityObservations[0].EntityID]
		if limit != nil {
			limit.CandidatesReturned = len(entityCandidates)
		}
		if len(entityCandidates) > maxCandidatesPerEntity {
			if limit != nil {
				limit.CandidatesReturned = maxCandidatesPerEntity
				limit.CandidatesOmitted = len(entityCandidates) - maxCandidatesPerEntity
			}
			entityCandidates = entityCandidates[:maxCandidatesPerEntity]
		}
		candidates = append(candidates, entityCandidates...)
	}
	sortCandidates(candidates)
	return candidates, nil
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Similarity != candidates[j].Similarity {
			return candidates[i].Similarity > candidates[j].Similarity
		}
		if candidates[i].EntityName != candidates[j].EntityName {
			return candidates[i].EntityName < candidates[j].EntityName
		}
		if candidates[i].TargetObsID != candidates[j].TargetObsID {
			return candidates[i].TargetObsID > candidates[j].TargetObsID
		}
		return candidates[i].SourceObsID > candidates[j].SourceObsID
	})
}

func collectEntityLimits(limitsByEntity map[int64]*EntityLimit) []EntityLimit {
	limits := make([]EntityLimit, 0)
	for _, limit := range limitsByEntity {
		if limit == nil || (limit.ObservationsOmitted == 0 && limit.CandidatesOmitted == 0) {
			continue
		}
		limits = append(limits, *limit)
	}
	sort.Slice(limits, func(i, j int) bool {
		if limits[i].ObservationsOmitted != limits[j].ObservationsOmitted {
			return limits[i].ObservationsOmitted > limits[j].ObservationsOmitted
		}
		if limits[i].CandidatesOmitted != limits[j].CandidatesOmitted {
			return limits[i].CandidatesOmitted > limits[j].CandidatesOmitted
		}
		return limits[i].EntityName < limits[j].EntityName
	})
	return limits
}
