# DECISION LOG

## 2026-04-14: Ship encrypted backup as a subcommand; leave restore as plain `age -d`

### Context

The `private_memory` wiring stores sensitive content (therapy, health, relationships). Telemetry already has a privacy-strict mode, but the main memory DB on disk remains plaintext. The threat model of "laptop lost spento" is covered by FileVault/BitLocker/LUKS at the OS level; the gap is cloud backup: iCloud Drive / Google Drive / Dropbox all corrupt live SQLite DBs (WAL race with sync client). Cross-platform encryption-at-rest on the live DB would require SQLCipher (CGO) and cross-platform keychain integration — both violate the pure-Go single-binary invariant for marginal gain over OS crypto.

### Decision

Add a `workmem backup` subcommand that writes an age-encrypted snapshot of the global memory DB. The snapshot is consistent (produced via `VACUUM INTO`, not a raw copy), the plaintext intermediate never leaves the temp directory, and the output uses `0600` permissions. Decryption is not wrapped by the CLI — users run `age -d -i identity.txt backup.age > memory.db` manually.

### Rationale

- `filippo.io/age` is pure Go — preserves `CGO_ENABLED=0` and the single-binary product direction.
- `VACUUM INTO` produces a driver-agnostic consistent snapshot; no dependence on modernc or SQLite-private backup APIs.
- End-to-end encryption with a user-held key gives cloud-sync safety (the `.age` file is useless without the identity) without claiming to solve problems the OS already solves (live-disk encryption).
- Asymmetric-only (no passphrase) keeps the operator UX honest: key lives in a file, backup is unattended, no prompts.
- Not wrapping restore keeps the CLI honest about its scope — restore is context-dependent (stop server? atomic swap? merge?) and those choices should be the user's, not ours.

### Alternatives considered

- **Encryption at rest on the live DB via SQLCipher + cross-platform keychain.** Rejected: requires CGO, cross-platform keychain gymnastics (macOS Keychain, Windows Credential Manager, Linux Secret Service with headless fallback), and the threat model above the OS crypto layer is narrow (laptop awake, unlocked, and stolen — chain of custody the app cannot fully defend anyway).
- **Litestream / WAL streaming replication to S3.** Rejected for v1: overkill for a personal tool where daily backup is enough, and introduces an operational dependency (object store + credentials) that clashes with "one binary, one file" positioning. Might reconsider later for power users.
- **Passphrase-based symmetric encryption.** Rejected: worse UX for unattended runs, and still funnels into age's file format anyway — if a user wants symmetric, `age -p` on the output file is a one-liner.
- **Include project-scoped DBs automatically.** Rejected: project DBs belong to workspaces, not to the user's top-level knowledge. Auto-including them couples backup to filesystem scanning and makes the unit of restore ambiguous. A `backup` invocation per workspace is explicit.
- **Include telemetry.db in the snapshot.** Rejected: telemetry is operational, rebuildable, and has a different lifecycle than knowledge. Mixing them also risks leaking telemetry via recall if paths cross.

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