# IMPLEMENTATION - workmem

## Phase 1: Viability And Contract [✅]

Establish that a Go binary can support the real product semantics, not just compile.

### Step 1.1: Repo bootstrap [✅]

Brief description. **Gate:** canonical docs exist and the implementation scope is explicit.

- [x] Create the new repository folder in user home
- [x] Write core project docs
- [x] Record starting architectural and product constraints

**On Step Gate (all items [x]):** no review required yet; this is bootstrap.

### Step 1.2: SQLite and FTS viability spike [✅]

Brief description. **Gate:** canary program proves schema init, FTS insert/match/delete, and basic persistence on the chosen driver.

- [x] Initialize Go module and binary entrypoint
- [x] Open SQLite DB with chosen driver
- [x] Prove contentless FTS table creation
- [x] Prove special FTS delete pattern works as expected
- [x] Record any driver caveats in OPERATIONS.md

**On Step Gate (all items [x]):** trigger focused review on SQLite semantics.

### Step 1.3: Product-contract harness definition [✅]

Brief description. **Gate:** a small product-contract matrix exists for core paths.

- [x] Identify must-preserve product behaviors
- [x] Separate product-contract tests from implementation-detail tests
- [x] Define fixtures for remember, recall, forget, and project isolation

**On Step Gate (all items [x]):** trigger correctness review.

## Phase 2: Core Product Surface [✅]

Deliver the minimum feature set needed for workmem to feel real.

### Step 2.1: Core memory operations [✅]

Brief description. **Gate:** `remember`, `recall`, `forget`, and `list_entities` behave credibly on shared fixtures.

- [x] Implement schema and migrations
- [x] Implement entity upsert and observation insert
- [x] Implement recall candidate collection and ranking skeleton
- [x] Implement forget semantics with tombstones and FTS cleanup
- [x] Verify returned behavior through product-contract fixtures

**On Step Gate (all items [x]):** trigger tripartite review + Integration Pulse.

### Step 2.2: Project-scoped memory [✅]

Brief description. **Gate:** project DB routing is isolated and deterministic.

- [x] Implement path resolution strategy
- [x] Implement DB caching strategy
- [x] Verify no global/project leakage on fixtures

**On Step Gate (all items [x]):** trigger correctness review.

## Phase 3: Full Surface And Packaging [✅]

Reach operational completeness and distribution quality.

### Step 3.1: Remaining tools and telemetry [✅]

Brief description. **Gate:** events, provenance tools, and telemetry plan are implemented or consciously deferred with docs.

- [x] Implement events and provenance primitives
- [x] Port compact recall behavior
- [x] Initially defer minimal telemetry with docs until the transport layer exists
- [x] Decide telemetry compatibility scope

**On Step Gate (all items [x]):** trigger tripartite review + Integration Pulse.

### Step 3.2: MCP stdio transport [✅]

Brief description. **Gate:** the Go binary exposes the documented tool surface over MCP stdio and survives a real command-transport smoke test.

- [x] Register the documented tool surface with MCP schemas
- [x] Bridge tool calls into the existing Go store backend
- [x] Prove stdio command transport against the Go binary entrypoint

**On Step Gate (all items [x]):** trigger correctness review focused on transport shape and tool errors.

### Step 3.3: Release pipeline [✅]

Ship workmem as a single binary users can install without `go build`. **Gate:** tagging `vX.Y.Z` on main produces a GitHub release with cross-platform archives + SHA256SUMS, a Homebrew tap formula resolves to those archives, and a fresh-machine walkthrough of each install path succeeds end-to-end.

- [x] Add CI cross-builds (`.github/workflows/go-ci.yml` matrix over darwin amd64+arm64, linux amd64+arm64, windows amd64 — no windows/arm64 yet)
- [x] Produce release binaries (`.github/workflows/release.yml` tag-triggered, 5 platforms, tarball+zip, SHA256SUMS, `-ldflags` inject `version`/`commit`/`buildDate` for `workmem version`)
- [x] Draft Homebrew strategy (tap repo [`marlian/homebrew-tap`](https://github.com/marlian/homebrew-tap) live with `Formula/workmem.rb` resolving to the release tarball by OS/arch, SHA256 verified, `brew test` exercises `workmem version` so future upstream regressions surface in the formula test)
- [x] Validate install path with fresh-machine assumptions (`brew tap marlian/tap && brew install workmem && workmem version` green on macOS arm64 against v0.2.0 release; direct-download path documented in README with per-platform checksum recipes; `go install github.com/marlian/workmem/cmd/workmem@latest` also works)

**On Step Gate (all items [x]):** trigger release readiness review.

### Step 3.4: Encrypted backup command [✅]

Ship a `workmem backup` subcommand that produces an age-encrypted snapshot of the global memory DB, taken via `VACUUM INTO` for consistency and streamed through `filippo.io/age` without any CGO additions. **Gate:** round-trip test proves the encrypted snapshot decrypts back to a readable SQLite database matching the source.

- [x] Add `filippo.io/age` dependency (pure Go, preserves `CGO_ENABLED=0`)
- [x] Implement `internal/backup` package: `Run(ctx, sourceDB, destPath, recipients)` with VACUUM INTO + age encryption, plus `ParseRecipients` accepting both raw `age1…` keys and recipients-file paths
- [x] Unit tests: round-trip, missing source, zero recipients, unwritable dest, invalid recipients
- [x] Wire `backup` subcommand in `cmd/workmem/main.go` with `--to`, `--age-recipient` (repeatable), `--db`, `--env-file`
- [x] README section documenting usage and manual `age -d` restore

**On Step Gate (all items [x]):** trigger correctness review focused on crypto wiring and VACUUM INTO error paths.

### Step 3.5: Telemetry [✅]

Implement opt-in telemetry with Go-native lifecycle management and a new privacy-strict mode. **Gate:** when `MEMORY_TELEMETRY_PATH` is set, every tool call lands a row in `tool_calls`; every `recall` lands a row in `search_metrics` linked by `tool_call_id`; when unset, no DB is created and no overhead is added. In `MEMORY_TELEMETRY_PRIVACY=strict` mode, entity/query/label values are sha256-hashed before storage.

- [x] Build `internal/telemetry` package (nil-tolerant Client, schema, sanitize, hash, detect)
- [x] Refactor `SearchMemory` to return `(results, metrics, err)` — no globals, no side channels
- [x] Wire `*telemetry.Client` through `cmd/workmem/main.go` and `mcpserver.Config`
- [x] Wrap `mcpserver` dispatch with duration + args/result sanitization + LogToolCall/LogSearchMetrics
- [x] Unit tests for package (nil-client safety, init failure, strict hashing, sanitize, detect)
- [x] Integration tests: enabled roundtrip / disabled zero overhead / privacy-strict
- [x] `docs/TELEMETRY.md` adapted for Go with privacy-strict documented
- [x] Telemetry invariants wired into `OPERATIONS.md`

**On Step Gate (all items [x]):** trigger correctness review on telemetry hook points and strict-mode hashing.

## Phase 4: Behavioral refinements and hardening [✅]

Evolve workmem through evidence-backed behavior and hardening. Each behavioral
change ships a measurement path or invariant so its effectiveness becomes
observable.

### Step 4.1: Conflict hints in `remember` response [✅]

Surface same-entity near-duplicate observations when the agent calls
`remember`, so possible conflicts become observable from the agent's point of
view. Full rationale and telemetry evidence
(14/14 silent overwrites in the first 8 days of production data) in
`DECISION_LOG.md` under 2026-04-22.

**Gate:** `remember` returns `possible_conflicts` on same-entity
high-similarity writes; telemetry records `conflicts_surfaced` per
`remember` call and `conflicts_acted_on` is derivable post-hoc from
`forget` calls against surfaced observation IDs; an integration
fixture demonstrates the deletion loop (write → hint → forget → clean recall).

- [x] Implement scoped composite-ranker conflict detection in the
  Remember path (reuse ranking logic from `internal/store/search.go`,
  scoped to `entity_id = E AND deleted_at IS NULL`)
- [x] Extend `remember` response shape with optional
  `possible_conflicts` array (observation_id, similarity, snippet)
- [x] Add a conservative starting similarity threshold as a named
  constant; document it as provisional and note that its final value
  is pinned via telemetry observation
- [x] Add `conflicts_surfaced INTEGER` column to `tool_calls` schema
  in `internal/telemetry/schema.go` with an idempotent ALTER path for
  existing DBs
- [x] Wire the `conflicts_surfaced` count from the Remember result
  into `LogToolCall` in `internal/mcpserver/telemetry.go`
- [x] Update MCP tool registration so the `remember` output schema
  exposes the new field (additive, backward-compatible; struct JSON
  tags + omitempty carry the shape, consistent with the repo's
  existing pattern of no explicit OutputSchema)
- [x] Unit tests: detection finds known duplicates, respects
  threshold, respects entity scope (different entity = no conflict),
  respects tombstones (deleted observations are not surfaced)
- [x] Integration test over MCP stdio: `remember` returns hints,
  agent-simulated `forget` on a surfaced ID removes it, subsequent
  `recall` is clean (`TestStepGateConflictHintEndToEndLoop`)
- [x] Update `API_CONTRACT.md` with the new response field and an
  explicit note that `forget` semantics are unchanged
- [x] Update README recommended LLM instructions with the one-line
  guidance about `possible_conflicts`
- [x] Extend `analysis/telemetry.py` with a cell that plots
  `conflicts_surfaced` vs `conflicts_acted_on` over time, so threshold
  calibration has a dashboard

**On Step Gate (all items [x]):** trigger tripartite review +
Integration Pulse. Review focus points: the scoped detection must not
leak into global ranking; the telemetry additions must preserve the
opt-in invariant (`MEMORY_TELEMETRY_PATH` unset ⇒ zero overhead); the
new response field must not break existing clients that ignore it.

### Step 4.2: Semantic hardening for expiry and local privacy [✅]

Close pre-v1 contract leaks found during external review: event expiry
must hide temporary context consistently, local memory files should be
private by default, and the SQLite canary must fail for the right reason.
**Gate:** `go test ./...` proves expired event observations are hidden
from all normal read surfaces, new DB paths use private POSIX modes, and
the orphan-observation canary only accepts a foreign-key constraint
failure.

- [x] Treat `expires_at` as a lifecycle visibility guard across recall,
  entity recall, event recall, and direct observation hydration
- [x] Validate and normalize `expires_at` on event creation
- [x] Create new memory directories with `0700` and best-effort chmod DB
  files to `0600`
- [x] Make `RejectsOrphanObservationInsert` validate the SQLite
  foreign-key error specifically
- [x] Update `API_CONTRACT.md`, `OPERATIONS.md`, and `DECISION_LOG.md`
  with the tightened contract

Note: `thinking/workmem-v1-semantic-hardening-spec.md` also proposed
zero-observation entity cleanup. That was not part of this Step gate; it was
closed separately in Step 4.4.

**On Step Gate (all items [x]):** trigger focused correctness/security
review before release tagging.

### Step 4.3: Post-review operational hardening [✅]

Close risks surfaced after the first hardening/review pass. **Gate:** CI is
green across host OS tests and cross-builds, active security/privacy/resource
risks are closed or explicitly deferred, and docs reflect the current Go
canonical architecture.

- [x] Redact malformed sensitive telemetry arguments before persistence
- [x] Validate `confidence` and case-insensitive self-relations before writes
- [x] Make `remember` and `relate` transactional across their dependent writes
- [x] Emit telemetry when FTS `MATCH` queries degrade and fallback paths continue
- [x] Bound project-scoped SQLite handle caching with leased `AcquireDB` access
- [x] Replace duplicate-schema error matching with versioned `schema_migrations`
- [x] Run the SQLite/FTS runtime canary from the built CLI on macOS, Linux, and Windows CI jobs

**On Step Gate (all items [x]):** focused reviews per PR plus CI matrix proof.

### Step 4.4: Zero-observation entity semantics [✅]

Close the final open item from `thinking/workmem-v1-semantic-hardening-spec.md`.
**Gate:** `go test ./...` proves empty entity shells are hidden, relation-only
entities remain visible, `recall_entity` follows the same semantics, and backup
outputs keep private POSIX mode.

- [x] Hide entities with zero active observations and zero live relations from
  `list_entities`
- [x] Keep relation-only entities visible in `list_entities`
- [x] Return not found from `recall_entity` for empty shells, while preserving
  relation-only entity graphs
- [x] Add explicit backup output `0600` mode regression coverage
- [x] Update `API_CONTRACT.md`, `OPERATIONS.md`, and `DECISION_LOG.md`

**On Step Gate (all items [x]):** focused correctness review is sufficient;
the change is local to entity visibility and backup permission tests.

## Phase 5: Evidence-driven tuning [🔧]

Use production telemetry to tune behavior only after enough data exists. No
threshold changes happen on vibes.

### Step 5.1: Conflict-hint threshold calibration [⏸]

Wait for the sampling window defined in `OPERATIONS.md`: at least 200
`remember` calls across two or more active projects, or 4 weeks of real use.
**Gate:** telemetry analysis either confirms `conflictHintMinScore = 0.6` twice
or records an evidence-backed adjustment in `DECISION_LOG.md`.

- [ ] Collect the telemetry sampling window
- [ ] Run the telemetry analysis split by `tool_calls.db_scope`
- [ ] Record the threshold decision and evidence summary

**On Step Gate (all items [x]):** correctness review focused on threshold
evidence and false-positive/false-negative trade-offs.

## Phase 6: Reconcile runner v0 [✅]

Add a slow, audit-first memory hygiene layer without changing the hot MCP tool
surface. v0 is deterministic-first: no embeddings, no remote providers, no
summarization-as-truth.

### Step 6.1: Supersession plumbing [✅]

Create the substrate that later reconcile runs can use safely. **Gate:** all
normal active-memory read paths hide superseded observations, migrations upgrade
legacy DBs, and no reconcile CLI exists yet.

- [x] Add migration-tracked observation supersession columns
- [x] Add reconcile run/decision audit tables and indexes
- [x] Filter `superseded_by IS NOT NULL` observations from recall, entity/event
  recall, provenance hydration, active counts, FTS ID search, and conflict hints
- [x] Cover fresh schema, legacy migrations, and read-path discipline in tests
- [x] Update `API_CONTRACT.md`, `OPERATIONS.md`, `ARCHITECTURE.md`,
  `DECISION_LOG.md`, README guidance, and GitHub review instructions

**On Step Gate (all items [x]):** focused correctness review before building
the reconcile CLI.

### Step 6.2: Deterministic propose report [✅]

Implement `workmem reconcile` in propose mode only. **Gate:** exact duplicate
candidates are reported in markdown with no memory mutations.

- [x] Add `workmem reconcile --mode propose`
- [x] Detect exact duplicate observations within the same entity
- [x] Write markdown report under ignored `review/`
- [x] Support global and project scopes
- [x] Prove propose mode does not mutate observations, access counts, or audit
  rows

**On Step Gate (all items [x]):** focused correctness review before adding
apply/rollback mutation paths.

### Step 6.3: Exact duplicate apply and rollback [✅]

Apply only deterministic exact duplicate supersession. **Gate:** rollback fully
restores active visibility and audit rows show what changed.

- [x] Apply exact duplicate supersession in a short transaction
- [x] Record complete reconcile decisions
- [x] Validate applied decisions before mutation: no self-supersession, active
  source/target observations, source/target same entity for exact duplicates,
  `source_obs_ids` encoded as a JSON array, and every applied supersession tied
  to a reconcile run
- [x] Add `workmem reconcile rollback <run_id>`
- [x] Prove rollback restores read-path visibility

**On Step Gate (all items [x]):** focused correctness review plus Integration
Pulse before PR merge. Review focus: rollback fail-closed behavior, audit row
completeness, and no semantic/non-deterministic matching sneaking into v0.

## Phase 7: Semantic reconcile substrate [✅]

Prepare semantic reconciliation without allowing semantic mutations. This phase
keeps the v0 safety posture intact: deterministic exact-duplicate apply remains
the only reconcile mutation path; semantic work remains validation/substrate-only
until a separate report-only proposal step is reviewed.

### Step 7.1: Embedding storage and provider boundary [✅]

Add the persistence and configuration substrate for future local semantic
proposals.
**Gate:** embedding schema is migration-tracked, provider config defaults to
`none`, remote providers fail closed without explicit opt-in, and no semantic
apply path exists.

- [x] Add migration-tracked `observation_embeddings` storage keyed by
  observation, provider, endpoint key, model, and dimensions
- [x] Add provider configuration parsing for `none`, `openai-compatible`,
  `ollama`, and explicitly-gated `openai`
- [x] Ensure `none` makes zero network calls and remains the default
- [x] Add tests for schema migration, provider defaults, and remote fail-closed
  behavior
- [x] Update `API_CONTRACT.md`, `ARCHITECTURE.md`, `OPERATIONS.md`,
  `DECISION_LOG.md`, and README guidance for the semantic validation substrate

**On Step Gate (all items [x]):** focused security/correctness review. Review
focus: no accidental remote memory export, no semantic apply route, and storage
schema supports provider/model/dimension changes.
