# ARCHITECTURE

## Goal

Define the current `workmem` architecture: a local-first MCP memory server
implemented as a native Go binary, backed by SQLite, and distributed as a
single executable.

## High-level shape

- Process model: single local process
- Transport: MCP over stdio
- Storage: SQLite file(s)
- Packaging: single compiled binary per target OS and architecture
- Deployment model: local executable launched by MCP clients

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

### 5. Optional telemetry layer

Responsible for:

- opt-in operational logging only
- separate SQLite DB
- no effect on correctness when disabled
- graceful degradation on init failure

## Key invariants to preserve

### Lifecycle visibility discipline

Queries returning live memory must exclude soft-deleted entities, soft-deleted
observations, superseded observations, and observations attached to expired
events. Provenance tools may bypass ranking, but not lifecycle visibility
guards.

### FTS delete correctness

The contentless FTS table requires the SQLite special delete insert pattern. The delete path must match the originally indexed data.

Supersession does not require immediate FTS row deletion; FTS-backed reads must
join through the active-observation predicate so superseded rows stay hidden.
Tombstone/forget cleanup remains the path that physically removes FTS rows.

### Project isolation

Global memory and project memory must remain physically and logically separate.

### Ranking integrity

Search must overcollect, hydrate, score, rank, and only then touch returned observations.

## Package layout

The codebase should stay small and auditable:

```text
cmd/workmem/
internal/backup/
internal/dotenv/
internal/mcpserver/
internal/reconcile/
internal/store/
internal/telemetry/
docs/
testdata/
```

## Deliberate constraint

Do not over-abstract. The product is valuable partly because a local memory
server can be audited as a small system. Keep the Go codebase direct instead
of turning it into a framework.
