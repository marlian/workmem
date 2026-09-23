# OPERATIONS

## Invariants

- workmem must remain a local-first, single-binary MCP stdio server.
- Core behavior comes before feature count.
- The client initiates memory tool calls. The server does not ingest transcripts,
  run LLM extraction, or schedule background consolidation. Reconcile is an
  explicit offline CLI operation.
- Confidence decay affects ranking only; it does not delete observations or
  rewrite stored confidence. `forget` is logical deletion, not guaranteed
  physical erasure of stored content.
- Telemetry must remain optional and side-effect free when disabled.
- SQLite queries must stay parameterized.
- The SQLite viability baseline is the `modernc.org/sqlite` driver until evidence proves it cannot carry the documented product contract.
- Project-scoped storage must never leak into global storage.
- The project mode is resolved once per process from `serve -project-mode`,
  then `MEMORY_PROJECT_MODE`, then `legacy`. An unknown value, a relative
  `MEMORY_PROJECTS_ROOT`, or `MEMORY_PROJECTS_ROOT` without central mode stops
  startup instead of falling back, and `serve` refuses to start with a missing
  or unreadable explicit `-env-file`. Proof: `TestResolveProjectStoreConfig`,
  `TestServeRefusesUnsafeConfiguration`.
- There is no silent fallback between modes: `central` never reads or writes a
  legacy `<project>/.memory/memory.db` and refuses the project while one
  exists (registered or not); `disabled` never touches any project path, also
  when the environment says otherwise but the flag says `disabled`. Proof:
  `TestCentralModeRefusesUnregisteredLegacyDBThenImportServesIt`,
  `TestServerCommandTransportCentralProjectStore`.
- In `central` mode a project directory must exist; workmem never creates it.
  A registered, initialized store whose DB file is missing, or a missing
  `registry.db` beside existing stores, fails closed. Registry ids are
  validated before use as path components. Proof:
  `TestCentralModeFailsClosedWhenInitializedStoreIsMissing`,
  `TestMissingRegistryBesideStoresFailsClosed`, `TestTamperedRegistryIDIsRejected`.
- The registry is looked up on every central call; no path->id mapping is
  cached, so a `project move` by another process can never serve one project's
  memory under another's path. Proof:
  `TestRunningProcessHonorsMoveAndDoesNotLeakIntoRecreatedPath`.
- Lookups for existing project memory (reconcile, semantic report) never
  create a root, registry, entry or DB. Proof:
  `TestResolveExistingProjectDBNeverCreates`, `TestReconcileCLICentralLookupCreatesNothing`.
- `workmem project import` never modifies source DB content or removes the
  source, and registers the copy only after migration and `integrity_check`
  pass. `project move -replace-empty` archives, never deletes, and only
  replaces a store with no entities, observations or events.
- Read-write SQLite connections use `busy_timeout` and `IMMEDIATE`
  transactions because multiple server processes share one instance's DBs and
  registry; migrations are checked before taking the write lock so opening an
  up-to-date DB never waits on another writer. Proof:
  `TestCentralRegistrationConvergesAcrossProcesses` (real processes, not
  goroutines), `TestInitDBIsNotBlockedByAnotherWriter`.
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
- `workmem reconcile --mode apply` and `workmem reconcile rollback <run_id>` must
  mutate only existing DBs, inside transactions, with audit rows tying every
  supersession and rollback to a reconcile run. Apply must validate current DB
  visibility before mutation, and rollback must fail closed when current
  source/target state no longer matches the recorded decision and content
  snapshot.
- Reconcile apply/rollback may run schema migrations as write-command preflight
  on existing DBs. This is intentional; read-only `propose` remains the no-schema-
  mutation inspection path.
- Reconcile CLI opens project DB files directly instead of leasing through the
  long-lived MCP project DB cache. Treat apply/rollback as short offline
  maintenance commands; do not run them concurrently with an active MCP server
  writing the same project DB.
- Semantic reconcile defaults to `none` and must make zero embedding network
  calls unless a non-`none` provider is explicitly configured. Remote providers
  or endpoints whose host is not literal `localhost` or a loopback IP require
  the explicit `--allow-remote-embeddings` flag; env/config alone must not
  enable remote memory export. Do not DNS-resolve host aliases for this trust
  decision.
- Semantic reconcile has no apply route. Exact-duplicate apply remains the only
  reconcile mutation path until semantic reports prove thresholds are safe.
- `workmem reconcile semantic --mode validate` must remain DB-free and network-
  free. It ignores report-only flag values so stale scan/report options cannot
  accidentally create or open memory DBs.
- `workmem reconcile semantic --mode report` may write only
  `observation_embeddings` cache rows. It must not mutate observations,
  supersession fields, reconcile audit rows, access counts, or FTS state, and it
  must exclude deleted, expired-event, and superseded observations from candidate
  generation. `openai-compatible` and `ollama` are supported report providers;
  `openai` remains validation-only and unsupported by report mode.
- Semantic report mode opens existing DBs without running schema migrations. If a
  DB is too old to contain the semantic schema, report mode must fail closed
  rather than perform schema/audit writes under a report-only command.
- Semantic report resource limits must be explicit and visible: provider requests
  are chunked by `--max-embeddings-per-request`, total uncached embeddings are
  bounded by `--max-embedding-calls`, per-entity comparison work is bounded by
  `--max-observations-per-entity`, and per-entity output is bounded by
  `--max-candidates-per-entity`. If caps omit observations or candidates, the
  markdown report must include limit signals.
- Semantic provider errors may expose sanitized transport causes or HTTP status,
  but must never include response bodies, prompts, endpoint keys, credentials, or
  memory content. Semantic reports intentionally include bounded candidate
  snippets for review; keep report files local, ignored, and private.
- `forget` must explicitly delete `observation_embeddings` rows for tombstoned
  observations/entities; observation tombstones are soft-deletes, so FK cascade
  is not a cleanup mechanism for embeddings. This cleanup also applies to
  tombstoned-entity drift where the observation/entity is already hidden from
  live memory and the user-facing forget result remains false. Drift cleanup must
  also remove FTS rows, tombstone surviving observations, and delete stale
  relations so `UpsertEntity` cannot revive forgotten facts or edges.

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

- Exact-duplicate reuse preserves the original observation's metadata and event
  association, while `remember_event.observations_attached` counts reused IDs as
  processed inputs even when they are not linked to the newly created event.
Trigger: the same entity/content is submitted across events, or a plain
`remember` repeats an observation still linked to an active expiring event.
Blast radius: new-event recall can omit an observation reported as attached;
repeating a fact does not detach it from the original event's expiry.
Fix: decide the cross-event deduplication contract before changing code, then
align attachment reporting and add product-contract fixtures for cross-event
and event-to-plain writes. The current behavior is documented in
`API_CONTRACT.md` under "Duplicate observation reuse".
Done when: the chosen association/lifetime behavior and attachment counts are
explicit and covered by regression tests through the tool interface.
Source proof: `internal/store/sqlite.go` (`AddObservation` duplicate early
return), `internal/store/tools.go` (`remember_event` attachment result), and
`internal/store/events.go` (`GetFullEvent` event filter).

- Semantic report supports unauthenticated `openai-compatible` and `ollama`
  endpoints. Non-loopback endpoints still require explicit remote opt-in;
  authenticated endpoints are not supported.
Trigger: a user needs authenticated remote embedding endpoints despite explicit
remote opt-in.
Blast radius: report mode is unusable for that endpoint; no memory is exported
without a successful explicit configuration.
Fix: add environment-backed API key support with redaction and no URL credentials.
Done when: auth headers are covered by tests and secrets are never rendered in
errors, reports, or telemetry.

- Telemetry records raw project paths: `tool_calls.project_path` stores the
  resolved path and `args_summary` keeps the raw `project` argument, even with
  `MEMORY_TELEMETRY_PRIVACY=strict`; in `central` mode the registry id is not
  recorded at all.
Trigger: telemetry is enabled with project-scoped calls.
Blast radius: local telemetry DB reveals directory names and layout; the
`analysis/` dashboard cannot correlate a project across a `project move`.
Fix: record the central registry id when available, hash or drop the raw path
under `strict`, and add a `project` case to `SanitizeArgs`.
Done when: strict-mode telemetry contains no raw project path, covered by a
telemetry integration test.
Source proof: `internal/mcpserver/telemetry.go` (`resolveProjectPath`),
`internal/telemetry/sanitize.go` (`SanitizeArgs` default branch).

- Central mode keys projects by the case-preserving canonical path. On a
  case-insensitive filesystem (default macOS APFS) two spellings that differ
  only in letter case get two registry entries and two stores.
Trigger: central mode on macOS with inconsistent path casing from clients.
Blast radius: split memory for one directory; Linux is unaffected.
Fix: on case-insensitive volumes, canonicalize letter case from directory
listings (or compare with the volume's case rules) before registry lookup.
Done when: a macOS test proves one registry entry for `~/App` and `~/app`.
Source proof: `internal/store/projectstore.go` (`CanonicalProjectPath`).

- `workmem project` has no `forget`/`adopt` command. A destination store that
  holds memory cannot be merged or unregistered from the CLI, and an orphaned
  store directory (for example after registry loss) cannot be re-registered in
  place.
Trigger: two stores for one directory both hold memory, or registry.db is
lost and restored from an older backup.
Blast radius: manual SQL on `registry.db` needed; data is never lost (stores
are archived, not deleted).
Fix: add `project forget <path>` (archive + unregister) and `project adopt
<id> <path>` (register an existing store directory after integrity check).
Done when: both commands exist with tests through the CLI.
Source proof: `internal/store/projectstore.go` (`MoveProject`, `ImportProject`).

- Project-scope `reconcile` rejects `--db`, so it derives the central root only
  from `-env-file`/`MEMORY_PROJECTS_ROOT`/`MEMORY_DB_PATH`. An instance started
  with `serve -db` alone cannot be targeted unless its env-file (or
  `MEMORY_PROJECTS_ROOT`) is passed; errors name the root that was searched.
Trigger: operator relies on `-db` in client args without an env-file.
Blast radius: reconcile reports "not registered"/"no registry"; nothing is created.
Fix: accept `--db` with project scope to derive the root (or a `-projects-root` flag).
Done when: a CLI test reconciles a `-db`-only central instance.
Source proof: `cmd/workmem/reconcile.go` (`openReconcileDB`).

- The default central root follows the global DB file name; renaming that
  file silently starts a new, empty root beside it.
Trigger: global DB renamed or moved without setting `MEMORY_PROJECTS_ROOT`.
Blast radius: project memory appears empty until the root is pointed back.
Fix: record the root in the global DB (or warn when a sibling `*-projects`
root with a registry exists and the configured one is empty).
Done when: startup warns on a likely root drift, covered by a test.
Source proof: `internal/store/projectstore.go` (`DefaultProjectsRoot`).

- `reconcile semantic --mode report` in central mode shares the resolver with
  exact reconcile but has no dedicated central-mode test.
Trigger: future change to `openSemanticReportDB`.
Blast radius: semantic reports could open the wrong DB undetected.
Fix: add a central-mode semantic report CLI test with the `none` provider.
Done when: the test exists.
Source proof: `cmd/workmem/reconcile.go` (`openSemanticReportDB`).

- An existing projects root is used as-is: workmem creates the root `0700` but,
  like legacy `.memory/` directories, does not tighten a root that already
  exists with broader permissions. Project DBs and the registry are `0600`
  either way; a readable root exposes project slugs (directory names) only.
Trigger: operator pre-creates `MEMORY_PROJECTS_ROOT` with default umask.
Blast radius: other local users can list project names, not memory content.
Fix: warn at startup when the root is group/world accessible.
Done when: startup warning covered by a test.
Source proof: `internal/store/projectstore.go` (`ensurePrivateDir`).

- `projectDBMu` is process-wide and is held while a central call consults the
  registry and, on a cache miss, while `InitDB` opens a project DB. A long
  writer on the registry or on a pending-migration DB can therefore delay all
  project-scoped calls of that process by up to the busy timeout.
Trigger: concurrent long reconcile apply plus first open of a project DB.
Blast radius: latency, bounded by the 5 s busy timeout; no data impact.
Fix: per-key open locks (singleflight) so only callers of the same project wait.
Done when: a test shows project A calls proceed while project B's open blocks.
Source proof: `internal/store/project.go` (`acquireCentralDB`).

- Legacy mode keys the project handle cache by the uncleaned resolved path, so
  `/p` and `/p/.` open two handles on one file.
Trigger: a client alternates spellings of the same project path in legacy mode.
Blast radius: duplicate handles on one DB within a process; both are correct
SQLite connections, so the cost is resources, not data. `central` mode is not
affected (cache keyed by registry id).
Fix: clean (or canonicalize) the legacy cache key, keeping the on-disk path.
Done when: a legacy-mode test proves one cache entry per directory.
Source proof: `internal/store/project.go` (`AcquireDB` legacy branch).

## Release proof ledger

- [x] Forget semantics including FTS deletion: covered by store tests and the SQLite/FTS runtime canary.
- [x] Project isolation: covered by project-scoped routing tests and leased `AcquireDB` cache tests.
- [x] Zero-observation entity semantics: empty shells hidden and relation-only
  entities preserved in `list_entities` / `recall_entity` product tests.
- [x] Supersession lifecycle guard: superseded observations hidden from recall,
  entity/event recall, provenance hydration, active counts, FTS ID search, and
  conflict hints.
- [x] Reconcile propose mode: exact duplicate candidates reported through a
  read-only DB handle without creating missing DBs or applying observation,
  access-count, or audit-row mutations.
- [x] Reconcile apply/rollback: exact duplicate supersession is audit-linked,
  transactional, and rollback restores source visibility only when current DB
  state still matches recorded decisions.
- [x] Semantic reconcile substrate: embedding storage is migration-tracked,
  provider config defaults to `none`, and remote endpoints fail closed without
  explicit opt-in.
- [x] Semantic reconcile report mode: same-entity semantic candidates are written
  to markdown while only `observation_embeddings` cache rows may change.
- [x] Semantic report hardening: provider requests are chunked, per-entity work
  is capped with visible report signals, and provider errors stay sanitized.
- [x] Release artifacts for macOS, Linux, and Windows: covered by CI cross-builds and release workflow artifacts.
- [x] Install flow on a fresh machine: documented in README and tracked in `IMPLEMENTATION.md` Step 3.3.

## Error Taxonomy

| Class | Meaning | Mitigation |
|---|---|---|
| contract-drift | Behavior diverges from `API_CONTRACT.md`, product fixtures, or documented invariants | compatibility tests and fixture replay |
| sqlite-feature-gap | chosen driver behaves differently on FTS or migration semantics | canary tests before deeper implementation |
| project-leak | global and project memory cross-contaminate | path and DB routing tests |
| project-store-split | one project resolves to more than one authoritative DB (legacy vs central, path spelling, lost registry entry, stale path mapping) | canonical paths, per-call registry lookup, fail-closed legacy detection and registry loss, `project list` audit |
| policy-drift | an instance runs a project mode other than the one intended (inherited env, missing env-file) | `-project-mode` flag precedence, fatal missing env-file, startup validation of mode/root |
| ranking-drift | search results are materially reordered | ranking fixtures and deterministic comparisons |
| telemetry-coupling | telemetry affects success path | optional layer with failure isolation |
