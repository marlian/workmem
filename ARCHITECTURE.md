# ARCHITECTURE

## Goal

Define the current `workmem` architecture: a local-first MCP memory server
implemented as a native Go binary, backed by SQLite, and distributed as a
single executable.

## High-level shape

- Process model: one local process per MCP instance or CLI invocation
- Transport: MCP over stdio
- Storage: SQLite file(s)
- Packaging: single compiled binary per target OS and architecture
- Deployment model: local executable launched by MCP clients

The client supplies observations and initiates recall. The server has no
transcript ingestion, model-driven fact extraction, automatic startup injection,
or background consolidation worker. Offline maintenance runs only through
explicit CLI commands.

## Execution surfaces

| Surface | Current behavior | Authority |
|---------|------------------|-----------|
| MCP tools | Store and retrieve observations, relations, and events | Explicit tool calls; lifecycle-guarded reads and writes |
| Exact reconcile | Propose, apply, or roll back exact duplicates within an entity | Apply recomputes and validates candidates in a transaction; rollback validates recorded state |
| Semantic reconcile | Validate configuration or report similar observations within an entity | Report may populate embedding cache, but cannot modify canonical observations or execute cleanup |
| Backup | Snapshot and encrypt the selected database | Explicit CLI operation; no automatic backup scheduler |

## Layers

### 1. MCP transport

Responsible for:

- stdio server lifecycle
- MCP handshake
- tool registration
- input decoding and validation
- mapping tool calls to service methods

### 2. Application layer

Responsible for:

- tool semantics
- project-vs-global routing
- result shaping
- documented behavior for recall and provenance primitives

### 3. Persistence layer

Responsible for:

- schema initialization
- migrations
- parameterized queries
- FTS synchronization
- transaction boundaries where needed

### 4. Ranking/search layer

Responsible for:

- candidate collection
- hydration
- composite scoring
- deterministic ordering
- access count touch on final ranked slice only

Recall combines seven lexical channels with read-time decayed confidence.
Embeddings are not part of this path. Conflict hints reuse lexical ranking to
surface possible overlaps; they neither establish a contradiction nor replace
an existing observation automatically.

### 5. Optional telemetry layer

Responsible for:

- opt-in operational logging only
- separate SQLite DB
- no effect on correctness when disabled
- graceful degradation on init failure

### 6. Offline maintenance

- `internal/store`: exact-duplicate validation, audit, apply, and rollback transactions
- `internal/embedding`: provider configuration, HTTP clients, and vector validation
- `internal/semantic`: bounded embedding/report orchestration
- `internal/reconcile`: markdown rendering for exact and semantic reports
- `internal/backup`: SQLite snapshot and age encryption

## Key invariants to preserve

### Lifecycle visibility discipline

Queries returning live memory must exclude soft-deleted entities, soft-deleted
observations, superseded observations, and observations attached to expired
events. Provenance tools may bypass ranking, but not lifecycle visibility
guards.

Age-based decay is a ranking calculation, not a retention policy. It changes
neither the stored observation nor its stored confidence and does not delete
old rows. Forget uses tombstones, and event expiry and supersession independently
control active-memory visibility; none of these is a guarantee of physical erasure.

### FTS delete correctness

The contentless FTS table requires the SQLite special delete insert pattern. The delete path must match the originally indexed data.

Supersession does not require immediate FTS row deletion; FTS-backed reads must
join through the active-observation predicate so superseded rows stay hidden.
Tombstone/forget cleanup remains the path that physically removes FTS rows.

### Reconcile apply and rollback

The reconcile runner is an offline CLI surface, not an MCP write path. Propose
opens existing DBs read-only; apply and rollback open existing DBs write-capable
and may run schema migrations before their short reconciliation transactions.
Apply recomputes the deterministic
exact-duplicate grouping query, validates active source/target observations just
before mutation, writes `reconcile_runs` / `reconcile_decisions`, and links each
superseded source to its apply run. The decision row snapshots the duplicated
content so rollback can reject rewritten rows. Rollback trusts audit rows only
after revalidating current source/target state, then clears the supersession
fields and marks decisions reverted by a rollback run.

`internal/store` owns the persistence-local exact-duplicate transactions and
their audit validation. `internal/reconcile` owns report rendering. Semantic
workflow orchestration already lives separately in `internal/semantic`; provider
behavior lives in `internal/embedding`.

### Semantic reconcile report boundary

Semantic reconciliation is split into storage, provider, and report behavior.
`observation_embeddings` stores vectors keyed by observation/provider/
endpoint-key/model/dimensions. `internal/embedding` owns provider config,
loopback/remote opt-in validation, HTTP clients for `openai-compatible` and
`ollama`, and vector encoding/validation. `internal/store` owns lifecycle-guarded
observation selection plus embedding-cache load/upsert primitives. `internal/semantic`
orchestrates report-only candidate generation, and `internal/reconcile` renders
markdown reports.

`workmem reconcile semantic --mode validate` validates config only: no DB open,
no network calls, no reports, and no mutation. `--mode report` opens an existing
DB write-capable and without schema migrations; cache writes to
`observation_embeddings` are the only allowed persistence. It generates
same-entity candidates from active observations, excluding tombstoned,
superseded, and expired-event observations. It must not mutate
observations, supersession fields, reconcile audit rows, access counts, or FTS
state. Report orchestration chunks provider requests, caps observations and
candidate output per entity, and emits limit signals when caps truncate work so
resource protection is visible rather than silent. The renderer groups pairwise
candidates into same-entity observation clusters and adds manual decision
checkboxes; clusters are review scaffolding, not executable decisions. Provider
diagnostics are sanitized to status/transport shape only; response bodies and
memory content do not enter provider errors. Reports intentionally include
bounded candidate snippets so a human can judge false positives; report files are
local, ignored, and written with private permissions. Since observations are
tombstoned rather than hard-deleted, `forget` explicitly removes embedding rows
instead of relying on foreign-key cascade cleanup.

Model-assisted cleanup proposals are intentionally outside the core architecture:
the public contract provides provider-neutral prompt guidance, while users choose
their own model and privacy boundary. Proposal output is advisory input for a
human review session, not a machine-readable apply plan.

Semantic apply is not implemented. Exact-duplicate apply is the only reconcile
mutation path. Model-assisted proposal review does not add an executable plan
format or an automatic observation synthesis/merge path.

### Project isolation

Global memory and project memory must remain physically and logically separate.

### Ranking integrity

Search must overcollect, hydrate, score, rank, and only then touch returned observations.

## Package layout

Current package layout:

```text
cmd/workmem/
internal/backup/
internal/dotenv/
internal/embedding/
internal/mcpserver/
internal/reconcile/
internal/semantic/
internal/store/
internal/telemetry/
docs/
testdata/
```

## Deliberate constraint

Do not over-abstract. The product is valuable partly because a local memory
server can be audited as a small system. Keep the Go codebase direct instead
of turning it into a framework.
