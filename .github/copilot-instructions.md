# Copilot Coding Agent — Repository Instructions

## Project context

This is `workmem`: a working memory server for AI reasoning via the Model Context Protocol (MCP). It exposes a knowledge graph (entities, observations, relations, events) backed by local SQLite with cognitive decay, composite relevance ranking, and project-scoped multi-tenancy.

Single binary, pure Go, zero CGO. SQLite via `modernc.org/sqlite`. MCP transport via `github.com/modelcontextprotocol/go-sdk`.

## Code layout

```
cmd/workmem/main.go         — entry point, CLI, signal handling
internal/mcpserver/          — MCP transport, tool registration, arg validation
internal/store/sqlite.go     — schema, migrations, CRUD, FTS, canary
internal/store/search.go     — 7-channel candidate collection, scoring, decay, grouping
internal/store/events.go     — record types, event/entity/observation queries
internal/store/tools.go      — HandleTool dispatch, arg validation
internal/store/project.go    — project path resolution, lazy DB cache
internal/store/config.go     — env-based configuration
testdata/contracts/          — shared behavioral fixtures
```

## Code style

- Standard Go formatting (`gofmt`)
- Errors returned, not panicked — `fmt.Errorf("context: %w", err)` wrapping
- `database/sql` for all DB operations — never raw SQLite C API
- Tests use `testing` package, `t.TempDir()` for isolation
- Exported functions start with uppercase, internal with lowercase
- No globals except config values read once at startup

## Database schema

Eight ordinary tables plus the contentless FTS table. Soft-delete via `deleted_at TEXT` applies only to `entities` and `observations`:

- `entities` — named objects (`name UNIQUE COLLATE NOCASE`, `entity_type`, `deleted_at`, timestamps)
- `observations` — atomic facts linked to an entity (`entity_id`, `content`, `source`, `confidence`, `access_count`, `last_accessed`, `deleted_at`, `superseded_by`, `event_id`, `entity_type` snapshot, timestamps)
- `relations` — typed edges between entities — **no soft-delete**, hard-deleted when entity is tombstoned
- `events` — grouped sessions/meetings/decisions (`label`, `event_date`, `event_type`, `context`, `expires_at`) — **no soft-delete**
- `reconcile_runs` — audit records for slow memory hygiene runs
- `reconcile_decisions` — reversible decisions proposed/applied by reconcile runs
- `observation_embeddings` — optional semantic reconcile vectors keyed by observation, provider, endpoint key, model, and dimensions
- `schema_migrations` — version registry for schema upgrades (`version`, `applied_at`)

Semantic reconcile is validation/substrate-only today. It does not generate candidates or reports yet. Future semantic report mode must be read-only for observations, supersession fields, reconcile audit rows, access counts, and FTS state; embedding-cache writes are the only acceptable semantic-side persistence. Exact-duplicate reconcile remains the only mutation path; remote embedding endpoints require the explicit `--allow-remote-embeddings` flag, not env/config alone.

**Invariant:** every query that reads live data **must** guard entity tombstones, observation tombstones, observation supersession (`superseded_by IS NULL`), and event expiry (`events.expires_at`). Missing any guard is a lifecycle bypass bug.

## FTS5 — contentless table

`memory_fts` is a **contentless** FTS5 virtual table. You cannot issue `DELETE FROM memory_fts`. The only correct deletion is the special insert command:

```sql
INSERT INTO memory_fts(memory_fts, rowid, entity_name, observation_content, entity_type)
VALUES('delete', <id>, <name>, <content>, <type>);
```

The `entity_type` must come from the **observation row** (`entity_type` column), not from `entities.entity_type`. The entity type can change after indexing — the observation snapshots the value at creation time.

## Multi-tenant project memory

Any tool that accepts a `project` parameter routes to a per-project SQLite DB at `<project>/.memory/memory.db` (created lazily). The global default DB lives at `MEMORY_DB_PATH`.

- `ResolveProjectPath` — always use this to expand `~` and relative paths
- `AcquireDB` — returns the correct leased DB for a tool call, lazy-opens and caches per project; release every lease
- Half-life: global = `MEMORY_HALF_LIFE_WEEKS` (12), project = `PROJECT_MEMORY_HALF_LIFE_WEEKS` (52)

## Composite scoring pipeline

1. `CollectCandidates` — 7 channels (fts_phrase, fts, entity_exact, entity_like, content_like, type_like, event_label), overcollects by 3x
2. `HydrateCandidates` — single batch `IN(?)` query joining observations + entities + events
3. `ScoreCandidates` — `DecayedConfidence` (Ebbinghaus) + `CompositeScore` (0.7 relevance + 0.3 memory + position bonus + multi-channel bonus)
4. `TouchObservations` — increments access only on the **ranked slice returned**, never the full candidate pool

## Tool surface (12 tools)

`remember`, `remember_batch`, `recall`, `recall_entity`, `relate`, `forget`, `list_entities`, `remember_event`, `recall_events`, `recall_event`, `get_observations`, `get_event_observations`.

12 tools is the ceiling. Adding tool 13 requires strong evidence that the benefit outweighs context token cost.

Every tool must:
1. Be registered in `mcpserver/server.go` with correct schema
2. Have a case in `HandleTool` in `tools.go`
3. Route through `AcquireDB` to respect project scoping and release project DB leases
4. Pass the correct half-life (project vs global)

## No hardcoding

- Config comes from env vars. Defaults in `config.go`.
- `MEMORY_DB_PATH`, `MEMORY_HALF_LIFE_WEEKS`, `PROJECT_MEMORY_HALF_LIFE_WEEKS`, `COMPACT_SNIPPET_LENGTH`, `PROJECT_DB_CACHE_MAX`

## SQL safety

- All queries use parameterized `?` placeholders
- No string interpolation of user-supplied values into SQL
- Chunked `IN()` queries handle empty slices without malformed SQL
- `ResolveProjectPath` before any filesystem operation on user-supplied paths

## Testing

- `go test ./...` must pass on all changes
- Each test creates an isolated DB via `t.TempDir()`
- Test lifecycle paths (deleted/superseded observations, deleted entities, and expired events/event observations excluded)
- Test FTS paths (forget removes from subsequent recall)
- `testdata/contracts/` contains shared behavioral fixtures

## Git discipline

- One logical change per commit
- `type: description` messages (feat, fix, test, perf, chore, docs, refactor)
- Update README when adding tools or changing visible behavior
