# ARCHITECTURE

## Goal

Rebuild the current MCP memory server as a native Go binary while preserving the user-facing behavior that matters.

## High-level shape

- Process model: single local process
- Transport: MCP over stdio
- Storage: SQLite file(s)
- Packaging: single compiled binary per target OS and architecture
- Deployment model: local executable launched by MCP clients

## Planned layers

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
- compatibility behavior for recall and provenance primitives

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

### Tombstone discipline

Queries returning live memory must exclude soft-deleted entities and observations.

### FTS delete correctness

The contentless FTS table requires the SQLite special delete insert pattern. The delete path must match the originally indexed data.

### Project isolation

Global memory and project memory must remain physically and logically separate.

### Ranking integrity

Search must overcollect, hydrate, score, rank, and only then touch returned observations.

## Expected package layout

This is provisional and should stay small:

```text
cmd/workmem/
internal/mcp/
internal/app/
internal/store/
internal/search/
internal/telemetry/
testdata/
```

## Deliberate constraint

Do not over-abstract early. The current Node implementation is valuable partly because it is easy to audit. The Go port should preserve that clarity rather than turning a small server into a framework.