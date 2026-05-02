package store

import (
	"fmt"
	"strings"
)

// ConflictHint describes an existing observation on the same entity whose
// lexical similarity to a new content exceeds the conflict threshold.
// Surfaced on `remember` responses as a review hint, not as an instruction to
// mutate memory. Deletion still goes through `forget`; reversible supersession
// belongs to the reconcile audit flow. See DECISION_LOG 2026-04-22 and
// 2026-05-02.
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
// halfLifeWeeks is the decay rate the caller computed from request scope
// (global ⇒ memoryHalfLifeWeeks(), project ⇒ projectMemoryHalfLifeWeeks()).
// Mirrors the explicit-parameter pattern of SearchMemory so the detector
// scores observations against the same decay surface the agent's recall
// would use, not a hardcoded global default. If the wrong rate is supplied
// here, project-scoped supersession hints get suppressed because the older
// observations decay below the conflict threshold under global decay.
//
// Callers should invoke this BEFORE inserting the new observation so that
// self-match filtering is unnecessary.
func DetectEntityConflicts(db dbtx, entityID int64, content string, halfLifeWeeks float64) ([]ConflictHint, error) {
	hints, _, err := DetectEntityConflictsWithDiagnostics(db, entityID, content, halfLifeWeeks)
	return hints, err
}

func DetectEntityConflictsWithDiagnostics(db dbtx, entityID int64, content string, halfLifeWeeks float64) ([]ConflictHint, int, error) {
	trimmed := strings.TrimSpace(content)
	if entityID <= 0 || trimmed == "" {
		return nil, 0, nil
	}

	candidates, diagnostics, err := collectEntityScopedCandidatesWithDiagnostics(db, entityID, trimmed)
	if err != nil {
		return nil, diagnostics.FTSQueryErrors, err
	}
	if len(candidates) == 0 {
		return nil, diagnostics.FTSQueryErrors, nil
	}

	hydrated, err := HydrateCandidates(db, candidates)
	if err != nil {
		return nil, diagnostics.FTSQueryErrors, err
	}
	if len(hydrated) == 0 {
		return nil, diagnostics.FTSQueryErrors, nil
	}

	// Preserve every candidate through scoring so we can threshold-filter
	// next. Passing len(hydrated) as the limit means no truncation here.
	ranked := ScoreCandidates(hydrated, candidates, halfLifeWeeks, len(hydrated))

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
	return hints, diagnostics.FTSQueryErrors, nil
}

// collectEntityScopedCandidates runs fts_phrase, fts, and content_like
// channels against observations belonging to a single entity. Mirrors
// CollectCandidates in search.go but with a tighter scope and fewer
// channels — entity-identity and event-based channels are skipped as
// they are constant or irrelevant inside a single entity.
func collectEntityScopedCandidates(db dbtx, entityID int64, content string) (map[int64]*candidate, error) {
	candidates, _, err := collectEntityScopedCandidatesWithDiagnostics(db, entityID, content)
	return candidates, err
}

func collectEntityScopedCandidatesWithDiagnostics(db dbtx, entityID int64, content string) (map[int64]*candidate, collectionDiagnostics, error) {
	candidates := map[int64]*candidate{}
	diagnostics := collectionDiagnostics{}
	const (
		maxCandidates = 50
		collectLimit  = 20
	)

	terms := strings.Fields(content)
	cleanTerms := stripQuotes(terms)
	if len(cleanTerms) == 0 {
		return candidates, diagnostics, nil
	}

	// FTS query errors are non-fatal here: conflict detection degrades to the
	// content_like fallback channel, and diagnostics preserve an observable
	// signal for remember telemetry (`conflict_fts_query_errors`). Row scan or
	// iteration errors after a successful query still return hard errors.

	// fts_phrase — full phrase, only meaningful with multiple terms.
	if len(cleanTerms) > 1 {
		phraseQuery := `"` + strings.Join(cleanTerms, " ") + `"`
		rows, err := db.Query(fmt.Sprintf(`
			SELECT memory_fts.rowid, memory_fts.rank
			FROM memory_fts
			JOIN observations o ON o.id = memory_fts.rowid
			JOIN entities e ON o.entity_id = e.id
			WHERE memory_fts MATCH ? AND o.entity_id = ? AND %s AND e.deleted_at IS NULL
			ORDER BY memory_fts.rank LIMIT ?
		`, activeObservationSQL("o")), phraseQuery, entityID, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts_phrase", maxCandidates); err != nil {
				return nil, diagnostics, err
			}
		} else {
			diagnostics.FTSQueryErrors++
		}
	}

	// fts — any term.
	ftsQuery := strings.Join(quoteTerms(cleanTerms), " OR ")
	if ftsQuery != "" {
		rows, err := db.Query(fmt.Sprintf(`
			SELECT memory_fts.rowid, memory_fts.rank
			FROM memory_fts
			JOIN observations o ON o.id = memory_fts.rowid
			JOIN entities e ON o.entity_id = e.id
			WHERE memory_fts MATCH ? AND o.entity_id = ? AND %s AND e.deleted_at IS NULL
			ORDER BY memory_fts.rank LIMIT ?
		`, activeObservationSQL("o")), ftsQuery, entityID, collectLimit)
		if err == nil {
			if err := collectFTSRows(rows, candidates, "fts", maxCandidates); err != nil {
				return nil, diagnostics, err
			}
		} else {
			diagnostics.FTSQueryErrors++
		}
	}

	// content_like — scoped substring per term, catches reformulations FTS
	// tokenization may have missed.
	for _, term := range cleanTerms {
		if len(candidates) >= maxCandidates {
			break
		}
		if err := collectSimpleIDs(db, candidates, maxCandidates, "content_like", fmt.Sprintf(`
			SELECT o.id FROM observations o
			JOIN entities e ON o.entity_id = e.id
			WHERE o.entity_id = ? AND %s AND e.deleted_at IS NULL AND o.content LIKE ? ESCAPE '\'
			ORDER BY o.id LIMIT ?
		`, activeObservationSQL("o")), entityID, "%"+escapeLikePattern(term)+"%", collectLimit); err != nil {
			return nil, diagnostics, err
		}
	}

	return candidates, diagnostics, nil
}
