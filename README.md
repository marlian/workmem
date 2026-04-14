# workmem

Native Go rewrite of mcp-memory.

This repository exists because the product pitch of the current server is stronger than its install story. The goal is to deliver the same local-first, zero-infrastructure memory server with a distribution model that actually feels like that promise: one binary, no Node runtime, no npm install, no native addon build step.

Status: backend parity plus MCP stdio transport are implemented; SQLite/FTS viability canary and MCP smoke tests are passing locally.

Current backend status: core memory operations, project-scoped routing, events, provenance, compact recall, MCP tool registration, and parity-oriented tests are implemented in Go.

Current product gap: the Go repository now serves MCP over stdio, but still needs wiring and proof against a real client such as Kilo plus the remaining release packaging work.

## Docs Map

- [PITCH.md](PITCH.md)
- [ARCHITECTURE.md](ARCHITECTURE.md)
- [OPERATIONS.md](OPERATIONS.md)
- [API_CONTRACT.md](API_CONTRACT.md)
- [DECISION_LOG.md](DECISION_LOG.md)
- [IMPLEMENTATION.md](IMPLEMENTATION.md)

## Quick Intent

- Keep MCP stdio compatibility.
- Preserve behavior that users already rely on.
- Improve install and distribution radically.
- Do the rewrite in a separate repo until parity is proven.

## Non-Goals For Day One

- Shipping every non-core feature immediately.
- Reimagining the product surface before parity exists.
- Touching the stable Node repository during exploration.

## Current Position

The existing Node server works and works well. This repository is not a rescue mission. It is a product-alignment rewrite: the runtime and installation model should match the promise of a local, invisible tool.

## Current Proof

- `go test ./...` passes for the initial SQLite canary.
- `go run ./cmd/workmem sqlite-canary` proves schema init, foreign-key enforcement, contentless FTS insert/match/delete, tombstone persistence, and reopen behavior.
- The reference edge case where `entity_type` changes after indexing is covered in the Go canary path.
- Shared compatibility fixtures now live in `testdata/contracts/` for remember, recall, compact recall, forget, and project isolation.
- Those fixtures are replayed against the Go runtime today; dual-runtime Node-vs-Go replay is still open work.
- `go test ./internal/mcpserver` now proves the MCP server both in-memory and over a real stdio `CommandTransport` launched from the Go binary entrypoint.

## CI

- `.github/workflows/go-ci.yml` runs `go test ./...` on macOS, Linux, and Windows.
- The same workflow cross-builds release-style binaries for the main desktop/server targets.