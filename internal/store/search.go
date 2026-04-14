package store

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

var ChannelWeights = map[string]float64{
	"fts_phrase":   1.15,
	"fts":          1.0,
	"entity_exact": 0.9,
	"entity_like":  0.7,
	"content_like": 0.5,
	"type_like":    0.45,
	"event_label":  0.4,
}

const collectionMultiplier = 3

type candidate struct {
	ID                int64
	Channels          map[string]struct{}
	BestChannel       string
	BestChannelWeight float64
	FTSPosition       *int
}

// SearchMetrics captures per-call ranking-pipeline observations, suitable for
// telemetry. SearchMemory populates every field except Compact (which depends
// on tool args and is set by the caller).
type SearchMetrics struct {
	Query           string
	Channels        map[string]int
	CandidatesTotal int
	ResultsReturned int
	LimitRequested  int
	ScoreMin        float64
	ScoreMax        float64
	ScoreMedian     float64
	Compact         bool
}

type SearchObservation struct {
	ID                  int64
	EntityID            int64
	EntityName          string
	EntityType          string
	Content             string
	Source              string
	Confidence          float64
	AccessCount         int64
	CreatedAt           string
	EventID             *int64
	EventLabel          string
	EventDate           string
	EventType           string
	EffectiveConfidence float64
	CompositeScore      float64
}

type RecallObservation struct {
	ID             int64   `json:"id"`
	Content        string  `json:"content"`
	Confidence     float64 `json:"confidence"`
	CompositeScore float64 `json:"composite_score"`
	Source         string  `json:"source"`
	AccessCount    int64   `json:"access_count"`
	CreatedAt      string  `json:"created_at"`
	Truncated      bool    `json:"truncated,omitempty"`
	EventID        *int64  `json:"event_id,omitempty"`
	EventLabel     string  `json:"event_label,omitempty"`
	EventDate      string  `json:"event_date,omitempty"`
}

type RecallEntityGroup struct {
	EntityName   string              `json:"entity_name"`
	EntityType   string              `json:"entity_type"`
	Observations []RecallObservation `json:"observations"`
}

type RecallResponse struct {
	Results    []RecallEntityGroup `json:"results"`
	TotalFacts int                 `json:"total_facts"`
	Compact    bool                `json:"compact,omitempty"`
	Hint       string              `json:"hint,omitempty"`
}

func DecayedConfidence(createdAt string, confidence float64, accessCount int64, halfLifeWeeks float64) float64 {
	if createdAt == "" {
		return confidence
	}
	when, err := parseSQLiteTime(createdAt)
	if err != nil {
		return confidence
	}
	hl := halfLifeWeeks
	if hl == 0 {
		hl = memoryHalfLifeWeeks()
	}
	ageWeeks := time.Since(when).Hours() / (24 * 7)
	stability := hl * (1 + math.Log2(float64(accessCount)+1))
	if stability <= 0 {
		return confidence
	}
	return confidence * math.Pow(0.5, ageWeeks/stability)
}

func FTSPositionScore(position, totalFtsResults int) float64 {
	if totalFtsResults <= 1 {
		return 1.0
	}
	return 1.0 - (float64(position) / float64(totalFtsResults-1))
}

func CompositeScore(item *candidate, memoryScore float64, totalFtsResults int) float64 {
	relevance := item.BestChannelWeight
	if item.FTSPosition != nil && totalFtsResults > 0 {
		relevance += FTSPositionScore(*item.FTSPosition, totalFtsResults) * 0.08
	}
	channelBonus := math.Min(0.10, float64(len(item.Channels)-1)*0.03)
	relevance += channelBonus
	return relevance*0.7 + memoryScore*0.3
}

func SanitizeSearchLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func CollectCandidates(db *sql.DB, query string, collectLimit, maxCandidates int) (map[int64]*candidate, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return map[int64]*candidate{}, nil
	}

	candidates := map[int64]*candidate{}
	terms := strings.Fields(trimmed)

	if len(terms) > 1 {
		phraseQuery := `"` + strings.Join(stripQuotes(terms), " ") + `"`
		rows, err := db.Query(`SELECT rowid, rank FROM memory_fts WHERE memory_fts MATCH ? ORDER BY rank LIMIT ?`, phraseQuery, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts_phrase", maxCandidates); err != nil {
				return nil, err
			}
		}
	}

	ftsQuery := strings.Join(quoteTerms(stripQuotes(terms)), " OR ")
	if ftsQuery != "" {
		rows, err := db.Query(`SELECT rowid, rank FROM memory_fts WHERE memory_fts MATCH ? ORDER BY rank LIMIT ?`, ftsQuery, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts", maxCandidates); err != nil {
				return nil, err
			}
		}
	}

	if err := collectSimpleIDs(db, candidates, maxCandidates, "entity_exact", `
		SELECT o.id FROM observations o
		JOIN entities e ON o.entity_id = e.id
		WHERE o.deleted_at IS NULL AND e.deleted_at IS NULL AND e.name = ? COLLATE NOCASE
		ORDER BY o.id LIMIT ?
	`, trimmed, collectLimit); err != nil {
		return nil, err
	}

	if len(candidates) < maxCandidates {
		if err := collectSimpleIDs(db, candidates, maxCandidates, "entity_like", `
			SELECT o.id FROM observations o
			JOIN entities e ON o.entity_id = e.id
			WHERE o.deleted_at IS NULL AND e.deleted_at IS NULL AND e.name LIKE ? COLLATE NOCASE AND e.name != ? COLLATE NOCASE
			ORDER BY o.id LIMIT ?
		`, "%"+trimmed+"%", trimmed, collectLimit); err != nil {
			return nil, err
		}
	}

	if len(candidates) < maxCandidates {
		for _, term := range terms {
			if len(candidates) >= maxCandidates {
				break
			}
			if err := collectSimpleIDs(db, candidates, maxCandidates, "content_like", `
				SELECT o.id FROM observations o
				WHERE o.deleted_at IS NULL AND o.content LIKE ?
				ORDER BY o.id LIMIT ?
			`, "%"+term+"%", collectLimit); err != nil {
				return nil, err
			}
		}
	}

	if len(candidates) < maxCandidates {
		for _, term := range terms {
			if len(candidates) >= maxCandidates {
				break
			}
			if err := collectSimpleIDs(db, candidates, maxCandidates, "type_like", `
				SELECT o.id FROM observations o
				JOIN entities e ON o.entity_id = e.id
				WHERE o.deleted_at IS NULL AND e.deleted_at IS NULL AND e.entity_type LIKE ?
				ORDER BY o.id LIMIT ?
			`, "%"+term+"%", collectLimit); err != nil {
				return nil, err
			}
		}
	}

	if len(candidates) < maxCandidates {
		if err := collectSimpleIDs(db, candidates, maxCandidates, "event_label", `
			SELECT o.id FROM observations o
			JOIN events ev ON o.event_id = ev.id
			WHERE o.deleted_at IS NULL AND ev.label LIKE ?
			ORDER BY o.id LIMIT ?
		`, "%"+trimmed+"%", collectLimit); err != nil {
			return nil, err
		}
	}

	return candidates, nil
}

func HydrateCandidates(db *sql.DB, candidateMap map[int64]*candidate) ([]SearchObservation, error) {
	if len(candidateMap) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(candidateMap))
	for id := range candidateMap {
		ids = append(ids, id)
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.Query(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.content, o.source, o.confidence, o.access_count, o.created_at,
		       ev.id, ev.label, ev.event_date, ev.event_type,
		       e.name, e.entity_type
		FROM observations o
		JOIN entities e ON o.entity_id = e.id
		LEFT JOIN events ev ON o.event_id = ev.id
		WHERE o.id IN (%s) AND o.deleted_at IS NULL AND e.deleted_at IS NULL
	`, placeholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("hydrate candidates: %w", err)
	}
	defer rows.Close()

	results := make([]SearchObservation, 0, len(ids))
	for rows.Next() {
		var item SearchObservation
		var eventID sql.NullInt64
		var eventLabel sql.NullString
		var eventDate sql.NullString
		var eventType sql.NullString
		var entityType sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.EntityID,
			&item.Content,
			&item.Source,
			&item.Confidence,
			&item.AccessCount,
			&item.CreatedAt,
			&eventID,
			&eventLabel,
			&eventDate,
			&eventType,
			&item.EntityName,
			&entityType,
		); err != nil {
			return nil, fmt.Errorf("scan hydrated candidate: %w", err)
		}
		item.EntityType = entityType.String
		if eventID.Valid {
			id := eventID.Int64
			item.EventID = &id
		}
		item.EventLabel = eventLabel.String
		item.EventDate = eventDate.String
		item.EventType = eventType.String
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hydrated candidates: %w", err)
	}
	return results, nil
}

func ScoreCandidates(observations []SearchObservation, candidateMap map[int64]*candidate, halfLifeWeeks float64, limit int) []SearchObservation {
	totalFtsResults := 0
	for _, item := range candidateMap {
		if item.FTSPosition != nil {
			totalFtsResults++
		}
	}

	ranked := make([]SearchObservation, 0, len(observations))
	for _, obs := range observations {
		candidate := candidateMap[obs.ID]
		obs.EffectiveConfidence = DecayedConfidence(obs.CreatedAt, obs.Confidence, obs.AccessCount, halfLifeWeeks)
		obs.CompositeScore = CompositeScore(candidate, obs.EffectiveConfidence, totalFtsResults)
		ranked = append(ranked, obs)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].CompositeScore != ranked[j].CompositeScore {
			return ranked[i].CompositeScore > ranked[j].CompositeScore
		}
		if ranked[i].EffectiveConfidence != ranked[j].EffectiveConfidence {
			return ranked[i].EffectiveConfidence > ranked[j].EffectiveConfidence
		}
		return ranked[i].ID < ranked[j].ID
	})

	if limit < len(ranked) {
		return ranked[:limit]
	}
	return ranked
}

func SearchMemory(db *sql.DB, query string, limit int, halfLifeWeeks float64) ([]SearchObservation, SearchMetrics, error) {
	requestedLimit := limit
	limit = SanitizeSearchLimit(limit)
	emptyMetrics := SearchMetrics{Query: query, LimitRequested: requestedLimit, Channels: map[string]int{}}
	if limit <= 0 {
		return []SearchObservation{}, emptyMetrics, nil
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return []SearchObservation{}, emptyMetrics, nil
	}

	collectLimit := limit * collectionMultiplier
	maxCandidates := collectLimit
	if maxCandidates < 200 {
		maxCandidates = 200
	}
	candidates, err := CollectCandidates(db, trimmed, collectLimit, maxCandidates)
	if err != nil {
		return nil, SearchMetrics{}, err
	}
	channelCounts := countCandidateChannels(candidates)
	hydrated, err := HydrateCandidates(db, candidates)
	if err != nil {
		return nil, SearchMetrics{}, err
	}
	ranked := ScoreCandidates(hydrated, candidates, halfLifeWeeks, limit)
	returnedIDs := make([]int64, 0, len(ranked))
	for _, item := range ranked {
		returnedIDs = append(returnedIDs, item.ID)
	}
	if err := TouchObservations(db, returnedIDs); err != nil {
		return nil, SearchMetrics{}, err
	}
	metrics := SearchMetrics{
		Query:           query,
		Channels:        channelCounts,
		CandidatesTotal: len(candidates),
		ResultsReturned: len(ranked),
		LimitRequested:  requestedLimit,
		ScoreMin:        scoreMin(ranked),
		ScoreMax:        scoreMax(ranked),
		ScoreMedian:     scoreMedian(ranked),
	}
	return ranked, metrics, nil
}

func countCandidateChannels(m map[int64]*candidate) map[string]int {
	out := make(map[string]int)
	for _, c := range m {
		for ch := range c.Channels {
			out[ch]++
		}
	}
	return out
}

// ranked is sorted descending by CompositeScore; the three helpers below
// exploit that ordering so we avoid a re-sort just for metrics.

func scoreMax(rs []SearchObservation) float64 {
	if len(rs) == 0 {
		return 0
	}
	return rs[0].CompositeScore
}

func scoreMin(rs []SearchObservation) float64 {
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1].CompositeScore
}

func scoreMedian(rs []SearchObservation) float64 {
	n := len(rs)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return rs[n/2].CompositeScore
	}
	return (rs[n/2-1].CompositeScore + rs[n/2].CompositeScore) / 2
}

func GroupResults(results []SearchObservation, compact bool) RecallResponse {
	groups := make(map[string]int)
	ordered := make([]RecallEntityGroup, 0)

	// Resolve once per call — COMPACT_SNIPPET_LENGTH does not change within a request.
	snippetLimit := compactSnippetLength()

	for _, result := range results {
		index, exists := groups[result.EntityName]
		if !exists {
			index = len(ordered)
			groups[result.EntityName] = index
			ordered = append(ordered, RecallEntityGroup{
				EntityName: result.EntityName,
				EntityType: result.EntityType,
			})
		}

		content, truncated := compactContent(result.Content, compact, snippetLimit)

		ordered[index].Observations = append(ordered[index].Observations, RecallObservation{
			ID:             result.ID,
			Content:        content,
			Confidence:     result.EffectiveConfidence,
			CompositeScore: result.CompositeScore,
			Source:         result.Source,
			AccessCount:    result.AccessCount,
			CreatedAt:      result.CreatedAt,
			Truncated:      truncated,
			EventID:        result.EventID,
			EventLabel:     result.EventLabel,
			EventDate:      result.EventDate,
		})
	}

	response := RecallResponse{Results: ordered, TotalFacts: len(results), Compact: compact}
	if len(results) == 0 {
		response.Hint = "No results found. Try list_entities to browse available entities, or use broader search terms."
	}
	return response
}

func compactContent(content string, compact bool, limit int) (string, bool) {
	if !compact {
		return content, false
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content, false
	}
	return string(runes[:limit-1]) + "…", true
}

func addCandidate(candidateMap map[int64]*candidate, id int64, channel string, maxCandidates int, ftsPosition *int) {
	existing, ok := candidateMap[id]
	if !ok {
		if len(candidateMap) >= maxCandidates {
			return
		}
		weight := ChannelWeights[channel]
		candidateMap[id] = &candidate{
			ID:                id,
			Channels:          map[string]struct{}{channel: {}},
			BestChannel:       channel,
			BestChannelWeight: weight,
			FTSPosition:       ftsPosition,
		}
		return
	}

	existing.Channels[channel] = struct{}{}
	if ChannelWeights[channel] > existing.BestChannelWeight {
		existing.BestChannel = channel
		existing.BestChannelWeight = ChannelWeights[channel]
	}
	if ftsPosition != nil && (existing.FTSPosition == nil || *ftsPosition < *existing.FTSPosition) {
		position := *ftsPosition
		existing.FTSPosition = &position
	}
}

func collectFTSRows(rows *sql.Rows, candidateMap map[int64]*candidate, channel string, maxCandidates int) error {
	defer rows.Close()
	position := 0
	for rows.Next() {
		var id int64
		var rank float64
		if err := rows.Scan(&id, &rank); err != nil {
			return fmt.Errorf("scan FTS row: %w", err)
		}
		p := position
		addCandidate(candidateMap, id, channel, maxCandidates, &p)
		position++
	}
	return rows.Err()
}

func collectSimpleIDs(db *sql.DB, candidateMap map[int64]*candidate, maxCandidates int, channel string, query string, args ...any) error {
	rows, err := db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("collect %s ids: %w", channel, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan %s id: %w", channel, err)
		}
		addCandidate(candidateMap, id, channel, maxCandidates, nil)
	}
	return rows.Err()
}

// stripQuotes removes all quote-like characters from search terms
// to prevent FTS5 syntax errors from user input.
func stripQuotes(terms []string) []string {
	clean := make([]string, 0, len(terms))
	for _, term := range terms {
		replaced := strings.Map(func(r rune) rune {
			switch r {
			case '"', '\u201c', '\u201d', '\u201e', '\u201f', '\u00ab', '\u00bb', '\u2039', '\u203a':
				return -1 // drop
			default:
				return r
			}
		}, term)
		if replaced != "" {
			clean = append(clean, replaced)
		}
	}
	return clean
}

func quoteTerms(terms []string) []string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" {
			continue
		}
		quoted = append(quoted, `"`+term+`"`)
	}
	return quoted
}

func parseSQLiteTime(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		var parsed time.Time
		var err error
		if layout == time.RFC3339Nano {
			parsed, err = time.Parse(layout, value)
		} else {
			parsed, err = time.ParseInLocation(layout, value, time.UTC)
		}
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported sqlite timestamp %q", value)
}
