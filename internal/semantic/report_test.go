package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"workmem/internal/embedding"
	"workmem/internal/store"
)

type fakeEmbedder struct {
	vectors map[string][]float32
	calls   int
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		vector, ok := f.vectors[text]
		if !ok {
			return nil, fmt.Errorf("missing fake vector for %q", text)
		}
		vectors = append(vectors, append([]float32(nil), vector...))
	}
	return vectors, nil
}

func TestBuildReportProducesCandidatesAndOnlyWritesEmbeddingCache(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report.db")
	entityID, err := store.UpsertEntity(db, "SemanticReportEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	firstID, err := store.AddObservation(db, entityID, "semantic source memory", "test", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(first) error = %v", err)
	}
	secondID, err := store.AddObservation(db, entityID, "semantic target memory", "test", 1.0)
	if err != nil {
		t.Fatalf("AddObservation(second) error = %v", err)
	}
	before := captureSemanticDBState(t, db, "semantic")
	cfg := semanticTestConfig(t)
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		"semantic source memory": {1, 0},
		"semantic target memory": {0.99, 0.01},
	}}

	report, err := BuildReport(context.Background(), db, cfg, embedder, ReportOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		SinceLabel:        "24h",
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		Threshold:         0.9,
		MaxEmbeddingCalls: 10,
	})
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if embedder.calls != 1 {
		t.Fatalf("embedder calls = %d, want 1", embedder.calls)
	}
	if len(report.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(report.Candidates))
	}
	candidate := report.Candidates[0]
	if candidate.SourceObsID != firstID || candidate.TargetObsID != secondID {
		t.Fatalf("candidate source/target = %d/%d, want %d/%d", candidate.SourceObsID, candidate.TargetObsID, firstID, secondID)
	}
	assertSemanticDBStateOnlyCacheChanged(t, db, before, "semantic")
	if got := countSemanticEmbeddings(t, db); got != 2 {
		t.Fatalf("embedding cache rows = %d, want 2", got)
	}

	secondEmbedder := &fakeEmbedder{vectors: map[string][]float32{}}
	secondReport, err := BuildReport(context.Background(), db, cfg, secondEmbedder, ReportOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Scope:             "global",
		Threshold:         0.9,
		MaxEmbeddingCalls: 10,
	})
	if err != nil {
		t.Fatalf("BuildReport(cache hit) error = %v", err)
	}
	if secondEmbedder.calls != 0 {
		t.Fatalf("cache hit embedder calls = %d, want 0", secondEmbedder.calls)
	}
	if secondReport.EmbeddingsReused != 2 || secondReport.EmbeddingsCached != 0 {
		t.Fatalf("cache report reused/cached = %d/%d, want 2/0", secondReport.EmbeddingsReused, secondReport.EmbeddingsCached)
	}
}

func TestBuildReportFailsClosedBeforeCacheWrites(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report-fail-closed.db")
	entityID, err := store.UpsertEntity(db, "SemanticFailClosedEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	if _, err := store.AddObservation(db, entityID, "first", "test", 1.0); err != nil {
		t.Fatalf("AddObservation(first) error = %v", err)
	}
	if _, err := store.AddObservation(db, entityID, "second", "test", 1.0); err != nil {
		t.Fatalf("AddObservation(second) error = %v", err)
	}
	cfg := semanticTestConfig(t)
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		"first":  {1, 0},
		"second": {1},
	}}
	_, err = BuildReport(context.Background(), db, cfg, embedder, ReportOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Threshold:         0.9,
		MaxEmbeddingCalls: 10,
	})
	if err == nil {
		t.Fatalf("BuildReport(dimension mismatch) error = nil, want error")
	}
	if got := countSemanticEmbeddings(t, db); got != 0 {
		t.Fatalf("embedding cache rows after failed report = %d, want 0", got)
	}
}

func TestBuildReportRejectsZeroThreshold(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report-zero-threshold.db")
	_, err := BuildReport(context.Background(), db, semanticTestConfig(t), &fakeEmbedder{}, ReportOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Threshold:         0,
		MaxEmbeddingCalls: 10,
	})
	if err == nil {
		t.Fatalf("BuildReport(zero threshold) error = nil, want error")
	}
}

func semanticTestConfig(t *testing.T) embedding.Config {
	t.Helper()
	cfg, err := embedding.ParseConfig(embedding.Options{
		Provider:   string(embedding.ProviderOpenAICompatible),
		BaseURL:    "http://localhost:1235/v1",
		Model:      "local-model",
		Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	return cfg
}

func newSemanticTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := store.InitDB(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type semanticDBState struct {
	observations       int
	reconcileRuns      int
	reconcileDecisions int
	accessCountSum     int
	supersededCount    int
	ftsMatches         int
}

func captureSemanticDBState(t *testing.T, db *sql.DB, match string) semanticDBState {
	t.Helper()
	return semanticDBState{
		observations:       queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM observations`),
		reconcileRuns:      queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM reconcile_runs`),
		reconcileDecisions: queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM reconcile_decisions`),
		accessCountSum:     queryIntForSemanticTest(t, db, `SELECT COALESCE(SUM(access_count), 0) FROM observations`),
		supersededCount:    queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM observations WHERE superseded_by IS NOT NULL OR superseded_by_run IS NOT NULL`),
		ftsMatches:         queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM memory_fts WHERE memory_fts MATCH ?`, match),
	}
}

func assertSemanticDBStateOnlyCacheChanged(t *testing.T, db *sql.DB, before semanticDBState, match string) {
	t.Helper()
	after := captureSemanticDBState(t, db, match)
	if after != before {
		t.Fatalf("semantic report changed forbidden DB state: before=%#v after=%#v", before, after)
	}
}

func countSemanticEmbeddings(t *testing.T, db *sql.DB) int {
	t.Helper()
	return queryIntForSemanticTest(t, db, `SELECT COUNT(*) FROM observation_embeddings`)
}

func queryIntForSemanticTest(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q error = %v", query, err)
	}
	return count
}
