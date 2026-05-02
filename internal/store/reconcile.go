package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ReconcileActionProposed                    = "proposed"
	ReconcileActionApplied                     = "applied"
	ReconcileRationaleExactDuplicateSameEntity = "exact_duplicate_same_entity"
	ReconcileDecisionKindExactDuplicate        = "exact_duplicate"
	ReconcileMaxEntitiesPerRunLimit            = 900
)

type ReconcileProposeOptions struct {
	GeneratedAt       time.Time
	Since             time.Duration
	SinceLabel        string
	MinObsPerEntity   int
	MaxEntitiesPerRun int
	Scope             string
}

type ReconcileProposeReport struct {
	GeneratedAt        time.Time
	Mode               string
	Scope              string
	Since              time.Duration
	SinceLabel         string
	ScannedEntities    []ReconcileEntitySignal
	DuplicateGroups    []ReconcileDuplicateGroup
	CandidatesProposed int
}

type ReconcileEntitySignal struct {
	EntityID           int64
	Name               string
	EntityType         string
	ActiveObservations int
	RecentObservations int
	LastObservationAt  string
}

type ReconcileDuplicateGroup struct {
	EntityID   int64
	EntityName string
	EntityType string
	Content    string
	Action     string
	Rationale  string
	Target     ReconcileObservation
	Sources    []ReconcileObservation
}

type ReconcileObservation struct {
	ID         int64
	Source     string
	Confidence float64
	EventID    *int64
	CreatedAt  string
}

type ReconcileApplyOptions struct {
	GeneratedAt       time.Time
	Since             time.Duration
	SinceLabel        string
	MinObsPerEntity   int
	MaxEntitiesPerRun int
	Scope             string
	TriggerSource     string
}

type ReconcileApplyResult struct {
	RunID                int64
	ScannedEntities      int
	CandidatesProposed   int
	DecisionsRecorded    int
	SupersessionsApplied int
}

type ReconcileRollbackOptions struct {
	RunID         int64
	Scope         string
	TriggerSource string
}

type ReconcileRollbackResult struct {
	RunID                 int64
	RolledBackRunID       int64
	DecisionsReverted     int
	SupersessionsRestored int
}

type reconcileValidationObservation struct {
	ID              int64
	EntityID        int64
	Content         string
	SupersededBy    sql.NullInt64
	SupersededByRun sql.NullInt64
}

func BuildReconcileProposeReport(db *sql.DB, options ReconcileProposeOptions) (*ReconcileProposeReport, error) {
	if db == nil {
		return nil, fmt.Errorf("reconcile propose: nil db")
	}
	return buildReconcileProposeReport(db, options)
}

func buildReconcileProposeReport(db dbtx, options ReconcileProposeOptions) (*ReconcileProposeReport, error) {
	now := options.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	since := options.Since
	if since <= 0 {
		since = 30 * 24 * time.Hour
	}
	sinceLabel := strings.TrimSpace(options.SinceLabel)
	if sinceLabel == "" {
		sinceLabel = since.String()
	}
	minObsPerEntity := options.MinObsPerEntity
	if minObsPerEntity <= 0 {
		minObsPerEntity = 2
	}
	maxEntitiesPerRun := options.MaxEntitiesPerRun
	if maxEntitiesPerRun <= 0 {
		maxEntitiesPerRun = 50
	}
	if maxEntitiesPerRun > ReconcileMaxEntitiesPerRunLimit {
		return nil, fmt.Errorf("reconcile propose: max entities per run %d exceeds limit %d", maxEntitiesPerRun, ReconcileMaxEntitiesPerRunLimit)
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		scope = "global"
	}

	asOf := now.UTC().Format(sqliteTimestampLayout)
	cutoff := now.Add(-since).UTC().Format(sqliteTimestampLayout)
	signals, err := selectReconcileEntitySignals(db, cutoff, asOf, minObsPerEntity, maxEntitiesPerRun)
	if err != nil {
		return nil, err
	}
	groups, candidates, err := selectExactDuplicateGroups(db, signals, asOf)
	if err != nil {
		return nil, err
	}

	return &ReconcileProposeReport{
		GeneratedAt:        now.UTC(),
		Mode:               "propose",
		Scope:              scope,
		Since:              since,
		SinceLabel:         sinceLabel,
		ScannedEntities:    signals,
		DuplicateGroups:    groups,
		CandidatesProposed: candidates,
	}, nil
}

func ApplyExactDuplicateReconcile(db *sql.DB, options ReconcileApplyOptions) (*ReconcileApplyResult, error) {
	if db == nil {
		return nil, fmt.Errorf("reconcile apply: nil db")
	}
	started := time.Now()
	mutationNow := started.UTC()
	auditAt := options.GeneratedAt
	if auditAt.IsZero() {
		auditAt = mutationNow
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		scope = "global"
	}
	triggerSource := strings.TrimSpace(options.TriggerSource)
	if triggerSource == "" {
		triggerSource = "cli"
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin reconcile apply: %w", err)
	}
	defer tx.Rollback()

	report, err := buildReconcileProposeReport(tx, ReconcileProposeOptions{
		GeneratedAt:       mutationNow,
		Since:             options.Since,
		SinceLabel:        options.SinceLabel,
		MinObsPerEntity:   options.MinObsPerEntity,
		MaxEntitiesPerRun: options.MaxEntitiesPerRun,
		Scope:             scope,
	})
	if err != nil {
		return nil, err
	}
	runID, err := insertReconcileRun(tx, auditAt, "apply", triggerSource, scope, len(report.ScannedEntities), report.CandidatesProposed, 0, 0, "")
	if err != nil {
		return nil, err
	}

	asOf := mutationNow.UTC().Format(sqliteTimestampLayout)
	supersessionsApplied := 0
	decisionsRecorded := 0
	for _, group := range report.DuplicateGroups {
		if len(group.Sources) == 0 {
			continue
		}
		if err := validateAndApplyExactDuplicateGroup(tx, runID, mutationNow, asOf, group); err != nil {
			return nil, err
		}
		if err := insertReconcileDecision(tx, runID, group); err != nil {
			return nil, err
		}
		supersessionsApplied += len(group.Sources)
		decisionsRecorded++
	}
	durationMS := int(time.Since(started).Milliseconds())
	if _, err := tx.Exec(`UPDATE reconcile_runs SET supersessions_applied = ?, duration_ms = ? WHERE id = ?`, supersessionsApplied, durationMS, runID); err != nil {
		return nil, fmt.Errorf("update reconcile apply run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit reconcile apply: %w", err)
	}
	return &ReconcileApplyResult{
		RunID:                runID,
		ScannedEntities:      len(report.ScannedEntities),
		CandidatesProposed:   report.CandidatesProposed,
		DecisionsRecorded:    decisionsRecorded,
		SupersessionsApplied: supersessionsApplied,
	}, nil
}

func RollbackReconcileRun(db *sql.DB, options ReconcileRollbackOptions) (*ReconcileRollbackResult, error) {
	if db == nil {
		return nil, fmt.Errorf("reconcile rollback: nil db")
	}
	if options.RunID <= 0 {
		return nil, fmt.Errorf("reconcile rollback: run id must be > 0")
	}
	now := time.Now().UTC()
	triggerSource := strings.TrimSpace(options.TriggerSource)
	if triggerSource == "" {
		triggerSource = "cli"
	}
	started := time.Now()

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin reconcile rollback: %w", err)
	}
	defer tx.Rollback()

	originalScope, err := loadApplyRunScope(tx, options.RunID)
	if err != nil {
		return nil, err
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		scope = originalScope
	} else if scope != originalScope {
		return nil, fmt.Errorf("reconcile rollback: scope %q does not match original run scope %q", scope, originalScope)
	}
	decisions, err := loadApplyDecisionsForRollback(tx, options.RunID)
	if err != nil {
		return nil, err
	}
	if len(decisions) == 0 {
		return nil, fmt.Errorf("reconcile rollback: run %d has no applied decisions", options.RunID)
	}
	for _, decision := range decisions {
		if decision.RevertedAt.Valid {
			return nil, fmt.Errorf("reconcile rollback: decision %d from run %d was already reverted", decision.ID, options.RunID)
		}
	}
	rollbackRunID, err := insertReconcileRun(tx, now, "rollback", triggerSource, scope, 0, 0, 0, 0, fmt.Sprintf("rollback_of_run=%d", options.RunID))
	if err != nil {
		return nil, err
	}

	asOf := now.UTC().Format(sqliteTimestampLayout)
	restored := 0
	for _, decision := range decisions {
		count, err := rollbackReconcileDecision(tx, options.RunID, rollbackRunID, now, asOf, decision)
		if err != nil {
			return nil, err
		}
		restored += count
	}
	durationMS := int(time.Since(started).Milliseconds())
	if _, err := tx.Exec(`UPDATE reconcile_runs SET supersessions_applied = ?, duration_ms = ? WHERE id = ?`, restored, durationMS, rollbackRunID); err != nil {
		return nil, fmt.Errorf("update reconcile rollback run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit reconcile rollback: %w", err)
	}
	return &ReconcileRollbackResult{
		RunID:                 rollbackRunID,
		RolledBackRunID:       options.RunID,
		DecisionsReverted:     len(decisions),
		SupersessionsRestored: restored,
	}, nil
}

func selectReconcileEntitySignals(db dbtx, cutoff string, asOf string, minObsPerEntity int, maxEntitiesPerRun int) ([]ReconcileEntitySignal, error) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT e.id, e.name, e.entity_type,
		       COUNT(o.id) AS active_observations,
		       SUM(CASE WHEN datetime(o.created_at) >= datetime(?) THEN 1 ELSE 0 END) AS recent_observations,
		       MAX(o.created_at) AS last_observation_at
		FROM observations o
		JOIN entities e ON e.id = o.entity_id
		WHERE e.deleted_at IS NULL AND %s
		GROUP BY e.id
		HAVING COUNT(o.id) >= ? AND SUM(CASE WHEN datetime(o.created_at) >= datetime(?) THEN 1 ELSE 0 END) > 0
		ORDER BY datetime(last_observation_at) DESC, e.id DESC
		LIMIT ?
	`, activeObservationAsOfSQL("o")), cutoff, asOf, minObsPerEntity, cutoff, maxEntitiesPerRun)
	if err != nil {
		return nil, fmt.Errorf("select reconcile entity signals: %w", err)
	}
	defer rows.Close()

	signals := make([]ReconcileEntitySignal, 0)
	for rows.Next() {
		var signal ReconcileEntitySignal
		var entityType sql.NullString
		if err := rows.Scan(&signal.EntityID, &signal.Name, &entityType, &signal.ActiveObservations, &signal.RecentObservations, &signal.LastObservationAt); err != nil {
			return nil, fmt.Errorf("scan reconcile entity signal: %w", err)
		}
		signal.EntityType = nullableStringValue(entityType)
		signals = append(signals, signal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reconcile entity signals: %w", err)
	}
	return signals, nil
}

func selectExactDuplicateGroups(db dbtx, signals []ReconcileEntitySignal, asOf string) ([]ReconcileDuplicateGroup, int, error) {
	if len(signals) == 0 {
		return nil, 0, nil
	}
	entityIDs := make([]int64, 0, len(signals))
	entitySignals := make(map[int64]ReconcileEntitySignal, len(signals))
	for _, signal := range signals {
		entityIDs = append(entityIDs, signal.EntityID)
		entitySignals[signal.EntityID] = signal
	}
	placeholders := placeholders(len(entityIDs))
	args := make([]any, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		args = append(args, entityID)
	}
	args = append(args, asOf, asOf)

	rows, err := db.Query(fmt.Sprintf(`
		WITH duplicate_groups AS (
			SELECT o.entity_id, o.content
			FROM observations o
			JOIN entities e ON e.id = o.entity_id
			WHERE e.deleted_at IS NULL
			  AND o.entity_id IN (%s)
			  AND %s
			GROUP BY o.entity_id, o.content
			HAVING COUNT(o.id) > 1
		)
		SELECT o.id, o.entity_id, o.content, o.source, o.confidence, o.event_id, o.created_at
		FROM observations o
		JOIN duplicate_groups dg ON dg.entity_id = o.entity_id AND dg.content = o.content
		JOIN entities e ON e.id = o.entity_id
		WHERE e.deleted_at IS NULL AND %s
		ORDER BY o.entity_id, o.content, datetime(o.created_at) DESC, o.id DESC
	`, placeholders, activeObservationAsOfSQL("o"), activeObservationAsOfSQL("o")), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("select exact duplicate observations: %w", err)
	}
	defer rows.Close()

	groups := make([]ReconcileDuplicateGroup, 0)
	var currentEntityID int64
	var currentContent string
	var hasCurrent bool
	var current *ReconcileDuplicateGroup
	for rows.Next() {
		var entityID int64
		var content string
		var source sql.NullString
		var confidence sql.NullFloat64
		var eventID sql.NullInt64
		var observation ReconcileObservation
		if err := rows.Scan(&observation.ID, &entityID, &content, &source, &confidence, &eventID, &observation.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan exact duplicate observation: %w", err)
		}
		observation.Source = nullableStringValue(source)
		if confidence.Valid {
			observation.Confidence = confidence.Float64
		}
		if eventID.Valid {
			value := eventID.Int64
			observation.EventID = &value
		}

		if !hasCurrent || entityID != currentEntityID || content != currentContent {
			if current != nil {
				groups = append(groups, *current)
			}
			signal := entitySignals[entityID]
			hasCurrent = true
			currentEntityID = entityID
			currentContent = content
			current = &ReconcileDuplicateGroup{
				EntityID:   entityID,
				EntityName: signal.Name,
				EntityType: signal.EntityType,
				Content:    content,
				Action:     ReconcileActionProposed,
				Rationale:  ReconcileRationaleExactDuplicateSameEntity,
				Target:     observation,
				Sources:    make([]ReconcileObservation, 0, 1),
			}
			continue
		}
		current.Sources = append(current.Sources, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate exact duplicate observations: %w", err)
	}
	if current != nil {
		groups = append(groups, *current)
	}

	candidates := 0
	for _, group := range groups {
		candidates += len(group.Sources)
	}
	return groups, candidates, nil
}

func validateAndApplyExactDuplicateGroup(db dbtx, runID int64, now time.Time, asOf string, group ReconcileDuplicateGroup) error {
	target, err := loadActiveObservationForReconcile(db, group.Target.ID, asOf)
	if err != nil {
		return fmt.Errorf("validate exact duplicate target %d: %w", group.Target.ID, err)
	}
	sourceIDs := make([]int64, 0, len(group.Sources))
	for _, source := range group.Sources {
		if source.ID == target.ID {
			return fmt.Errorf("validate exact duplicate source %d: self-supersession", source.ID)
		}
		sourceObservation, err := loadActiveObservationForReconcile(db, source.ID, asOf)
		if err != nil {
			return fmt.Errorf("validate exact duplicate source %d: %w", source.ID, err)
		}
		if sourceObservation.EntityID != target.EntityID {
			return fmt.Errorf("validate exact duplicate source %d: entity mismatch with target %d", source.ID, target.ID)
		}
		if sourceObservation.Content != target.Content {
			return fmt.Errorf("validate exact duplicate source %d: content mismatch with target %d", source.ID, target.ID)
		}
		sourceIDs = append(sourceIDs, source.ID)
	}
	if len(sourceIDs) == 0 {
		return fmt.Errorf("validate exact duplicate group for target %d: no source observations", target.ID)
	}
	supersededAt := now.UTC().Format(sqliteTimestampLayout)
	for _, sourceID := range sourceIDs {
		result, err := db.Exec(`
			UPDATE observations
			SET superseded_by = ?, superseded_at = ?, superseded_reason = ?, superseded_by_run = ?
			WHERE id = ? AND deleted_at IS NULL AND superseded_by IS NULL
		`, target.ID, supersededAt, ReconcileRationaleExactDuplicateSameEntity, runID, sourceID)
		if err != nil {
			return fmt.Errorf("apply exact duplicate supersession source %d: %w", sourceID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("apply exact duplicate supersession source %d rows affected: %w", sourceID, err)
		}
		if updated != 1 {
			return fmt.Errorf("apply exact duplicate supersession source %d: updated %d row(s), want 1", sourceID, updated)
		}
	}
	return nil
}

func insertReconcileDecision(db dbtx, runID int64, group ReconcileDuplicateGroup) error {
	sourceIDs := make([]int64, 0, len(group.Sources))
	for _, source := range group.Sources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	encodedSources, err := json.Marshal(sourceIDs)
	if err != nil {
		return fmt.Errorf("encode exact duplicate source ids: %w", err)
	}
	if _, err := db.Exec(`
		INSERT INTO reconcile_decisions (run_id, kind, entity_id, source_obs_ids, target_obs_id, content_snapshot, similarity, action, rationale)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runID, ReconcileDecisionKindExactDuplicate, group.EntityID, string(encodedSources), group.Target.ID, group.Content, 1.0, ReconcileActionApplied, ReconcileRationaleExactDuplicateSameEntity); err != nil {
		return fmt.Errorf("insert reconcile decision: %w", err)
	}
	return nil
}

type reconcileDecisionForRollback struct {
	ID              int64
	EntityID        sql.NullInt64
	SourceObsIDs    string
	TargetObsID     int64
	ContentSnapshot sql.NullString
	Kind            string
	Action          string
	Rationale       sql.NullString
	Similarity      sql.NullFloat64
	RunID           int64
	RevertedAt      sql.NullString
}

func insertReconcileRun(db dbtx, at time.Time, mode string, triggerSource string, scope string, scannedEntities int, candidatesProposed int, supersessionsApplied int, durationMS int, notes string) (int64, error) {
	result, err := db.Exec(`
		INSERT INTO reconcile_runs (ts, mode, trigger_source, scope, scanned_entities, candidates_proposed, supersessions_applied, duration_ms, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, at.UTC().Format(sqliteTimestampLayout), mode, nullableString(triggerSource, ""), scope, scannedEntities, candidatesProposed, supersessionsApplied, durationMS, nullableString(notes, ""))
	if err != nil {
		return 0, fmt.Errorf("insert reconcile run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reconcile run LastInsertId: %w", err)
	}
	return runID, nil
}

func loadActiveObservationForReconcile(db dbtx, observationID int64, asOf string) (reconcileValidationObservation, error) {
	var observation reconcileValidationObservation
	err := db.QueryRow(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.content, o.superseded_by, o.superseded_by_run
		FROM observations o
		JOIN entities e ON e.id = o.entity_id
		WHERE o.id = ? AND e.deleted_at IS NULL AND %s
	`, activeObservationAsOfSQL("o")), observationID, asOf).Scan(&observation.ID, &observation.EntityID, &observation.Content, &observation.SupersededBy, &observation.SupersededByRun)
	if errorsIsNoRows(err) {
		return reconcileValidationObservation{}, fmt.Errorf("observation is not active")
	}
	if err != nil {
		return reconcileValidationObservation{}, fmt.Errorf("load active observation: %w", err)
	}
	return observation, nil
}

func loadRollbackSourceObservation(db dbtx, observationID int64, asOf string) (reconcileValidationObservation, error) {
	var observation reconcileValidationObservation
	// Rollback sources are intentionally not "active" while superseded, so this
	// query reuses the entity/event/deleted lifecycle guards but omits the
	// superseded_by IS NULL predicate from activeObservationAsOfSQL.
	err := db.QueryRow(fmt.Sprintf(`
		SELECT o.id, o.entity_id, o.content, o.superseded_by, o.superseded_by_run
		FROM observations o
		JOIN entities e ON e.id = o.entity_id
		WHERE o.id = ?
		  AND e.deleted_at IS NULL
		  AND o.deleted_at IS NULL
		  AND (o.event_id IS NULL OR EXISTS (
			SELECT 1 FROM events ev_active
			WHERE ev_active.id = o.event_id AND %s
		  ))
	`, activeEventAsOfSQL("ev_active")), observationID, asOf).Scan(&observation.ID, &observation.EntityID, &observation.Content, &observation.SupersededBy, &observation.SupersededByRun)
	if errorsIsNoRows(err) {
		return reconcileValidationObservation{}, fmt.Errorf("observation cannot be restored to active visibility")
	}
	if err != nil {
		return reconcileValidationObservation{}, fmt.Errorf("load rollback source observation: %w", err)
	}
	return observation, nil
}

func loadApplyRunScope(db dbtx, runID int64) (string, error) {
	var mode string
	var scope string
	err := db.QueryRow(`SELECT mode, scope FROM reconcile_runs WHERE id = ?`, runID).Scan(&mode, &scope)
	if errorsIsNoRows(err) {
		return "", fmt.Errorf("reconcile rollback: run %d not found", runID)
	}
	if err != nil {
		return "", fmt.Errorf("load reconcile run %d: %w", runID, err)
	}
	if mode != "apply" {
		return "", fmt.Errorf("reconcile rollback: run %d has mode %q, want apply", runID, mode)
	}
	return scope, nil
}

func loadApplyDecisionsForRollback(db dbtx, runID int64) ([]reconcileDecisionForRollback, error) {
	rows, err := db.Query(`
		SELECT id, run_id, kind, entity_id, source_obs_ids, target_obs_id, content_snapshot, similarity, action, rationale, reverted_at
		FROM reconcile_decisions
		WHERE run_id = ?
		ORDER BY id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("select reconcile decisions for rollback: %w", err)
	}
	defer rows.Close()
	decisions := make([]reconcileDecisionForRollback, 0)
	for rows.Next() {
		var decision reconcileDecisionForRollback
		if err := rows.Scan(&decision.ID, &decision.RunID, &decision.Kind, &decision.EntityID, &decision.SourceObsIDs, &decision.TargetObsID, &decision.ContentSnapshot, &decision.Similarity, &decision.Action, &decision.Rationale, &decision.RevertedAt); err != nil {
			return nil, fmt.Errorf("scan reconcile decision for rollback: %w", err)
		}
		if decision.Kind != ReconcileDecisionKindExactDuplicate {
			return nil, fmt.Errorf("reconcile rollback: decision %d kind %q is not supported", decision.ID, decision.Kind)
		}
		if decision.Action != ReconcileActionApplied {
			return nil, fmt.Errorf("reconcile rollback: decision %d action %q is not supported", decision.ID, decision.Action)
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reconcile decisions for rollback: %w", err)
	}
	return decisions, nil
}

func rollbackReconcileDecision(db dbtx, originalRunID int64, rollbackRunID int64, now time.Time, asOf string, decision reconcileDecisionForRollback) (int, error) {
	var sourceIDs []int64
	if err := json.Unmarshal([]byte(decision.SourceObsIDs), &sourceIDs); err != nil {
		return 0, fmt.Errorf("rollback decision %d: decode source_obs_ids: %w", decision.ID, err)
	}
	if len(sourceIDs) == 0 {
		return 0, fmt.Errorf("rollback decision %d: source_obs_ids is empty", decision.ID)
	}
	if decision.RunID != originalRunID {
		return 0, fmt.Errorf("rollback decision %d: run_id %d does not match requested run %d", decision.ID, decision.RunID, originalRunID)
	}
	if decision.Kind != ReconcileDecisionKindExactDuplicate || decision.Action != ReconcileActionApplied {
		return 0, fmt.Errorf("rollback decision %d: unsupported kind/action %q/%q", decision.ID, decision.Kind, decision.Action)
	}
	if !decision.EntityID.Valid {
		return 0, fmt.Errorf("rollback decision %d: entity_id is NULL", decision.ID)
	}
	if !decision.Similarity.Valid || decision.Similarity.Float64 != 1.0 {
		return 0, fmt.Errorf("rollback decision %d: similarity does not match exact duplicate", decision.ID)
	}
	if !decision.Rationale.Valid || decision.Rationale.String != ReconcileRationaleExactDuplicateSameEntity {
		return 0, fmt.Errorf("rollback decision %d: rationale does not match exact duplicate", decision.ID)
	}
	if !decision.ContentSnapshot.Valid {
		return 0, fmt.Errorf("rollback decision %d: content_snapshot is NULL", decision.ID)
	}
	expectedContent := decision.ContentSnapshot.String
	target, err := loadActiveObservationForReconcile(db, decision.TargetObsID, asOf)
	if err != nil {
		return 0, fmt.Errorf("rollback decision %d target %d: %w", decision.ID, decision.TargetObsID, err)
	}
	if decision.EntityID.Int64 != target.EntityID {
		return 0, fmt.Errorf("rollback decision %d: entity_id %d does not match target entity %d", decision.ID, decision.EntityID.Int64, target.EntityID)
	}
	if target.Content != expectedContent {
		return 0, fmt.Errorf("rollback decision %d: target content does not match audit snapshot", decision.ID)
	}
	currentSources, err := loadAllSupersededSourcesForDecision(db, originalRunID, target, expectedContent)
	if err != nil {
		return 0, fmt.Errorf("rollback decision %d: %w", decision.ID, err)
	}
	if !sameInt64Set(sourceIDs, currentSources) {
		return 0, fmt.Errorf("rollback decision %d: source_obs_ids do not match current superseded sources for target %d", decision.ID, target.ID)
	}
	restored := 0
	for _, sourceID := range sourceIDs {
		if sourceID == target.ID {
			return 0, fmt.Errorf("rollback decision %d: source %d equals target", decision.ID, sourceID)
		}
		source, err := loadRollbackSourceObservation(db, sourceID, asOf)
		if err != nil {
			return 0, fmt.Errorf("rollback decision %d source %d: %w", decision.ID, sourceID, err)
		}
		if source.EntityID != target.EntityID {
			return 0, fmt.Errorf("rollback decision %d source %d: entity mismatch with target %d", decision.ID, sourceID, target.ID)
		}
		if source.Content != expectedContent {
			return 0, fmt.Errorf("rollback decision %d source %d: content does not match audit snapshot", decision.ID, sourceID)
		}
		if !source.SupersededBy.Valid || source.SupersededBy.Int64 != target.ID {
			return 0, fmt.Errorf("rollback decision %d source %d: superseded_by does not match target %d", decision.ID, sourceID, target.ID)
		}
		if !source.SupersededByRun.Valid || source.SupersededByRun.Int64 != originalRunID {
			return 0, fmt.Errorf("rollback decision %d source %d: superseded_by_run does not match run %d", decision.ID, sourceID, originalRunID)
		}
		result, err := db.Exec(`
			UPDATE observations
			SET superseded_by = NULL, superseded_at = NULL, superseded_reason = NULL, superseded_by_run = NULL
			WHERE id = ? AND superseded_by = ? AND superseded_by_run = ? AND deleted_at IS NULL
		`, sourceID, target.ID, originalRunID)
		if err != nil {
			return 0, fmt.Errorf("rollback source %d supersession: %w", sourceID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rollback source %d rows affected: %w", sourceID, err)
		}
		if updated != 1 {
			return 0, fmt.Errorf("rollback source %d: updated %d row(s), want 1", sourceID, updated)
		}
		restored++
	}
	result, err := db.Exec(`
		UPDATE reconcile_decisions
		SET reverted_at = ?, reverted_by_run = ?
		WHERE id = ? AND reverted_at IS NULL
	`, now.UTC().Format(sqliteTimestampLayout), rollbackRunID, decision.ID)
	if err != nil {
		return 0, fmt.Errorf("mark decision %d reverted: %w", decision.ID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark decision %d reverted rows affected: %w", decision.ID, err)
	}
	if updated != 1 {
		return 0, fmt.Errorf("mark decision %d reverted: updated %d row(s), want 1", decision.ID, updated)
	}
	return restored, nil
}

func loadAllSupersededSourcesForDecision(db dbtx, originalRunID int64, target reconcileValidationObservation, expectedContent string) ([]int64, error) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT o.id
		FROM observations o
		WHERE o.superseded_by = ?
		  AND o.superseded_by_run = ?
		  AND o.entity_id = ?
		  AND o.content = ?
		ORDER BY o.id
	`), target.ID, originalRunID, target.EntityID, expectedContent)
	if err != nil {
		return nil, fmt.Errorf("select current superseded sources: %w", err)
	}
	defer rows.Close()
	sourceIDs := make([]int64, 0)
	for rows.Next() {
		var sourceID int64
		if err := rows.Scan(&sourceID); err != nil {
			return nil, fmt.Errorf("scan current superseded source: %w", err)
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current superseded sources: %w", err)
	}
	return sourceIDs, nil
}

func sameInt64Set(left []int64, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]int64(nil), left...)
	rightCopy := append([]int64(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i] < leftCopy[j] })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i] < rightCopy[j] })
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
