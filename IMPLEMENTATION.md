# IMPLEMENTATION - workmem

## Phase 1: Viability And Contract [✅]

Establish that a Go binary can support the real product semantics, not just compile.

### Step 1.1: Repo bootstrap [✅]

Brief description. **Gate:** canonical docs exist and the rewrite scope is explicit.

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

### Step 1.3: Compatibility harness definition [✅]

Brief description. **Gate:** a small contract matrix exists for Node-vs-Go comparison on core paths.

- [x] Identify must-preserve behaviors from the current server
- [x] Separate product-contract tests from implementation-detail tests
- [x] Define fixtures for remember, recall, forget, and project isolation

**On Step Gate (all items [x]):** trigger correctness review.

## Phase 2: Core Product Parity [✅]

Deliver the minimum feature set needed for the port to feel real.

### Step 2.1: Core memory operations [✅]

Brief description. **Gate:** `remember`, `recall`, `forget`, and `list_entities` behave credibly on shared fixtures.

- [x] Implement schema and migrations
- [x] Implement entity upsert and observation insert
- [x] Implement recall candidate collection and ranking skeleton
- [x] Implement forget semantics with tombstones and FTS cleanup
- [x] Verify returned behavior through compatibility fixtures

**On Step Gate (all items [x]):** trigger tripartite review + Integration Pulse.

### Step 2.2: Project-scoped memory [✅]

Brief description. **Gate:** project DB routing is isolated and deterministic.

- [x] Implement path resolution strategy
- [x] Implement DB caching strategy
- [x] Verify no global/project leakage on fixtures

**On Step Gate (all items [x]):** trigger correctness review.

## Phase 3: Full Surface And Packaging [🔧]

Reach operational parity and distribution quality.

### Step 3.1: Remaining tools and telemetry [✅]

Brief description. **Gate:** events, provenance tools, and telemetry plan are implemented or consciously deferred with docs.

- [x] Implement events and provenance primitives
- [x] Port compact recall behavior
- [x] Defer minimal telemetry with docs until the Go transport layer exists
- [x] Decide telemetry compatibility scope

**On Step Gate (all items [x]):** trigger tripartite review + Integration Pulse.

### Step 3.2: MCP stdio transport [✅]

Brief description. **Gate:** the Go binary exposes the parity tool surface over MCP stdio and survives a real command-transport smoke test.

- [x] Register the parity tool surface with MCP schemas
- [x] Bridge tool calls into the existing Go store backend
- [x] Prove stdio command transport against the Go binary entrypoint

**On Step Gate (all items [x]):** trigger correctness review focused on transport shape and tool errors.

### Step 3.3: Release pipeline [🔧]

Ship workmem as a single binary users can install without `go build`. **Gate:** tagging `vX.Y.Z` on main produces a GitHub release with cross-platform archives + SHA256SUMS, a Homebrew tap formula resolves to those archives, and a fresh-machine walkthrough of each install path succeeds end-to-end.

- [x] Add CI cross-builds (`.github/workflows/go-ci.yml` matrix over darwin/linux/windows × amd64/arm64)
- [x] Produce release binaries (`.github/workflows/release.yml` tag-triggered, 5 platforms, tarball+zip, SHA256SUMS, `-ldflags` inject `version`/`commit`/`buildDate` for `workmem version`)
- [ ] Draft Homebrew strategy (tap repo `marlian/homebrew-tap` with `Formula/workmem.rb` resolving to the release tarball by OS/arch, SHA256 verified)
- [ ] Validate install path with fresh-machine assumptions (`brew install`, `curl | tar | install`, `go install` all succeed on macOS+Linux starting from a clean shell)

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

Port the Node telemetry design to Go with Go-native refinements and a new privacy-strict mode. **Gate:** when `MEMORY_TELEMETRY_PATH` is set, every tool call lands a row in `tool_calls`; every `recall` lands a row in `search_metrics` linked by `tool_call_id`; when unset, no DB is created and no overhead is added. In `MEMORY_TELEMETRY_PRIVACY=strict` mode, entity/query/label values are sha256-hashed before storage.

- [x] Build `internal/telemetry` package (nil-tolerant Client, schema, sanitize, hash, detect)
- [x] Refactor `SearchMemory` to return `(results, metrics, err)` — no globals, no side channels
- [x] Wire `*telemetry.Client` through `cmd/workmem/main.go` and `mcpserver.Config`
- [x] Wrap `mcpserver` dispatch with duration + args/result sanitization + LogToolCall/LogSearchMetrics
- [x] Unit tests for package (nil-client safety, init failure, strict hashing, sanitize, detect)
- [x] Integration tests: enabled roundtrip / disabled zero overhead / privacy-strict
- [x] `docs/TELEMETRY.md` adapted for Go with privacy-strict documented
- [x] Telemetry invariants wired into `OPERATIONS.md`

**On Step Gate (all items [x]):** trigger correctness review on telemetry hook points and strict-mode hashing.
