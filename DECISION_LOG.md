# DECISION LOG

## 2026-04-14: Use the official Go MCP SDK for transport

### Context

The Go backend had already reached strong storage and tool parity, but still lacked the actual MCP stdio serving layer. At this point the main risk was protocol drift or handwritten JSON-RPC mistakes when wiring the server into a real client.

### Decision

Implement the transport layer on top of `github.com/modelcontextprotocol/go-sdk/mcp` and keep the backend semantics in `internal/store` as the single source of truth for tool behavior.

### Rationale

- the official SDK gives us a spec-tracking stdio transport and server lifecycle
- raw tool handlers still let us preserve the JS server's error-shaping behavior
- the same SDK makes it easy to add command-transport smoke tests before wiring the server into Kilo

### Alternatives considered

- write a custom JSON-RPC stdio loop in Go
Rejected because it adds protocol drift risk exactly where we now need confidence.

- use a third-party SDK such as `mcp-go`
Rejected for now because the official SDK is the cleaner fit when protocol compatibility is the main concern.

## 2026-04-13: Start Go rewrite in a separate repository

### Context

The existing public Node repository is stable, functional, and already used. The rewrite is motivated by product alignment, not by failure of the current implementation.

### Decision

Create a separate repository for the Go port instead of developing the rewrite inside the public Node repository.

### Rationale

- protects the stable repo from half-finished port work
- avoids confusing users about the supported implementation
- allows independent CI, packaging, and release experiments
- keeps the Node server shippable while parity is being proven

### Alternatives considered

- new folder inside the public repo
Rejected because it increases confusion and perceived support surface too early.

- long-lived branch in the public repo only
Rejected for now because it is less discoverable as a distinct product effort and still ties experimentation too tightly to the stable codebase.

## 2026-04-13: Telemetry is a later milestone, not a day-one blocker

### Context

The current server has opt-in telemetry in a separate SQLite database. It matters, but it is not the core product promise.

### Decision

Treat telemetry as a post-core-parity milestone.

### Rationale

- core MCP and storage semantics matter first
- telemetry is optional by design in the current implementation
- this reduces early porting complexity without sacrificing the product thesis

### Alternatives considered

- full telemetry parity before core work
Rejected because it delays proof of the main product value.

## 2026-04-14: Use modernc SQLite for the first Go viability path

### Context

Step 1.2 required proving the exact SQLite behavior this product depends on, especially contentless FTS5 with the special delete insert pattern used by `forget`.

### Decision

Use `modernc.org/sqlite` for the first Go bootstrap and canary path.

### Rationale

- preserves the single-binary product direction better than a CGO-first dependency choice
- already passed the canary for schema init, FTS insert/match/delete, tombstone persistence, and the `entity_type` snapshot edge case
- gives a real baseline to extend before investing in broader parity work

### Alternatives considered

- `github.com/mattn/go-sqlite3`
Rejected for now because CGO would complicate the distribution story too early, before we know whether a pure-Go path can hold the contract.

- delaying driver choice until later
Rejected because Step 1.2 needed an actual driver, not an abstract preference.

## 2026-04-14: Defer telemetry implementation until the Go transport layer is real

### Context

Phase 3.1 required a telemetry decision, but the Go port still lacks a stable MCP request path to instrument.

### Decision

Document telemetry scope now and defer implementation until the serving layer exists.

### Rationale

- avoids building logging around temporary test-only entrypoints
- keeps parity work focused on user-visible behavior first
- preserves the Node design principle that telemetry must be optional and side-effect free

### Alternatives considered

- implement telemetry directly in the storage helpers now
Rejected because it would couple instrumentation to the wrong layer and likely be rewritten once tool dispatch is finalized.