# OPERATIONS

## Invariants

- The Go port must remain a local-first, single-binary MCP stdio server.
- Core behavior comes before feature count.
- Telemetry must remain optional and side-effect free when disabled.
- SQLite queries must stay parameterized.
- The SQLite viability baseline is the `modernc.org/sqlite` driver until evidence proves it cannot carry parity.
- Project-scoped storage must never leak into global storage.
- Live-data queries must never bypass tombstone guards.
- FTS cleanup must never use raw `DELETE` against a contentless FTS table.
- `remember_event` must be atomic: the event row and all attached observations commit together or not at all. Proof: `TestRememberEventAtomicityOnMidLoopFailure` in `internal/store/parity_test.go`.

## Active Debt

### P0

- None yet.

### P1

- The Go port now replays the shared product fixtures locally, but still lacks an automated Node-vs-Go dual-runtime comparison in CI.
Trigger: Trusting Go-only fixture replay as full parity proof.
Blast radius: Drift from the JS reference can sneak in when both sides evolve independently.
Fix: add a JS-side fixture runner and compare normalized outputs in CI.
Done when: shared fixtures execute against both runtimes with diffable results.

- The current driver choice is validated only on local canary paths, not yet on cross-platform release targets.
Trigger: Treating a passing macOS canary as full portability proof.
Blast radius: Late failures on Linux or Windows packaging, or FTS behavior drift under different builds.
Fix: Keep the canary in CI and run it on at least macOS, Linux, and Windows before calling the persistence layer portable.
Done when: the same schema/FTS canary passes in cross-build validation.

- Telemetry parity is consciously deferred until the new Go MCP entrypoint is wired into a real client and the request path is considered stable.
Trigger: Instrumenting before the client-facing transport contract has been debugged in practice.
Blast radius: Busywork telemetry code tied to temporary wiring.
Fix: keep telemetry scope documented; implement it once the Kilo-facing transport path is stable.
Done when: the Go MCP entrypoint is live under a real client and tool-call telemetry can be attached once, not retrofitted twice.

- FTS5 viability is proven locally on the chosen driver, but not yet in a cross-platform validation matrix.
Trigger: Assuming a passing local canary implies release-target portability.
Blast radius: Search or forget semantics break only after packaging or OS expansion.
Fix: Run the same canary on macOS, Linux, and Windows targets.
Done when: FTS-specific parity tests pass across the release matrix.

### Driver caveats

- The first proven driver is `modernc.org/sqlite`, not the Node reference stack's `better-sqlite3` binding.
- The current canary passes schema init, foreign-key enforcement, contentless FTS insert/match/delete, and persistence reopen on `darwin/arm64`.
- The store currently forces `SetMaxOpenConns(1)` to keep the early SQLite path deterministic while the persistence layer is still thin.
- The FTS delete path must keep using the observation-row snapshot of `entity_type`; reading live `entities.entity_type` after mutation is not safe.
- Cross-build CI currently compiles with `CGO_ENABLED=0`; that matches the single-binary intent, but the runtime FTS proof is still stronger on real test runs than on cross-compiled artifacts alone.

### P2

- Telemetry schema and migration strategy are not yet designed.
Trigger: Reaching post-parity milestone without an observability plan.
Blast radius: Delayed adoption of telemetry in the Go port.
Fix: Define minimal telemetry compatibility after core parity lands.
Done when: telemetry design is recorded and scheduled.

## Pre-Launch TODO

- Prove MCP stdio compatibility with Kilo or another real client.
- Prove schema initialization and migrations on clean and upgraded DBs.
- Prove forget semantics including FTS deletion.
- Prove project isolation.
- Prove release artifacts for macOS, Linux, and Windows.
- Prove install flow that is materially simpler than the Node server.

## Error Taxonomy

| Class | Meaning | Mitigation |
|---|---|---|
| contract-drift | Go behavior diverges from stable Node semantics | compatibility tests and fixture replay |
| sqlite-feature-gap | chosen driver behaves differently on FTS or migration semantics | canary tests before deeper implementation |
| project-leak | global and project memory cross-contaminate | path and DB routing tests |
| ranking-drift | search results are materially reordered | ranking fixtures and deterministic comparisons |
| telemetry-coupling | telemetry affects success path | optional layer with failure isolation |