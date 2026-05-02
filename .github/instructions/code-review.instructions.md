---
applyTo: "**/*"
---

# Code Review Instructions

## Review posture

You are reviewing a Go MCP memory server backed by SQLite. The codebase is split across `internal/store/` (data layer) and `internal/mcpserver/` (transport). Bugs here cause silent data loss, incorrect retrieval, or broken multi-tenant isolation — all invisible to the caller.

## Priority checklist

Check these in order. Stop and flag a finding as soon as you see a violation.

### 1. Tombstone invariant

Every query that reads live data must guard both tables:

- `entities.deleted_at IS NULL`
- `observations.deleted_at IS NULL`

A query missing either guard silently returns soft-deleted rows. This is the highest-frequency escape in this codebase.

Also verify: `forget` correctly tombstones entities, observations, AND cleans up the FTS5 index before setting `deleted_at`.

### 2. FTS5 delete pattern

`memory_fts` is a contentless FTS5 virtual table. You cannot issue `DELETE FROM memory_fts WHERE ...`.

The only correct deletion pattern is the special INSERT command:
```sql
INSERT INTO memory_fts(memory_fts, rowid, entity_name, observation_content, entity_type)
VALUES('delete', <id>, <name>, <content>, <type>);
```

The `entity_type` must come from the **observation row** (`entity_type` column on the `observations` table), not from `entities.entity_type`. If a PR reads entity_type from `entities` for FTS deletion, flag it — the entity type can change after indexing.

### 3. Tool contract compliance

- Is the tool registered in `mcpserver/server.go` with correct JSON schema?
- Is there a case in `HandleTool` in `tools.go`?
- Does the handler call `AcquireDB` and release the returned lease to respect project scoping?
- Does it pass the correct half-life (project vs global)?

### 4. Multi-tenant isolation

- Any code touching the filesystem must use `ResolveProjectPath` — never raw `filepath.Join` on untrusted input
- Project DBs are cached via `AcquireDB`. Never open DBs directly.
- A query must never cross-contaminate global and project DBs.

### 5. Scoring pipeline integrity

If a PR touches `SearchMemory`, `CollectCandidates`, `HydrateCandidates`, or `ScoreCandidates`:

- `TouchObservations` must be called **only on the final ranked slice**, not on candidates
- `SanitizeSearchLimit` must be applied before any DB query using limit
- Candidate pool must overcollect (`limit * collectionMultiplier`) before scoring
- Hard cap (`maxCandidates`) must be respected

### 6. SQL safety

- All queries use `?` parameterized placeholders via `database/sql`
- No string interpolation of user-supplied values into SQL
- Chunked `IN()` queries handle empty slices without malformed SQL
- FTS queries use `stripQuotes` to prevent syntax injection

### 7. Confidence handling

- `confidence == 0.0` is a valid value and must NOT be overwritten to 1.0
- Only `confidence < 0` should be corrected to the default
- This was a known bug in the initial port — watch for regression

### 8. Test coverage

- New tools or changed behavior must have tests
- Tests use `t.TempDir()` for isolation — never production DBs
- Test lifecycle exclusion for affected paths: tombstoned entities/observations
  superseded observations, and expired event observations must not appear as
  active memory
- Test FTS cleanup after forget

## Telemetry design rationale (do NOT flag)

- Telemetry is opt-in via `MEMORY_TELEMETRY_PATH`; when unset, no telemetry DB is created and tool correctness must be unchanged.
- `MEMORY_TELEMETRY_PRIVACY=strict` hashes entity/query/label values before storage. Default telemetry keeps structural fields useful for local analysis; never let telemetry failures break tool calls.

## What NOT to flag

- Package layout choices (store vs mcpserver split) — intentional
- `modernc.org/sqlite` instead of `mattn/go-sqlite3` — deliberate CGO-free choice
- Missing features from the legacy Node implementation — `workmem` is now the canonical product; check `API_CONTRACT.md` and `OPERATIONS.md` for tracked obligations
- The tool count being exactly 12 — this is a design constraint, not an oversight
