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
	DefaultSimilarityThreshold = 0.92
	DefaultMaxEmbeddingCalls   = 500
)

type ReportOptions struct {
	GeneratedAt       time.Time
	Since             time.Duration
	SinceLabel        string
	MinObsPerEntity   int
	MaxEntitiesPerRun int
	Scope             string
	Threshold         float64
	MaxEmbeddingCalls int
}

type Report struct {
	GeneratedAt       time.Time
	Mode              string
	Scope             string
	Since             time.Duration
	SinceLabel        string
	Provider          string
	EndpointKey       string
	Model             string
	Dimensions        int
	Threshold         float64
	MaxEmbeddingCalls int
	ScannedEntities   []store.ReconcileEntitySignal
	Candidates        []Candidate
	EmbeddingsReused  int
	EmbeddingsCached  int
	EmbeddingRequests int
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

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

func BuildReport(ctx context.Context, db *sql.DB, cfg embedding.Config, embedder Embedder, options ReportOptions) (*Report, error) {
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
		SinceLabel:        sinceLabel,
		MinObsPerEntity:   options.MinObsPerEntity,
		MaxEntitiesPerRun: options.MaxEntitiesPerRun,
	})
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("semantic report: %d embedding call(s) required exceeds limit %d", len(missing), maxEmbeddingCalls)
	}
	embeddingRequests := 0
	if len(missing) > 0 {
		texts := make([]string, 0, len(missing))
		for _, observation := range missing {
			texts = append(texts, observation.Content)
		}
		embedded, err := embedder.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		embeddingRequests = 1
		if len(embedded) != len(missing) {
			return nil, fmt.Errorf("semantic report: embedding response count mismatch")
		}
		encoded := make([][]byte, len(embedded))
		for i, vector := range embedded {
			blob, err := embedding.EncodeVector(vector, cfg.Dimensions)
			if err != nil {
				return nil, err
			}
			encoded[i] = blob
			vectors[missing[i].ID] = vector
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

	candidates, err := buildCandidates(observations, vectors, threshold)
	if err != nil {
		return nil, err
	}
	return &Report{
		GeneratedAt:       now.UTC(),
		Mode:              "report",
		Scope:             scope,
		Since:             since,
		SinceLabel:        sinceLabel,
		Provider:          string(cfg.Provider),
		EndpointKey:       cfg.EndpointKey,
		Model:             cfg.Model,
		Dimensions:        cfg.Dimensions,
		Threshold:         threshold,
		MaxEmbeddingCalls: maxEmbeddingCalls,
		ScannedEntities:   signals,
		Candidates:        candidates,
		EmbeddingsReused:  len(cached),
		EmbeddingsCached:  len(missing),
		EmbeddingRequests: embeddingRequests,
	}, nil
}

func buildCandidates(observations []store.SemanticObservation, vectors map[int64][]float32, threshold float64) ([]Candidate, error) {
	byEntity := make(map[int64][]store.SemanticObservation)
	for _, observation := range observations {
		if _, ok := vectors[observation.ID]; !ok {
			return nil, fmt.Errorf("semantic report: missing vector for observation %d", observation.ID)
		}
		byEntity[observation.EntityID] = append(byEntity[observation.EntityID], observation)
	}
	candidates := make([]Candidate, 0)
	for _, entityObservations := range byEntity {
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
				candidates = append(candidates, Candidate{
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
	}
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
	return candidates, nil
}
