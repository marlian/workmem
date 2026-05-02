# OPERATIONS

## Invariants

- workmem must remain a local-first, single-binary MCP stdio server.
- Core behavior comes before feature count.
- Telemetry must remain optional and side-effect free when disabled.
- SQLite queries must stay parameterized.
- The SQLite viability baseline is the `modernc.org/sqlite` driver until evidence proves it cannot carry the documented product contract.
- Project-scoped storage must never leak into global storage.
- Live-data queries must never bypass tombstone guards.
- Live-data queries must never bypass supersession guards: observations with
  `superseded_by IS NOT NULL` are not active memory and must be hidden from
  recall, entity/event recall, direct observation hydration, and active counts.
- Supersession never auto-resurrects sources when the replacement observation
  becomes inactive. Recovery is an explicit rollback/repair operation that
  clears the supersession marker.
- Live-data queries must never bypass event-expiry guards: expired events and observations attached to expired events are hidden from normal read surfaces, including direct provenance hydration by ID.
- Entity listing and entity recall must hide empty shells with zero active
  observations and zero live relations. Relation-only entities remain visible
  because relations carry graph context.
- FTS cleanup must never use raw `DELETE` against a contentless FTS table.
- Supersession hides observations from FTS-backed reads through the active
  observation join predicate. Supersession does not physically delete FTS rows;
  tombstone/forget remains the FTS cleanup path so rollback can restore
  superseded observations without FTS rehydration.
- `remember_event` must be atomic: the event row and all attached observations commit together or not at all. Proof: `TestRememberEventAtomicityOnMidLoopFailure` in `internal/store/parity_test.go`.
- Telemetry is opt-in (`MEMORY_TELEMETRY_PATH`) and never affects the tool call success path. Init failure logs a single warning to stderr and disables telemetry for the session; the main memory DB is unaffected.
- Telemetry data lives in its own SQLite file, physically separate from the memory database. No foreign keys, no joins, no shared lifecycle.
- When `MEMORY_TELEMETRY_PRIVACY=strict`, entity names, queries, and event labels must be sha256-hashed before reaching disk. Observation/content values are always reduced to `<N chars>` regardless of mode.
- Telemetry summaries and validation errors must never include raw sensitive payloads. This applies before type validation too: malformed `observation`, `content`, `context`, `facts`, or `observations` values are redacted by key and JSON type, not serialized as received.
- New memory directories must be private by default (`0700`) and SQLite DB files plus SQLite sidecars (`-wal`, `-shm`, `-journal`) are best-effort hardened to `0600` on POSIX filesystems.
- Tool-level write paths must reject confidence values outside the documented inclusive `0.0-1.0` range before any DB mutation.
- `relate` must reject self-referencing relations before any DB mutation, using the same case-insensitive entity-name semantics as entity lookup.
- User input used in SQL `LIKE` predicates must escape `%`, `_`, and `\` and include an explicit `ESCAPE '\'` clause.
- `remember` must commit `UpsertEntity`, conflict detection, and `AddObservation` atomically. If observation insert fails after entity upsert, neither entity nor observation may survive. Conflict detection still runs before inserting the new observation.
- `relate` must commit endpoint entity upserts and relation insert atomically. If relation insertion fails for a non-idempotent reason, newly created endpoint entities must roll back; duplicate relations remain idempotent and return "Relation already exists".
- FTS `MATCH` query failures in search/conflict candidate collection are non-fatal only when fallback channels can continue, and must emit a degraded signal: `search_metrics.fts_query_errors` for recall, `tool_calls.conflict_fts_query_errors` for remember conflict detection.
- Project-scoped DB handles use leased access through `AcquireDB`; idle handles over the `PROJECT_DB_CACHE_MAX` target must be evicted and closed without touching the global DB handle.
- Schema upgrades are version-stamped in `schema_migrations`; fresh/current-shape DBs stamp already-present columns, while legacy DBs run only missing migrations.

## Active Debt

### P0

- None yet.

### P1

- Conflict-hint threshold (`conflictHintMinScore = 0.6` in `internal/store/conflict.go`) is provisional and must be calibrated against production telemetry. DECISION_LOG 2026-04-22 commits to "evidence over intuition" for this value — the constant lives unchanged until the calibration protocol below produces a defensible choice.
Trigger: The threshold is treated as permanent, or tuned on vibes instead of the telemetry ratio.
Blast radius: Too low → noisy hints, agent ignores them, surface-to-act ratio collapses, feature silently dies. Too high → real conflicts stop surfacing, silent-overwrite rate rises back toward the 14/14 baseline.
Fix (calibration protocol):
  1. Sample window: at least 200 `remember` calls across ≥ 2 active projects, or 4 weeks of real use, whichever comes first. Re-run `analysis/telemetry.py` cell 7b at the end of the window.
  2. Health signal: surface-to-act ratio ≥ 0.5 sustained means the hint is landing. < 0.2 means it is being ignored.
  3. Distribution check: inspect the raw similarity scores of surfaced hints that were NOT acted on. If they cluster near the threshold, the threshold is too low. If they span the full range, the prompt line or agent discipline is the bottleneck, not the threshold.
  4. Adjust in 0.05 increments, one change per cycle. Record the change in DECISION_LOG as an append-only entry with the evidence summary.
  5. After adjustment, reset the sample window and re-measure.
  6. Split telemetry by `tool_calls.db_scope` before computing the ratio. Global memory uses `MEMORY_HALF_LIFE_WEEKS` (12 by default) and project memory uses `PROJECT_MEMORY_HALF_LIFE_WEEKS` (52 by default), so observations of the same age decay differently across scopes and produce systematically different composite scores. Mixing both scopes into one ratio averages two distributions and pins a threshold that fits neither. The schema already exposes `db_scope`; the obligation is on the analysis path. Same goes for any future per-instance half-life override.
Done when: either the threshold has been confirmed twice in a row at the same value with the ratio inside [0.5, 0.9], or telemetry has shown a clear reason to redesign the hint (e.g., lexical detection consistently misses semantic conflicts and a pure-Go embedding path becomes available).

### Driver caveats

- The proven driver is `modernc.org/sqlite`; CGO-free distribution remains a product constraint.
- The runtime SQLite/FTS canary runs in CI on macOS, Linux, and Windows. It proves schema init, foreign-key enforcement, contentless FTS insert/match/delete, tombstone persistence, and reopen persistence on each host OS.
- The store currently forces `SetMaxOpenConns(1)` to keep the early SQLite path deterministic while the persistence layer is still thin.
- The FTS delete path must keep using the observation-row snapshot of `entity_type`; reading live `entities.entity_type` after mutation is not safe.
- Cross-build CI still compiles release-target artifacts with `CGO_ENABLED=0`; host-runtime canary jobs cover SQLite/FTS behavior, while cross-build jobs cover artifact compilation.

### P2

- Until `workmem reconcile --mode apply` exists, superseded observations are
  hidden but cannot be produced by a first-class write path. Exact content
  matching a superseded observation can be remembered again as a new active
  observation; Step 6.3 will clean deterministic duplicates through audited
  supersession.
Trigger: a user or future migration manually marks observations superseded, then
the same content is remembered again before the runner exists.
Blast radius: harmless duplicate active observations that the future deterministic
reconcile step must collapse.
Fix: implement Step 6.2/6.3 exact duplicate propose/apply and consider conflict
hints against superseded history only if real reports show this matters.
Done when: exact duplicate reconcile apply/rollback is shipped and tested.

## Release proof ledger

- [x] Forget semantics including FTS deletion: covered by store tests and the SQLite/FTS runtime canary.
- [x] Project isolation: covered by project-scoped routing tests and leased `AcquireDB` cache tests.
- [x] Zero-observation entity semantics: empty shells hidden and relation-only
  entities preserved in `list_entities` / `recall_entity` product tests.
- [x] Supersession lifecycle guard: superseded observations hidden from recall,
  entity/event recall, provenance hydration, active counts, FTS ID search, and
  conflict hints.
- [x] Release artifacts for macOS, Linux, and Windows: covered by CI cross-builds and release workflow artifacts.
- [x] Install flow on a fresh machine: documented in README and tracked in `IMPLEMENTATION.md` Step 3.3.

## Error Taxonomy

| Class | Meaning | Mitigation |
|---|---|---|
| contract-drift | Behavior diverges from `API_CONTRACT.md`, product fixtures, or documented invariants | compatibility tests and fixture replay |
| sqlite-feature-gap | chosen driver behaves differently on FTS or migration semantics | canary tests before deeper implementation |
| project-leak | global and project memory cross-contaminate | path and DB routing tests |
| ranking-drift | search results are materially reordered | ranking fixtures and deterministic comparisons |
| telemetry-coupling | telemetry affects success path | optional layer with failure isolation |
