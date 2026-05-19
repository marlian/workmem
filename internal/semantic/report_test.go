package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workmem/internal/embedding"
	"workmem/internal/store"
)

type fakeEmbedder struct {
	vectors map[string][]float32
	calls   int
	batches [][]string
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.batches = append(f.batches, append([]string(nil), texts...))
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

func TestBuildReportChunksEmbeddingRequestsAndCachesAll(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report-chunks.db")
	entityID, err := store.UpsertEntity(db, "SemanticChunkEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	for _, content := range []string{"chunk source a", "chunk source b", "chunk source c"} {
		if _, err := store.AddObservation(db, entityID, content, "test", 1.0); err != nil {
			t.Fatalf("AddObservation(%q) error = %v", content, err)
		}
	}
	before := captureSemanticDBState(t, db, "chunk")
	entryVectors := map[string][]float32{
		"chunk source a": {1, 0},
		"chunk source b": {0.99, 0.01},
		"chunk source c": {0.98, 0.02},
	}
	embedder := &fakeEmbedder{vectors: entryVectors}
	report, err := BuildReport(context.Background(), db, semanticTestConfig(t), embedder, ReportOptions{
		GeneratedAt:              time.Now().UTC(),
		Since:                    24 * time.Hour,
		MinObsPerEntity:          2,
		MaxEntitiesPerRun:        10,
		Threshold:                0.9,
		MaxEmbeddingCalls:        10,
		MaxEmbeddingsPerRequest:  2,
		MaxObservationsPerEntity: 10,
		MaxCandidatesPerEntity:   10,
	})
	if err != nil {
		t.Fatalf("BuildReport(chunked) error = %v", err)
	}
	if report.EmbeddingRequests != 2 || embedder.calls != 2 {
		t.Fatalf("embedding requests/calls = %d/%d, want 2/2", report.EmbeddingRequests, embedder.calls)
	}
	if got := []int{len(embedder.batches[0]), len(embedder.batches[1])}; got[0] != 2 || got[1] != 1 {
		t.Fatalf("embedding batch sizes = %v, want [2 1]", got)
	}
	if report.EmbeddingsCached != 3 || countSemanticEmbeddings(t, db) != 3 {
		t.Fatalf("cached embeddings = report %d db %d, want 3/3", report.EmbeddingsCached, countSemanticEmbeddings(t, db))
	}
	assertSemanticDBStateOnlyCacheChanged(t, db, before, "chunk")
	secondEmbedder := &fakeEmbedder{vectors: map[string][]float32{}}
	secondReport, err := BuildReport(context.Background(), db, semanticTestConfig(t), secondEmbedder, ReportOptions{
		GeneratedAt:              time.Now().UTC(),
		Since:                    24 * time.Hour,
		MinObsPerEntity:          2,
		MaxEntitiesPerRun:        10,
		Threshold:                0.9,
		MaxEmbeddingCalls:        10,
		MaxEmbeddingsPerRequest:  2,
		MaxObservationsPerEntity: 10,
		MaxCandidatesPerEntity:   10,
	})
	if err != nil {
		t.Fatalf("BuildReport(chunked cache hit) error = %v", err)
	}
	if secondEmbedder.calls != 0 || secondReport.EmbeddingsReused != 3 {
		t.Fatalf("chunked cache hit calls/reused = %d/%d, want 0/3", secondEmbedder.calls, secondReport.EmbeddingsReused)
	}
}

func TestBuildReportAppliesEntityCapsAndReportsLimits(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report-caps.db")
	entityID, err := store.UpsertEntity(db, "SemanticCapEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	contents := []string{"cap source a", "cap source b", "cap source c", "cap source d"}
	for _, content := range contents {
		if _, err := store.AddObservation(db, entityID, content, "test", 1.0); err != nil {
			t.Fatalf("AddObservation(%q) error = %v", content, err)
		}
	}
	secondEntityID, err := store.UpsertEntity(db, "SemanticSecondCapEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity(second) error = %v", err)
	}
	secondContents := []string{"second cap source a", "second cap source b", "second cap source c", "second cap source d"}
	for _, content := range secondContents {
		if _, err := store.AddObservation(db, secondEntityID, content, "test", 1.0); err != nil {
			t.Fatalf("AddObservation(%q) error = %v", content, err)
		}
	}
	before := captureSemanticDBState(t, db, "cap")
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		"cap source a":        {1, 0},
		"cap source b":        {0.99, 0.01},
		"cap source c":        {0.98, 0.02},
		"cap source d":        {0.97, 0.03},
		"second cap source a": {1, 0},
		"second cap source b": {0.99, 0.01},
		"second cap source c": {0.98, 0.02},
		"second cap source d": {0.97, 0.03},
	}}
	report, err := BuildReport(context.Background(), db, semanticTestConfig(t), embedder, ReportOptions{
		GeneratedAt:              time.Now().UTC(),
		Since:                    24 * time.Hour,
		MinObsPerEntity:          2,
		MaxEntitiesPerRun:        10,
		Threshold:                0.9,
		MaxEmbeddingCalls:        10,
		MaxEmbeddingsPerRequest:  10,
		MaxObservationsPerEntity: 3,
		MaxCandidatesPerEntity:   1,
	})
	if err != nil {
		t.Fatalf("BuildReport(capped) error = %v", err)
	}
	if len(report.Candidates) != 2 {
		t.Fatalf("candidates = %d, want capped 2", len(report.Candidates))
	}
	if len(report.EntityLimits) != 2 {
		t.Fatalf("entity limits = %d, want 2", len(report.EntityLimits))
	}
	limitsByName := make(map[string]EntityLimit)
	for _, limit := range report.EntityLimits {
		limitsByName[limit.EntityName] = limit
	}
	for _, name := range []string{"SemanticCapEntity", "SemanticSecondCapEntity"} {
		limit := limitsByName[name]
		if limit.ActiveObservations != 4 || limit.ObservationsConsidered != 3 || limit.ObservationsOmitted != 1 {
			t.Fatalf("observation limit for %s = %#v, want active=4 considered=3 omitted=1", name, limit)
		}
		if limit.CandidatesReturned != 1 || limit.CandidatesOmitted != 2 {
			t.Fatalf("candidate limit for %s = %#v, want returned=1 omitted=2", name, limit)
		}
	}
	if got := countSemanticEmbeddings(t, db); got != 6 {
		t.Fatalf("embedding cache rows = %d, want only considered observations cached", got)
	}
	assertSemanticDBStateOnlyCacheChanged(t, db, before, "cap")
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

func TestBuildReportReportsUncachedEmbeddingLimit(t *testing.T) {
	db := newSemanticTestDB(t, "semantic-report-embedding-limit.db")
	entityID, err := store.UpsertEntity(db, "SemanticEmbeddingLimitEntity", "test")
	if err != nil {
		t.Fatalf("UpsertEntity() error = %v", err)
	}
	if _, err := store.AddObservation(db, entityID, "first", "test", 1.0); err != nil {
		t.Fatalf("AddObservation(first) error = %v", err)
	}
	if _, err := store.AddObservation(db, entityID, "second", "test", 1.0); err != nil {
		t.Fatalf("AddObservation(second) error = %v", err)
	}
	_, err = BuildReport(context.Background(), db, semanticTestConfig(t), &fakeEmbedder{}, ReportOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             24 * time.Hour,
		MinObsPerEntity:   2,
		MaxEntitiesPerRun: 10,
		Threshold:         0.9,
		MaxEmbeddingCalls: 1,
	})
	if err == nil {
		t.Fatalf("BuildReport(embedding limit) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "uncached embedding(s)") {
		t.Fatalf("BuildReport(embedding limit) error = %v, want uncached embedding wording", err)
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
