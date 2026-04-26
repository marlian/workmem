# OPERATIONS

## Invariants

- The Go port must remain a local-first, single-binary MCP stdio server.
- Core behavior comes before feature count.
- Telemetry must remain optional and side-effect free when disabled.
- SQLite queries must stay parameterized.
- The SQLite viability baseline is the `modernc.org/sqlite` driver until evidence proves it cannot carry parity.
- Project-scoped storage must never leak into global storage.
- Live-data queries must never bypass tombstone guards.
- Live-data queries must never bypass event-expiry guards: expired events and observations attached to expired events are hidden from normal read surfaces, including direct provenance hydration by ID.
- FTS cleanup must never use raw `DELETE` against a contentless FTS table.
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

- The Go port now replays the shared product fixtures locally, but still lacks an automated Node-vs-Go dual-runtime comparison in CI.
Trigger: Trusting Go-only fixture replay as full parity proof.
Blast radius: Drift from the JS reference can sneak in when both sides evolve independently.
Fix: add a JS-side fixture runner and compare normalized outputs in CI.
Done when: shared fixtures execute against both runtimes with diffable results.

- The current driver choice is validated only on local canary paths, not yet on cross-platform release targets.
Trigger: Treating a passing macOS canary as full portability proof.
Blast radius: Late failures on Linux or Windows packaging, or FTS behavior drift under different builds.
Fix: Keep the canary in CI and run it on at least macOS, Linux, and Windows before calling the persistence layer portable.
Done when: the same schema/FTS canary passes in cross-build validation.

- FTS5 viability is proven locally on the chosen driver, but not yet in a cross-platform validation matrix.
Trigger: Assuming a passing local canary implies release-target portability.
Blast radius: Search or forget semantics break only after packaging or OS expansion.
Fix: Run the same canary on macOS, Linux, and Windows targets.
Done when: FTS-specific parity tests pass across the release matrix.

### Driver caveats

- The first proven driver is `modernc.org/sqlite`, not the Node reference stack's `better-sqlite3` binding.
- The current canary passes schema init, foreign-key enforcement, contentless FTS insert/match/delete, and persistence reopen on `darwin/arm64`.
- The store currently forces `SetMaxOpenConns(1)` to keep the early SQLite path deterministic while the persistence layer is still thin.
- The FTS delete path must keep using the observation-row snapshot of `entity_type`; reading live `entities.entity_type` after mutation is not safe.
- Cross-build CI currently compiles with `CGO_ENABLED=0`; that matches the single-binary intent, but the runtime FTS proof is still stronger on real test runs than on cross-compiled artifacts alone.

### P2

- Project-scoped DB handles are cached indefinitely for the process lifetime.
Trigger: A long-running MCP server touches many distinct project paths over time.
Blast radius: The `projectDBs` map and open SQLite file descriptors grow until shutdown; small personal use is fine, broad workspace use can leak resources.
Fix: Add a bounded cache with lazy close/eviction, or an explicit max cap with deterministic close behavior.
Done when: a regression test opens more than the cap and proves older project DB handles are closed/evicted without breaking active global storage.

- Schema migrations are still inline `ALTER TABLE` statements guarded by duplicate-schema string matching.
Trigger: Adding more migrations before replacing the guard with explicit migration tracking.
Blast radius: A real migration error could be mistaken for an idempotent duplicate-schema condition, or a legacy DB path could drift from fresh schema behavior.
Fix: add `schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)` before v1; keep fresh `CREATE TABLE IF NOT EXISTS` bootstrap and prove legacy upgrades with regression tests.
Done when: each migration has a version, runs once, and legacy-schema tests no longer depend on string-matching duplicate column errors.

## Pre-Launch TODO

- Prove schema initialization and migrations on clean and upgraded DBs.
- Prove forget semantics including FTS deletion.
- Prove project isolation.
- Prove release artifacts for macOS, Linux, and Windows.
- Prove install flow that is materially simpler than the Node server.

## Error Taxonomy

| Class | Meaning | Mitigation |
|---|---|---|
| contract-drift | Go behavior diverges from stable Node semantics | compatibility tests and fixture replay |
| sqlite-feature-gap | chosen driver behaves differently on FTS or migration semantics | canary tests before deeper implementation |
| project-leak | global and project memory cross-contaminate | path and DB routing tests |
| ranking-drift | search results are materially reordered | ranking fixtures and deterministic comparisons |
| telemetry-coupling | telemetry affects success path | optional layer with failure isolation |
