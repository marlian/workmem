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

Brief description. **Gate:** release artifacts exist for major OS targets and install flow is simpler than the Node baseline.

- [x] Add CI cross-builds
- [ ] Produce release binaries
- [ ] Draft Homebrew strategy
- [ ] Validate install path with fresh-machine assumptions

**On Step Gate (all items [x]):** trigger release readiness review.