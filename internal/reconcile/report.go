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

	"workmem/internal/store"
)

func DefaultReportPath(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return filepath.Join("review", fmt.Sprintf("reconcile-%s.md", now.UTC().Format("20060102-150405")))
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
