# DECISION LOG

## 2026-04-22: Surface near-duplicate observations in `remember` response; supersession stays agent-driven

### Context

workmem is working memory, not a diary. Supersession is not a first-class
state — it is a transition implemented as `forget` followed by `remember`.
From the agent's perspective, `forget` removes a fact from FTS and recall
(soft-delete at the DB layer exists for backup/debug only). This ontology
makes `forget` the only mechanism through which the canonical state
mutates, which `OPERATIONS.md` already treats as a never-drift invariant.

Eight days of production telemetry (121 tool calls across `memory` and
`private_memory` on `claude-code`: 38 `recall`, 42 `remember`, 24
`remember_batch`, 11 `remember_event`, 3 `forget`) now provide direct
evidence that the ontology is not being honored in practice. On
same-entity transitions within a 7-day window:

- silent overwrites (`remember → remember` on same entity, no `forget`
  between): **14**
- forget corrections (`remember → forget` on same entity): **0**
- post-forget rewrites (`forget → remember` on same entity): **0**

14 of 14 same-entity updates were silent overwrites. The agent never
used `forget` as supersession despite the recommended LLM instructions
in the README. Old facts remain in the live set, dampened by decay but
still recoverable by composite ranking — exactly the failure mode the
external reviewer in `review/commenti.md` predicted for
ranking-as-truth-resolution.

The PITCH commits to "stupidity of use, solidity of backend": the model
does not think about memory, the backend does the work. Requiring the
agent to `recall_entity` before every `remember` violates that
commitment and, per the telemetry, does not happen anyway.

### Decision

Extend the `remember` response with an optional `possible_conflicts`
field. When storing an observation on entity E with content C, the
backend runs the existing composite ranker scoped to
`entity_id = E AND deleted_at IS NULL`, returns up to 3 existing
observations above a conservative similarity threshold, and surfaces
them on the response:

```json
{
  "entity_id": 42,
  "observation_id": 999,
  "stored": true,
  "possible_conflicts": [
    {"observation_id": 877, "similarity": 0.87, "snippet": "..."},
    {"observation_id": 801, "similarity": 0.62, "snippet": "..."}
  ]
}
```

The agent decides whether to call `forget(observation_id)` on each.
The backend never soft-deletes on the agent's behalf.

The similarity threshold is **intentionally not pinned in this
decision**. Its calibration is a follow-up job for the telemetry loop
described below: we ship with a conservative starting value, observe
`conflicts_surfaced` vs `conflicts_acted_on`, and freeze the number
when the data supports a defensible choice. Pinning a threshold at
design time without production evidence would contradict the
"evidence over intuition" principle in the PITCH.

One new line in the recommended LLM instructions:

> *"If `remember` returns `possible_conflicts`, review those
> observations and call `forget(obs_id)` on any your new fact
> supersedes."*

Telemetry is extended to close the loop:
`conflicts_surfaced INTEGER` is added to `tool_calls`, and
`conflicts_acted_on` is derived post-hoc by joining `forget` calls
against surfaced observation IDs within a short window. These
measurements are what eventually pin the threshold and validate
that the hint is changing agent behavior; they also let us back the
feature out cleanly if the surface-to-act ratio stays low.

No new MCP tool. No schema change on `observations` / `entities`.
No change to `forget` semantics. Response shape extends additively;
clients that ignore the new field are unaffected.

### Rationale

- **Empirical motivation, not speculative.** 100% silent-overwrite
  rate in 8 days of real usage across `claude-code` on `workmem`,
  `inv-try`, `governor`, and global scope. Direction is unambiguous;
  sample is small enough to leave threshold and copy tunable.
- **Preserves the ontological commitment.** Backend detects; agent
  decides. `forget` remains the only mutation mechanism for the
  canonical state. No "superseded" state, no supersession chain, no
  diary semantics.
- **Preserves "12 tools, no more no less."** The feature fits inside
  the existing `remember` surface. No tool 13.
- **Preserves the single-binary / pure-Go / CGO_ENABLED=0 invariants.**
  Detection reuses the FTS-based composite ranker already in
  `internal/store/search.go`. No embedding model, no external runtime.
- **Honest about its limits.** Lexical similarity catches
  reformulations with high token overlap ("rate limit 100/min" vs
  "rate limit 200/min") and misses semantic contradictions with no
  overlap ("limit is 100" vs "we accept up to one hundred").
  Documented as a hint, not a guarantee. The agent can still call
  `recall_entity` proactively in paranoid contexts.
- **Measurable.** `conflicts_surfaced` + `conflicts_acted_on` turn
  the feature into a telemetry-observable control loop. The threshold
  is calibrated, not declared, and the feature can be reverted on
  evidence.

### Alternatives considered

- **Auto-supersede: backend soft-deletes high-similarity existing
  observations without notifying the agent.** Rejected. Similarity is
  not contradiction (complementary facts like "uses Binance" / "uses
  Bitfinex" share tokens), so auto-delete destroys data. It also
  violates the invariant that `forget` is the only mutation mechanism,
  granting the backend semantic authority it should not have. And it
  leaves no audit trail — a later `recall_entity` short of expected
  rows has no explanation.

- **Prompt reinforcement: instruct the agent to `recall_entity` before
  every `remember`.** Rejected. Violates "stupidity of use": the agent
  should not have to detect conflicts, and should not pay a read cost
  on every write. The telemetry shows the current prompt guidance is
  already ignored in 14/14 cases; adding more text is unlikely to fix
  what a structural signal can.

- **Introduce a `supersede(old_id, new_content)` tool.** Rejected.
  Breaches the 12-tool ceiling. Duplicates semantics already
  expressible as `forget` + `remember`. Invites a supersession-chain
  schema (the diary model this project is explicitly not).

- **Semantic embedding-based conflict detection.** Rejected for this
  iteration. Breaks the pure-Go single-binary invariant (ONNX runtime,
  Ollama call, or external API — all compromises). Lexical similarity
  via the existing composite ranker covers the high-overlap case for
  free. Revisit if the telemetry says the lexical layer is missing
  too many real conflicts *and* a pure-Go inference path is
  available.

- **Pin the similarity threshold now.** Rejected. The whole point of
  the telemetry instrumentation is to calibrate the threshold on
  production evidence. A design-time number would either be too
  aggressive (noise) or too conservative (no signal) without data to
  tell us which, and would lock us into the wrong value harder than a
  number explicitly marked as provisional.

- **Defer the decision pending more data.** Rejected. The 100%
  silent-overwrite ratio across 14 transitions is already a
  directional signal strong enough to ship a conservative, reversible
  change. Waiting increases the accumulated wrong state in live DBs
  without producing a qualitatively different answer.

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

## 2026-04-14: Port telemetry with Go-native refinements and add privacy-strict mode

### Context

The Node reference implementation shipped with opt-in telemetry in a separate SQLite DB (schemas: `tool_calls`, `search_metrics`). Phase 3 deferred the port until the Go MCP entrypoint was real and adopted. That condition is now met: the Go binary serves Claude Code, Kilo, and Codex in production. Time to port.

### Decision

Port the Node telemetry design to Go and preserve the guiding principles (opt-in via env, separate database, counts-only for results, content replaced with `<N chars>`). Refine three Node-era shortcuts:

1. **Nil-tolerant `*Client`** — the client value is `nil` when disabled, every method returns immediately on `nil` receiver. Replaces per-callsite `if TELEMETRY_ENABLED` checks.
2. **No globals** — the client is constructed in `cmd/workmem/main.go` and plumbed via `mcpserver.Config{Telemetry: …}`. Replaces the Node pattern of module-level mutable state (`_telemetryDb`, `_lastSearchMetrics`, etc.).
3. **`SearchMemory` returns `SearchMetrics` as a tuple** — `(results []SearchObservation, metrics SearchMetrics, err error)`. Replaces the Node `_lastSearchMetrics` side-channel.

Add a new **privacy-strict mode** (`MEMORY_TELEMETRY_PRIVACY=strict`): entity names, queries, and event labels are sha256-hashed before storage. Intended for sensitive backends (e.g., the `private_memory` server backing therapy/health/relationship content).

### Rationale

- Node-era globals would have been awkward in Go and hard to test under parallel `t.Run` — eliminating them keeps the test story clean.
- Privacy-strict closes a real threat: local plaintext telemetry DB on a laptop with sensitive entity names is a leak vector if the laptop is lost/sync'd/exported. Strict mode lets one binary serve two wiring contexts (permissive `memory`, strict `private_memory`) cleanly.
- `SearchMemory` returning metrics as a proper value is idiomatic Go and testable in isolation without the telemetry package.
- Using `modernc.org/sqlite` for the telemetry DB keeps the pure-Go single-binary invariant (no CGO addition just for the analytics path).

### Alternatives considered

- **1:1 port with globals** — Rejected because Go's `database/sql` + `sql.Stmt` lifecycle around a package-level mutable pointer becomes painful under test; the nil-client pattern is simpler and safer.
- **Attach telemetry as an MCP tool** — Rejected as in the Node design: telemetry is human-developer infrastructure, not a model capability. Adding a tool wastes context tokens on every call for every client.
- **Encryption at rest on the telemetry DB with a keychain-stored key** — Rejected for this iteration. Cross-platform keychain integration (macOS/Windows/Linux headless) is a bigger cantiere than hashing the sensitive fields. Revisit if the strict mode proves insufficient in practice.

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