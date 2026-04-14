# Telemetry

> Opt-in usage analytics for workmem. Zero overhead when disabled. Separate database, strict privacy controls.

## Enabling

Set `MEMORY_TELEMETRY_PATH` to a file path. The telemetry database is created on first tool call.

**Via `.env`:**
```bash
MEMORY_TELEMETRY_PATH=./telemetry.db
```

**Via client config (example for Claude Code's `~/.claude.json`):**
```json
{
  "memory": {
    "command": "/path/to/workmem",
    "args": ["-env-file", "/path/to/memory.env"],
    "env": {
      "MEMORY_TELEMETRY_PATH": "/absolute/path/to/telemetry.db"
    }
  }
}
```

When `MEMORY_TELEMETRY_PATH` is unset (the default), every telemetry call is a no-op — no timing, no logging, no database opened. The `*telemetry.Client` pointer in the runtime is `nil` and every method returns immediately.

## Privacy modes

Telemetry supports two modes, controlled by `MEMORY_TELEMETRY_PRIVACY`:

| Value | Mode | Behavior |
|-------|------|----------|
| (unset) / any other value | **permissive** (default) | entity names, queries, and event labels are stored in plaintext |
| `strict` | **strict** | entity names, queries, and event labels are sha256-hashed before storage |

Strict mode is intended for sensitive instances such as a `private_memory` server backing therapy/health/relationship content. Ranking debug ("which queries overfetch candidates?") becomes harder in strict mode because plaintext queries are no longer recoverable — but sensitive identifiers never land on disk.

Observation/content values are **always** reduced to `<N chars>` regardless of mode. Facts/observations arrays are **always** reduced to `<N items>`. Strict mode only changes what happens to identifier-like fields.

**Example `.env` for sensitive backend:**
```bash
MEMORY_TELEMETRY_PATH=/home/user/.local/state/workmem/private-telemetry.db
MEMORY_TELEMETRY_PRIVACY=strict
```

## What it logs

### Tool calls (`tool_calls` table)

Every MCP tool invocation is logged with:

| Column | Example |
|--------|---------|
| `ts` | `2026-04-14T20:15:32.456` |
| `tool` | `recall`, `remember`, `forget` |
| `client_name` | `kilo`, `claude-code`, `cursor`, `windsurf`, `vscode-copilot` |
| `client_version` | `0.43.6` |
| `client_source` | `protocol` / `env` / `none` |
| `db_scope` | `global` / `project` |
| `project_path` | resolved absolute path, or null |
| `duration_ms` | `12.4` |
| `args_summary` | Sanitized JSON (see below) |
| `result_summary` | Counts only, never data |
| `is_error` | `0` or `1` |

### Search ranking metrics (`search_metrics` table)

For `recall` calls, additional metrics capture the ranking pipeline:

| Column | Example |
|--------|---------|
| `tool_call_id` | FK into `tool_calls.id` |
| `query` | Search text (hashed in strict mode) |
| `channels` | `{"fts": 12, "fts_phrase": 3, "entity_exact": 1}` |
| `candidates_total` | `16` |
| `results_returned` | `5` |
| `limit_requested` | `20` |
| `score_min` | `0.32` |
| `score_max` | `0.87` |
| `score_median` | `0.61` |
| `compact` | `0` or `1` |

## What it does NOT log

- Observation content (replaced with `<N chars>`, always)
- Full result payloads — only counts (entities returned, observations stored, etc.)
- In strict mode, any identifier (entity name, query, event label, from/to)

## Client identity

The server identifies which client is calling through two mechanisms:

1. **MCP protocol** (primary) — the `initialize` handshake includes `clientInfo.name` and `clientInfo.version`. This is a required field in the MCP spec.
2. **Environment fingerprinting** (fallback) — when the protocol doesn't provide client info, the server detects the client from environment variables:

| Client | Signal |
|--------|--------|
| Kilo | `KILO=1` (version from `KILOCODE_VERSION`) |
| Claude Code | `CLAUDE_CODE_SSE_PORT` set |
| Cursor | `CURSOR_TRACE_ID` set |
| Windsurf | `WINDSURF_EXTENSION_ID` set |
| VS Code Copilot | `VSCODE_MCP_HTTP_PREFER` set non-empty |
| VS Code (unknown extension) | `TERM_PROGRAM=vscode` |

The `client_source` column tells you which mechanism fired: `protocol`, `env`, or `none`.

## Querying the data

The telemetry database is a standard SQLite file. Open it with any tool: `sqlite3`, DBeaver, Jupyter, pandas, etc.

### Example queries

**Tool usage by client:**
```sql
SELECT client_name, tool, COUNT(*) as calls,
       ROUND(AVG(duration_ms), 1) as avg_ms
FROM tool_calls
GROUP BY client_name, tool
ORDER BY calls DESC;
```

**Search ranking quality (permissive mode — query is plaintext):**
```sql
SELECT query, candidates_total, results_returned,
       ROUND(score_min, 3) as min, ROUND(score_max, 3) as max,
       channels
FROM search_metrics
ORDER BY candidates_total DESC
LIMIT 20;
```

**Overfetch detection (candidates >> returned):**
```sql
SELECT query, candidates_total, results_returned, limit_requested,
       ROUND(1.0 * results_returned / candidates_total, 2) as yield_ratio
FROM search_metrics
WHERE candidates_total > 0
ORDER BY yield_ratio ASC
LIMIT 20;
```

**Error rate by tool:**
```sql
SELECT tool, COUNT(*) as total,
       SUM(is_error) as errors,
       ROUND(100.0 * SUM(is_error) / COUNT(*), 1) as error_pct
FROM tool_calls
GROUP BY tool
ORDER BY error_pct DESC;
```

**Channel effectiveness:**
```sql
SELECT json_each.key as channel, COUNT(*) as appearances
FROM search_metrics, json_each(search_metrics.channels)
GROUP BY channel
ORDER BY appearances DESC;
```

## Separate database

Telemetry is stored in its own SQLite file, completely separate from `memory.db`. This means:

- Deleting the telemetry DB has zero impact on your knowledge graph
- The telemetry DB can be wiped and recreated at any time
- No foreign keys or joins between telemetry and memory data
- Telemetry uses `journal_mode=WAL` for concurrent reads while the server writes

## Init failure handling

If the telemetry path is invalid or the database can't be opened, the server prints a single warning to stderr and disables telemetry for the rest of the session. It does not retry on every call. The main `memory.db` is unaffected — telemetry failure never breaks the tool call path.

Example warning:
```
[memory] telemetry init failed (disabled for this session): unable to open database file
```

## Design rationale

**Why plaintext queries in permissive mode?** Local, single-user development: the telemetry DB lives on your machine, is gitignored, and is only readable by you. Redacting queries permissively would make the analytics useless — you can't answer "which queries produce too many candidates?" if the query text is hashed.

**Why privacy-strict mode?** For backends holding sensitive content (therapy, health, relationships, personal journaling), even local plaintext can matter: laptop loss, accidental sync-folder placement, or exported snapshots. Strict mode ensures entity names and queries never land on disk in the clear.

**Why a separate SQLite and not the memory DB?** Separation of concerns. The memory DB is your knowledge graph. Telemetry is operational data with a different lifecycle (wipe freely, aggregate, export). Mixing them risks accidentally leaking telemetry via `recall`, or losing telemetry history when the memory DB is rebuilt.

**Why not an MCP tool?** Telemetry is infrastructure, not a capability the model needs. Adding a tool would cost context tokens on every call for something only the human developer uses. Query the SQLite directly.

**Why a nil-tolerant `*Client`?** The alternative is `if TELEMETRY_ENABLED` checks sprinkled across every call site. The `nil`-receiver pattern keeps the dispatch code clean — the wrapper always calls `LogToolCall`; when telemetry is disabled, the client is `nil` and the method returns immediately.
