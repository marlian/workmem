package reconcile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
