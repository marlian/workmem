package reconcile

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"workmem/internal/semantic"
	"workmem/internal/store"
)

func DefaultReportPath(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return filepath.Join("review", fmt.Sprintf("reconcile-%s.md", now.UTC().Format("20060102-150405")))
}

func DefaultSemanticReportPath(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return filepath.Join("review", fmt.Sprintf("reconcile-semantic-%s.md", now.UTC().Format("20060102-150405")))
}

func WriteProposeReport(path string, report *store.ReconcileProposeReport) (string, error) {
	if report == nil {
		return "", fmt.Errorf("write reconcile report: nil report")
	}
	if strings.TrimSpace(path) == "" {
		path = DefaultReportPath(report.GeneratedAt)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create reconcile report dir: %w", err)
		}
	}
	content := RenderProposeReport(report)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write reconcile report: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("harden reconcile report mode: %w", err)
	}
	return path, nil
}

func WriteSemanticReport(path string, report *semantic.Report) (string, error) {
	if report == nil {
		return "", fmt.Errorf("write semantic reconcile report: nil report")
	}
	if strings.TrimSpace(path) == "" {
		path = DefaultSemanticReportPath(report.GeneratedAt)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create semantic reconcile report dir: %w", err)
		}
	}
	content := RenderSemanticReport(report)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write semantic reconcile report: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("harden semantic reconcile report mode: %w", err)
	}
	return path, nil
}

func RenderProposeReport(report *store.ReconcileProposeReport) string {
	if report == nil {
		return ""
	}
	var buf bytes.Buffer
	generatedAt := report.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	fmt.Fprintf(&buf, "# Reconcile Run - %s\n\n", generatedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "Mode: %s\n", report.Mode)
	fmt.Fprintf(&buf, "Scope: %s\n", report.Scope)
	sinceLabel := strings.TrimSpace(report.SinceLabel)
	if sinceLabel == "" {
		sinceLabel = report.Since.String()
	}
	fmt.Fprintf(&buf, "Since: %s\n\n", sinceLabel)

	fmt.Fprintf(&buf, "## Summary\n")
	fmt.Fprintf(&buf, "- Entities scanned: %d\n", len(report.ScannedEntities))
	fmt.Fprintf(&buf, "- Exact duplicate groups: %d\n", len(report.DuplicateGroups))
	fmt.Fprintf(&buf, "- Candidates proposed: %d\n", report.CandidatesProposed)
	fmt.Fprintf(&buf, "- Mutations applied: 0\n\n")

	fmt.Fprintf(&buf, "## Exact duplicate candidates\n\n")
	if len(report.DuplicateGroups) == 0 {
		fmt.Fprintf(&buf, "No exact duplicate candidates found.\n\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Content | Target obs | Source obs | Action | Rationale |\n")
		fmt.Fprintf(&buf, "|---|---|---:|---:|---|---|\n")
		for _, group := range report.DuplicateGroups {
			action := group.Action
			if action == "" {
				action = store.ReconcileActionProposed
			}
			rationale := group.Rationale
			if rationale == "" {
				rationale = store.ReconcileRationaleExactDuplicateSameEntity
			}
			for _, source := range group.Sources {
				fmt.Fprintf(&buf, "| %s | %s | %d | %d | %s | %s |\n",
					markdownCell(group.EntityName),
					markdownCell(truncateString(group.Content, 96)),
					group.Target.ID,
					source.ID,
					markdownCell(action),
					markdownCell(rationale),
				)
			}
		}
		fmt.Fprintf(&buf, "\n")
	}

	fmt.Fprintf(&buf, "## Hygiene signals\n\n")
	if len(report.ScannedEntities) == 0 {
		fmt.Fprintf(&buf, "No entities matched the scan window.\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Active observations | Recent observations | Last observation |\n")
		fmt.Fprintf(&buf, "|---|---:|---:|---|\n")
		for _, signal := range report.ScannedEntities {
			fmt.Fprintf(&buf, "| %s | %d | %d | %s |\n",
				markdownCell(signal.Name),
				signal.ActiveObservations,
				signal.RecentObservations,
				markdownCell(signal.LastObservationAt),
			)
		}
	}
	return buf.String()
}

func RenderSemanticReport(report *semantic.Report) string {
	if report == nil {
		return ""
	}
	var buf bytes.Buffer
	generatedAt := report.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	fmt.Fprintf(&buf, "# Semantic Reconcile Report - %s\n\n", generatedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "Mode: %s\n", report.Mode)
	fmt.Fprintf(&buf, "Scope: %s\n", report.Scope)
	sinceLabel := strings.TrimSpace(report.SinceLabel)
	if sinceLabel == "" {
		sinceLabel = report.Since.String()
	}
	fmt.Fprintf(&buf, "Since: %s\n", sinceLabel)
	fmt.Fprintf(&buf, "Provider: %s\n", report.Provider)
	fmt.Fprintf(&buf, "Endpoint key: <redacted; stored in embedding cache identity>\n")
	fmt.Fprintf(&buf, "Model: %s\n", report.Model)
	fmt.Fprintf(&buf, "Dimensions: %d\n", report.Dimensions)
	fmt.Fprintf(&buf, "Threshold: %.4f\n\n", report.Threshold)

	fmt.Fprintf(&buf, "## Safety\n")
	fmt.Fprintf(&buf, "- REPORT ONLY: no semantic apply path exists.\n")
	fmt.Fprintf(&buf, "- Mutations allowed in this mode: embedding cache writes only.\n")
	fmt.Fprintf(&buf, "- Mutations forbidden in this mode: observations, supersession fields, reconcile audit rows, access counts, and FTS state.\n\n")

	fmt.Fprintf(&buf, "## Summary\n")
	fmt.Fprintf(&buf, "- Entities scanned: %d\n", len(report.ScannedEntities))
	fmt.Fprintf(&buf, "- Semantic candidates: %d\n", len(report.Candidates))
	fmt.Fprintf(&buf, "- Embeddings reused from cache: %d\n", report.EmbeddingsReused)
	fmt.Fprintf(&buf, "- Embeddings cached this run: %d\n", report.EmbeddingsCached)
	fmt.Fprintf(&buf, "- Embedding requests: %d\n", report.EmbeddingRequests)
	fmt.Fprintf(&buf, "- Memory mutations applied: 0\n\n")

	fmt.Fprintf(&buf, "## Semantic candidates\n\n")
	if len(report.Candidates) == 0 {
		fmt.Fprintf(&buf, "No semantic candidates found.\n\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Similarity | Target obs | Source obs | Target content | Source content |\n")
		fmt.Fprintf(&buf, "|---|---:|---:|---:|---|---|\n")
		for _, candidate := range report.Candidates {
			fmt.Fprintf(&buf, "| %s | %.4f | %d | %d | %s | %s |\n",
				markdownCell(candidate.EntityName),
				candidate.Similarity,
				candidate.TargetObsID,
				candidate.SourceObsID,
				markdownCell(truncateString(candidate.TargetContent, 96)),
				markdownCell(truncateString(candidate.SourceContent, 96)),
			)
		}
		fmt.Fprintf(&buf, "\n")
	}

	fmt.Fprintf(&buf, "## Hygiene signals\n\n")
	if len(report.ScannedEntities) == 0 {
		fmt.Fprintf(&buf, "No entities matched the scan window.\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Active observations | Recent observations | Last observation |\n")
		fmt.Fprintf(&buf, "|---|---:|---:|---|\n")
		for _, signal := range report.ScannedEntities {
			fmt.Fprintf(&buf, "| %s | %d | %d | %s |\n",
				markdownCell(signal.Name),
				signal.ActiveObservations,
				signal.RecentObservations,
				markdownCell(signal.LastObservationAt),
			)
		}
	}
	return buf.String()
}

func markdownCell(value string) string {
	value = html.EscapeString(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "*", "\\*")
	value = strings.ReplaceAll(value, "_", "\\_")
	value = strings.ReplaceAll(value, "[", "\\[")
	value = strings.ReplaceAll(value, "]", "\\]")
	value = strings.ReplaceAll(value, "(", "\\(")
	value = strings.ReplaceAll(value, ")", "\\)")
	value = strings.ReplaceAll(value, "!", "\\!")
	return strings.TrimSpace(value)
}

func truncateString(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
