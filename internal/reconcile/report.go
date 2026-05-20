package reconcile

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"workmem/internal/semantic"
	"workmem/internal/store"
)

const semanticClusterTopPairs = 5

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
	fmt.Fprintf(&buf, "Threshold: %.4f\n", report.Threshold)
	fmt.Fprintf(&buf, "Max uncached embeddings: %d\n", report.MaxEmbeddingCalls)
	fmt.Fprintf(&buf, "Max embeddings per request: %d\n", report.MaxEmbeddingsPerRequest)
	fmt.Fprintf(&buf, "Max observations per entity: %d\n", report.MaxObservationsPerEntity)
	fmt.Fprintf(&buf, "Max candidates per entity: %d\n\n", report.MaxCandidatesPerEntity)

	fmt.Fprintf(&buf, "## Safety\n")
	fmt.Fprintf(&buf, "- REPORT ONLY: no semantic apply path exists.\n")
	fmt.Fprintf(&buf, "- Mutations allowed in this mode: embedding cache writes only.\n")
	fmt.Fprintf(&buf, "- Mutations forbidden in this mode: observations, supersession fields, reconcile audit rows, access counts, and FTS state.\n\n")

	clusters := buildSemanticClusters(report.Candidates)

	fmt.Fprintf(&buf, "## Summary\n")
	fmt.Fprintf(&buf, "- Entities scanned: %d\n", len(report.ScannedEntities))
	fmt.Fprintf(&buf, "- Semantic candidates: %d\n", len(report.Candidates))
	fmt.Fprintf(&buf, "- Candidate clusters: %d\n", len(clusters))
	fmt.Fprintf(&buf, "- Embeddings reused from cache: %d\n", report.EmbeddingsReused)
	fmt.Fprintf(&buf, "- Embeddings cached this run: %d\n", report.EmbeddingsCached)
	fmt.Fprintf(&buf, "- Embedding requests: %d\n", report.EmbeddingRequests)
	fmt.Fprintf(&buf, "- Entities with limit signals: %d\n", len(report.EntityLimits))
	fmt.Fprintf(&buf, "- Memory mutations applied: %d\n\n", report.MemoryMutationsApplied)

	fmt.Fprintf(&buf, "## Candidate clusters\n\n")
	if len(clusters) == 0 {
		fmt.Fprintf(&buf, "No candidate clusters found.\n\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Cluster | Obs ids | Pairs | Max sim | Avg sim | Hint | Recommendation |\n")
		fmt.Fprintf(&buf, "|---|---:|---|---:|---:|---:|---|---|\n")
		for _, cluster := range clusters {
			fmt.Fprintf(&buf, "| %s | %d | %s | %d | %.4f | %.4f | %s | inspect |\n",
				markdownCell(cluster.EntityName),
				cluster.Index,
				markdownCell(joinObservationIDs(cluster.ObservationIDs)),
				len(cluster.Pairs),
				cluster.MaxSimilarity,
				cluster.AverageSimilarity,
				markdownCell(cluster.Hint),
			)
		}
		fmt.Fprintf(&buf, "\n")

		for _, cluster := range clusters {
			fmt.Fprintf(&buf, "### Cluster %d - %s\n\n", cluster.Index, markdownCell(cluster.EntityName))
			fmt.Fprintf(&buf, "- Observation ids: `%s`\n", joinObservationIDs(cluster.ObservationIDs))
			fmt.Fprintf(&buf, "- Pairs above threshold: %d\n", len(cluster.Pairs))
			fmt.Fprintf(&buf, "- Similarity range: %.4f max, %.4f average\n", cluster.MaxSimilarity, cluster.AverageSimilarity)
			fmt.Fprintf(&buf, "- Recommendation: inspect before any manual memory action\n")
			fmt.Fprintf(&buf, "- Top pairs:\n")
			limit := semanticClusterTopPairs
			if len(cluster.Pairs) < limit {
				limit = len(cluster.Pairs)
			}
			for i := 0; i < limit; i++ {
				pair := cluster.Pairs[i]
				fmt.Fprintf(&buf, "  - `%d` <-> `%d` (%.4f): %s <=> %s\n",
					pair.TargetObsID,
					pair.SourceObsID,
					pair.Similarity,
					markdownCell(truncateString(pair.TargetContent, 80)),
					markdownCell(truncateString(pair.SourceContent, 80)),
				)
			}
			if len(cluster.Pairs) > limit {
				fmt.Fprintf(&buf, "  - ... %d more pair(s) in the pairwise table below\n", len(cluster.Pairs)-limit)
			}
			fmt.Fprintf(&buf, "- Manual decision:\n")
			fmt.Fprintf(&buf, "  - [ ] keep all\n")
			fmt.Fprintf(&buf, "  - [ ] consolidate into a new observation\n")
			fmt.Fprintf(&buf, "  - [ ] forget stale observations\n")
			fmt.Fprintf(&buf, "  - [ ] move scope\n")
			fmt.Fprintf(&buf, "- Notes:\n\n")
		}
	}

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

	fmt.Fprintf(&buf, "\n## Limit signals\n\n")
	if len(report.EntityLimits) == 0 {
		fmt.Fprintf(&buf, "No per-entity limits were reached.\n")
	} else {
		fmt.Fprintf(&buf, "| Entity | Active observations | Observations considered | Observations omitted | Candidates returned | Candidates omitted |\n")
		fmt.Fprintf(&buf, "|---|---:|---:|---:|---:|---:|\n")
		for _, limit := range report.EntityLimits {
			fmt.Fprintf(&buf, "| %s | %d | %d | %d | %d | %d |\n",
				markdownCell(limit.EntityName),
				limit.ActiveObservations,
				limit.ObservationsConsidered,
				limit.ObservationsOmitted,
				limit.CandidatesReturned,
				limit.CandidatesOmitted,
			)
		}
	}
	return buf.String()
}

type semanticCluster struct {
	Index             int
	EntityName        string
	ObservationIDs    []int64
	Pairs             []semantic.Candidate
	MaxSimilarity     float64
	AverageSimilarity float64
	Hint              string
}

func buildSemanticClusters(candidates []semantic.Candidate) []semanticCluster {
	if len(candidates) == 0 {
		return nil
	}
	type entityClusterInput struct {
		entityName string
		parents    map[int64]int64
		pairs      []semantic.Candidate
	}
	byEntity := make(map[string]*entityClusterInput)
	for _, candidate := range candidates {
		key := semanticClusterEntityKey(candidate)
		input := byEntity[key]
		if input == nil {
			input = &entityClusterInput{
				entityName: strings.TrimSpace(candidate.EntityName),
				parents:    make(map[int64]int64),
			}
			byEntity[key] = input
		}
		ensureSemanticClusterNode(input.parents, candidate.TargetObsID)
		ensureSemanticClusterNode(input.parents, candidate.SourceObsID)
		unionSemanticClusterNodes(input.parents, candidate.TargetObsID, candidate.SourceObsID)
		input.pairs = append(input.pairs, candidate)
	}

	var clusters []semanticCluster
	for _, input := range byEntity {
		componentIDs := make(map[int64][]int64)
		for obsID := range input.parents {
			root := findSemanticClusterRoot(input.parents, obsID)
			componentIDs[root] = append(componentIDs[root], obsID)
		}
		componentPairs := make(map[int64][]semantic.Candidate)
		for _, pair := range input.pairs {
			root := findSemanticClusterRoot(input.parents, pair.TargetObsID)
			componentPairs[root] = append(componentPairs[root], pair)
		}
		for root, obsIDs := range componentIDs {
			pairs := componentPairs[root]
			if len(pairs) == 0 {
				continue
			}
			sort.Slice(obsIDs, func(i, j int) bool { return obsIDs[i] < obsIDs[j] })
			sortSemanticClusterPairs(pairs)
			maxSimilarity := pairs[0].Similarity
			var totalSimilarity float64
			for _, pair := range pairs {
				totalSimilarity += pair.Similarity
			}
			clusters = append(clusters, semanticCluster{
				EntityName:        input.entityName,
				ObservationIDs:    obsIDs,
				Pairs:             pairs,
				MaxSimilarity:     maxSimilarity,
				AverageSimilarity: totalSimilarity / float64(len(pairs)),
				Hint:              semanticClusterHint(pairs[0]),
			})
		}
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].MaxSimilarity != clusters[j].MaxSimilarity {
			return clusters[i].MaxSimilarity > clusters[j].MaxSimilarity
		}
		if clusters[i].AverageSimilarity != clusters[j].AverageSimilarity {
			return clusters[i].AverageSimilarity > clusters[j].AverageSimilarity
		}
		if clusters[i].EntityName != clusters[j].EntityName {
			return clusters[i].EntityName < clusters[j].EntityName
		}
		return joinObservationIDs(clusters[i].ObservationIDs) < joinObservationIDs(clusters[j].ObservationIDs)
	})
	for i := range clusters {
		clusters[i].Index = i + 1
	}
	return clusters
}

func semanticClusterEntityKey(candidate semantic.Candidate) string {
	if candidate.EntityID != 0 {
		return fmt.Sprintf("entity-id:%d", candidate.EntityID)
	}
	return fmt.Sprintf("entity-name:%s\x00%s", candidate.EntityName, candidate.EntityType)
}

func ensureSemanticClusterNode(parents map[int64]int64, obsID int64) {
	if _, ok := parents[obsID]; !ok {
		parents[obsID] = obsID
	}
}

func findSemanticClusterRoot(parents map[int64]int64, obsID int64) int64 {
	parent, ok := parents[obsID]
	if !ok {
		parents[obsID] = obsID
		return obsID
	}
	if parent == obsID {
		return obsID
	}
	root := findSemanticClusterRoot(parents, parent)
	parents[obsID] = root
	return root
}

func unionSemanticClusterNodes(parents map[int64]int64, left int64, right int64) {
	leftRoot := findSemanticClusterRoot(parents, left)
	rightRoot := findSemanticClusterRoot(parents, right)
	if leftRoot == rightRoot {
		return
	}
	if leftRoot < rightRoot {
		parents[rightRoot] = leftRoot
		return
	}
	parents[leftRoot] = rightRoot
}

func sortSemanticClusterPairs(pairs []semantic.Candidate) {
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Similarity != pairs[j].Similarity {
			return pairs[i].Similarity > pairs[j].Similarity
		}
		if pairs[i].TargetObsID != pairs[j].TargetObsID {
			return pairs[i].TargetObsID < pairs[j].TargetObsID
		}
		return pairs[i].SourceObsID < pairs[j].SourceObsID
	})
}

func semanticClusterHint(candidate semantic.Candidate) string {
	target := strings.TrimSpace(candidate.TargetContent)
	source := strings.TrimSpace(candidate.SourceContent)
	if target == "" {
		return truncateString(source, 96)
	}
	if source == "" {
		return truncateString(target, 96)
	}
	return truncateString(target, 48) + " / " + truncateString(source, 48)
}

func joinObservationIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return strings.Join(parts, ", ")
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
