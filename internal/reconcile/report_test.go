package reconcile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"workmem/internal/semantic"
	"workmem/internal/store"
)

func TestRenderProposeReportIncludesSummaryAndEscapesCells(t *testing.T) {
	report := &store.ReconcileProposeReport{
		GeneratedAt:        time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
		Mode:               "propose",
		Scope:              "global",
		Since:              30 * 24 * time.Hour,
		SinceLabel:         "30d",
		CandidatesProposed: 1,
		ScannedEntities: []store.ReconcileEntitySignal{{
			EntityID:           1,
			Name:               "Entity|One<script>",
			ActiveObservations: 2,
			RecentObservations: 2,
			LastObservationAt:  "2026-05-02 08:00:00",
		}},
		DuplicateGroups: []store.ReconcileDuplicateGroup{{
			EntityID:   1,
			EntityName: "Entity|One<script>",
			Content:    "duplicate | [link](x) ![img](y) *bold* `code`",
			Target:     store.ReconcileObservation{ID: 20},
			Sources:    []store.ReconcileObservation{{ID: 10}},
		}},
	}

	rendered := RenderProposeReport(report)
	for _, want := range []string{
		"Mode: propose",
		"Since: 30d",
		"- Entities scanned: 1",
		"- Candidates proposed: 1",
		"Entity\\|One&lt;script&gt;",
		"duplicate \\| \\[link\\]\\(x\\) \\!\\[img\\]\\(y\\) \\*bold\\* \\`code\\`",
		"| Entity | Content | Target obs | Source obs | Action | Rationale |",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderProposeReportNilReturnsEmptyString(t *testing.T) {
	if got := RenderProposeReport(nil); got != "" {
		t.Fatalf("RenderProposeReport(nil) = %q, want empty string", got)
	}
}

func TestWriteProposeReportCreatesPrivateMarkdownFile(t *testing.T) {
	report := &store.ReconcileProposeReport{
		GeneratedAt: time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
		Mode:        "propose",
		Scope:       "global",
		Since:       time.Hour,
	}
	path := filepath.Join(t.TempDir(), "nested", "report.md")
	written, err := WriteProposeReport(path, report)
	if err != nil {
		t.Fatalf("WriteProposeReport() error = %v", err)
	}
	if written != path {
		t.Fatalf("written path = %q, want %q", written, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(report) error = %v", err)
	}
	if !strings.Contains(string(content), "# Reconcile Run") {
		t.Fatalf("report content missing heading: %s", string(content))
	}
	assertReportMode0600(t, path)
}

func TestWriteProposeReportHardensExistingMarkdownFile(t *testing.T) {
	report := &store.ReconcileProposeReport{
		GeneratedAt: time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
		Mode:        "propose",
		Scope:       "global",
		Since:       time.Hour,
	}
	path := filepath.Join(t.TempDir(), "existing-report.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	if _, err := WriteProposeReport(path, report); err != nil {
		t.Fatalf("WriteProposeReport(existing) error = %v", err)
	}
	assertReportMode0600(t, path)
}

func TestRenderSemanticReportIncludesSafetyAndEscapesCells(t *testing.T) {
	report := &semantic.Report{
		GeneratedAt:              time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC),
		Mode:                     "report",
		Scope:                    "global",
		Since:                    30 * 24 * time.Hour,
		SinceLabel:               "30d",
		Provider:                 "openai-compatible",
		EndpointKey:              "http://localhost:1235/v1",
		Model:                    "local-model",
		Dimensions:               2,
		Threshold:                0.92,
		MaxEmbeddingCalls:        500,
		MaxEmbeddingsPerRequest:  64,
		MaxObservationsPerEntity: 200,
		MaxCandidatesPerEntity:   100,
		EmbeddingsReused:         1,
		EmbeddingsCached:         2,
		EmbeddingRequests:        1,
		ScannedEntities: []store.ReconcileEntitySignal{{
			Name:               "Entity|One<script>",
			ActiveObservations: 2,
			RecentObservations: 2,
			LastObservationAt:  "2026-05-04 08:00:00",
		}},
		Candidates: []semantic.Candidate{{
			EntityName:    "Entity|One<script>",
			Similarity:    0.9876,
			TargetObsID:   20,
			SourceObsID:   10,
			TargetContent: "target | [link](x)",
			SourceContent: "source | *bold*",
		}, {
			EntityName:    "Entity|One<script>",
			Similarity:    0.9,
			TargetObsID:   20,
			SourceObsID:   30,
			TargetContent: "target | [link](x)",
			SourceContent: "second source",
		}},
		EntityLimits: []semantic.EntityLimit{{
			EntityName:             "Entity|One<script>",
			ActiveObservations:     250,
			ObservationsConsidered: 200,
			ObservationsOmitted:    50,
			CandidatesReturned:     100,
			CandidatesOmitted:      12,
		}},
	}
	rendered := RenderSemanticReport(report)
	for _, want := range []string{
		"REPORT ONLY",
		"Endpoint key: <redacted; stored in embedding cache identity>",
		"Max embeddings per request: 64",
		"Mutations allowed in this mode: embedding cache writes only.",
		"- Memory mutations applied: 0",
		"- Candidate clusters: 1",
		"## Candidate clusters",
		"| Entity | Cluster | Obs ids | Pairs | Max sim | Avg sim | Hint | Recommendation |",
		"| Entity\\|One&lt;script&gt; | 1 | 10, 20, 30 | 2 | 0.9876 | 0.9438 |",
		"### Cluster 1 - Entity\\|One&lt;script&gt;",
		"- Observation ids: `10, 20, 30`",
		"- Manual decision:",
		"  - [ ] consolidate into a new observation",
		"| Entity | Similarity | Target obs | Source obs | Target content | Source content |",
		"Entity\\|One&lt;script&gt;",
		"target \\| \\[link\\]\\(x\\)",
		"source \\| \\*bold\\*",
		"| Entity | Active observations | Observations considered | Observations omitted | Candidates returned | Candidates omitted |",
		"| Entity\\|One&lt;script&gt; | 250 | 200 | 50 | 100 | 12 |",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("semantic report missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, report.EndpointKey) {
		t.Fatalf("semantic report leaked endpoint key:\n%s", rendered)
	}
}

func TestBuildSemanticClustersUsesEntityIDAcrossEntityTypeSnapshots(t *testing.T) {
	clusters := buildSemanticClusters([]semantic.Candidate{{
		EntityID:    42,
		EntityName:  "Entity",
		EntityType:  "old-type",
		Similarity:  0.8,
		TargetObsID: 2,
		SourceObsID: 1,
	}, {
		EntityID:    42,
		EntityName:  "Entity",
		EntityType:  "new-type",
		Similarity:  0.7,
		TargetObsID: 3,
		SourceObsID: 2,
	}})
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d, want 1: %#v", len(clusters), clusters)
	}
	cluster := clusters[0]
	if got := joinObservationIDs(cluster.ObservationIDs); got != "1, 2, 3" {
		t.Fatalf("cluster obs ids = %q, want 1, 2, 3", got)
	}
	if len(cluster.Pairs) != 2 {
		t.Fatalf("cluster pairs = %d, want 2", len(cluster.Pairs))
	}
}

func TestWriteSemanticReportCreatesPrivateMarkdownFile(t *testing.T) {
	report := &semantic.Report{
		GeneratedAt: time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC),
		Mode:        "report",
		Scope:       "global",
		Since:       time.Hour,
	}
	path := filepath.Join(t.TempDir(), "nested", "semantic-report.md")
	written, err := WriteSemanticReport(path, report)
	if err != nil {
		t.Fatalf("WriteSemanticReport() error = %v", err)
	}
	if written != path {
		t.Fatalf("written path = %q, want %q", written, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(report) error = %v", err)
	}
	if !strings.Contains(string(content), "# Semantic Reconcile Report") {
		t.Fatalf("semantic report content missing heading: %s", string(content))
	}
	assertReportMode0600(t, path)
}

func assertReportMode0600(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not portable on Windows")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(report) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("report mode = %v, want 0600", got)
	}
}
