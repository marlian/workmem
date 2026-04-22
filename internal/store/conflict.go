package store

import (
	"database/sql"
	"strings"
)

// ConflictHint describes an existing observation on the same entity whose
// lexical similarity to a new content exceeds the conflict threshold.
// Surfaced on `remember` responses so the agent can decide whether to
// `forget` the prior fact. See DECISION_LOG 2026-04-22.
type ConflictHint struct {
	ObservationID int64   `json:"observation_id"`
	Similarity    float64 `json:"similarity"`
	Snippet       string  `json:"snippet"`
}

// conflictHintMinScore is the minimum composite similarity for an existing
// observation to qualify as a possible conflict. Intentionally conservative
// at launch — calibrated via telemetry (DECISION_LOG 2026-04-22). Tuning
// this value is implementation-internal and does not constitute a contract
// change.
const conflictHintMinScore = 0.6

// conflictHintMaxResults caps the number of hints surfaced per remember.
const conflictHintMaxResults = 3

// (Snippet length is derived at call time from the same
// compactSnippetLength() helper compact recall uses, so `COMPACT_SNIPPET_LENGTH`
// env overrides apply uniformly across both surfaces. No separate constant.)

// DetectEntityConflicts scans non-deleted observations attached to entityID
// and returns up to conflictHintMaxResults that resemble content above the
// conflict threshold. Read-only — does not touch access counts, does not
// modify state, does not emit telemetry.
//
// The detector reuses the composite ranker's lexical channels (fts_phrase,
// fts, content_like) scoped to entity_id = entityID. Entity-identity and
// event-based channels are skipped because they are constant or irrelevant
// inside a single entity.
//
// Callers should invoke this BEFORE inserting the new observation so that
// self-match filtering is unnecessary.
func DetectEntityConflicts(db *sql.DB, entityID int64, content string) ([]ConflictHint, error) {
	trimmed := strings.TrimSpace(content)
	if entityID <= 0 || trimmed == "" {
		return nil, nil
	}

	candidates, err := collectEntityScopedCandidates(db, entityID, trimmed)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	hydrated, err := HydrateCandidates(db, candidates)
	if err != nil {
		return nil, err
	}
	if len(hydrated) == 0 {
		return nil, nil
	}

	// Preserve every candidate through scoring so we can threshold-filter
	// next. Passing len(hydrated) as the limit means no truncation here.
	ranked := ScoreCandidates(hydrated, candidates, memoryHalfLifeWeeks(), len(hydrated))

	hints := make([]ConflictHint, 0, conflictHintMaxResults)
	for _, obs := range ranked {
		if obs.CompositeScore < conflictHintMinScore {
			continue
		}
		snippet, _ := compactContent(obs.Content, true, compactSnippetLength())
		hints = append(hints, ConflictHint{
			ObservationID: obs.ID,
			Similarity:    obs.CompositeScore,
			Snippet:       snippet,
		})
		if len(hints) >= conflictHintMaxResults {
			break
		}
	}
	return hints, nil
}

// collectEntityScopedCandidates runs fts_phrase, fts, and content_like
// channels against observations belonging to a single entity. Mirrors
// CollectCandidates in search.go but with a tighter scope and fewer
// channels — entity-identity and event-based channels are skipped as
// they are constant or irrelevant inside a single entity.
func collectEntityScopedCandidates(db *sql.DB, entityID int64, content string) (map[int64]*candidate, error) {
	candidates := map[int64]*candidate{}
	const (
		maxCandidates = 50
		collectLimit  = 20
	)

	terms := strings.Fields(content)
	cleanTerms := stripQuotes(terms)
	if len(cleanTerms) == 0 {
		return candidates, nil
	}

	// FTS error handling note: both FTS channels silence Query errors via
	// `if err == nil`, mirroring the pattern in search.go so conflict
	// detection degrades the same way recall does when the FTS layer is
	// unavailable. The calibration consequence is that an FTS outage shows
	// up in telemetry as a run of `conflicts_surfaced = 0` rather than as
	// errors — review the surface-to-act ratio in light of any known FTS
	// incident before pinning thresholds from that period.

	// fts_phrase — full phrase, only meaningful with multiple terms.
	if len(cleanTerms) > 1 {
		phraseQuery := `"` + strings.Join(cleanTerms, " ") + `"`
		rows, err := db.Query(`
			SELECT memory_fts.rowid, memory_fts.rank
			FROM memory_fts
			JOIN observations o ON o.id = memory_fts.rowid
			WHERE memory_fts MATCH ? AND o.entity_id = ? AND o.deleted_at IS NULL
			ORDER BY memory_fts.rank LIMIT ?
		`, phraseQuery, entityID, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts_phrase", maxCandidates); err != nil {
				return nil, err
			}
		}
	}

	// fts — any term.
	ftsQuery := strings.Join(quoteTerms(cleanTerms), " OR ")
	if ftsQuery != "" {
		rows, err := db.Query(`
			SELECT memory_fts.rowid, memory_fts.rank
			FROM memory_fts
			JOIN observations o ON o.id = memory_fts.rowid
			WHERE memory_fts MATCH ? AND o.entity_id = ? AND o.deleted_at IS NULL
			ORDER BY memory_fts.rank LIMIT ?
		`, ftsQuery, entityID, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts", maxCandidates); err != nil {
				return nil, err
			}
		}
	}

	// content_like — scoped substring per term, catches reformulations FTS
	// tokenization may have missed.
	for _, term := range cleanTerms {
		if len(candidates) >= maxCandidates {
			break
		}
		if err := collectSimpleIDs(db, candidates, maxCandidates, "content_like", `
			SELECT o.id FROM observations o
			WHERE o.entity_id = ? AND o.deleted_at IS NULL AND o.content LIKE ?
			ORDER BY o.id LIMIT ?
		`, entityID, "%"+term+"%", collectLimit); err != nil {
			return nil, err
		}
	}

	return candidates, nil
}
