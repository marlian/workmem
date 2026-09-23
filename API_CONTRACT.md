# API CONTRACT

## Intent

This document describes the current Go implementation's MCP and CLI behavior. Changes to the public tool surface require a deliberate, documented contract change.

## Current MCP tool surface

### Core tools

- `remember`
- `remember_batch`
- `recall`
- `recall_entity`
- `relate`
- `forget`
- `list_entities`

### Event and provenance tools

- `remember_event`
- `recall_events`
- `recall_event`
- `get_observations`
- `get_event_observations`

## Behavioral expectations

- `remember` creates or reuses an entity and stores an observation. If an
  identical active observation already exists on that entity, it reuses the
  observation ID instead of inserting a duplicate.
- `remember`, `remember_batch`, and `remember_event` accept `confidence`
  only in the inclusive `0.0-1.0` range when provided; out-of-range,
  NaN, or infinite values are validation errors and must not mutate the DB.
- `recall` returns observations grouped by entity, ranked using lexical
  retrieval and read-time confidence decay. Decay does not delete observations
  or rewrite stored content or confidence. Embeddings are not used by recall.
- `recall` returns full observation content by default. `compact: true`
  truncates returned observation snippets and marks shortened content with
  `truncated: true`; it is not semantic summarization or a total token budget.
  `get_observations` retrieves full content for selected visible IDs.
- `relate` creates directed relations between two distinct entities;
  self-referencing relations are validation errors under the same
  case-insensitive entity-name semantics used by entity lookup, and must not
  mutate the DB.
- `forget` soft-deletes observations or entities and must remove deleted observations from FTS recall.
- `list_entities` returns active entities that still carry active context:
  at least one active observation or at least one live incoming/outgoing
  relation. Empty shells with no active observations and no live relations are
  hidden; relation-only entities remain visible.
- `recall_entity` returns not found for empty shells with no active
  observations and no live relations. Relation-only entities return a graph
  with empty observations and their live relations.
- `project`-scoped calls route to an isolated per-project DB selected by the
  instance's `MEMORY_PROJECT_MODE`:
  - `legacy` (default): `<project>/.memory/memory.db`, created lazily.
  - `central`: `<MEMORY_PROJECTS_ROOT>/<id>/memory.db`, where `id` comes from
    `registry.db` keyed by the canonical project path (absolute, cleaned,
    symlinks resolved). The project directory must exist and is never written
    to. Different spellings of the same directory share one DB. The registry
    is consulted on every call, so a `project move` by another process takes
    effect immediately. Central mode never reads a legacy
    `<project>/.memory/memory.db`: an unregistered project with one fails with
    an import hint, and a registered project that still has one fails until
    it is moved out. A registered, initialized store whose file is missing,
    or a missing `registry.db` beside existing stores, fails instead of
    starting empty.
  - `disabled`: every non-empty `project` argument returns an error naming
    `MEMORY_PROJECT_MODE=disabled` before any filesystem access; global calls
    are unaffected.
- The project mode comes from the `serve -project-mode` flag, then
  `MEMORY_PROJECT_MODE`, then `legacy`. An unknown value, a relative
  `MEMORY_PROJECTS_ROOT`, or `MEMORY_PROJECTS_ROOT` without central mode stops
  startup. The default central root is `<global DB dir>/<global DB file
  stem>-projects`. `serve` also refuses to start when an explicit `-env-file`
  is missing or unreadable.
- provenance tools bypass ranking and return direct facts by identifier, but they must not bypass lifecycle visibility guards such as tombstones, supersession, or event expiry.
- Superseded observations are hidden from normal active-memory read surfaces:
  `recall`, `recall_entity`, `list_entities` active observation counts,
  `recall_events` observation counts, `recall_event`, `get_observations`, and
  `get_event_observations`.
- Supersession is explicit lifecycle state, not a conditional alias. A
  superseded observation stays hidden until rollback/repair clears its
  supersession marker; it is not automatically resurrected if the replacement
  observation later becomes inactive.
- `remember_event.expires_at`, when provided, must be a valid timestamp. Expired events and observations attached to expired events are hidden from normal read surfaces: `recall`, `recall_entity`, `recall_events`, `recall_event`, `get_observations`, and `get_event_observations`.

## Duplicate observation reuse

`remember`, `remember_batch`, and observations supplied to `remember_event`
share exact-content deduplication within an entity. Reusing an active observation
ID does not update that row's source, stored confidence, entity-type snapshot,
or event association. A plain `remember` of an active event-linked observation
does not detach it from the event or extend its visibility beyond that event's
expiry.

Current limitation: when `remember_event` creates event B with an observation
already active under event A (or with no event), it returns the existing ID
without attaching that row to B. `observations_attached` counts processed input
items, including reused IDs; it is not a guarantee that every returned ID
belongs to the new event. `recall_event(B)` returns only observations actually
linked to B. This cross-event behavior is tracked in
[OPERATIONS.md](OPERATIONS.md) pending a contract decision and regression coverage.

## Compatibility policy

The Go `workmem` implementation is the canonical product. API compatibility
means preserving the documented MCP contract, not chasing the legacy Node
implementation.

That means:

- preserve tool names
- preserve major argument names
- preserve response shape where practical
- document every intentional contract change

The product-contract fixtures live in `testdata/contracts/` and define the
minimum externally visible behavior that must stay stable across refactors.

## Allowed internal changes

- internal implementation details
- telemetry internals
- non-user-visible refactors

## Not allowed to drift silently

- forget semantics
- project routing behavior (including `MEMORY_PROJECT_MODE` semantics and the
  central registry layout)
- result grouping shape
- compact recall behavior
- provenance response shape without explicit migration notes

## Additive response extensions

The tool surface can grow *additively* on existing responses without changing
any tool name, argument name, or documented field. Clients that do not know
about a new field must keep working unchanged.

### `remember` — `possible_conflicts`

Motivated by the 2026-04-22 decision (`DECISION_LOG.md`). When
`remember` stores an observation on an entity, the backend runs the
composite ranker scoped to that entity's active observations and, if any score
at or above the internal similarity threshold, surfaces up to 3 of them on the
response:

```json
{
  "entity_id": 42,
  "observation_id": 999,
  "stored": true,
  "possible_conflicts": [
    {"observation_id": 877, "similarity": 0.87, "snippet": "..."}
  ]
}
```

Contract properties:

- The field is **optional**. Omitted entirely when there are no
  qualifying conflicts. Clients that ignore the field must keep
  working identically to the pre-extension response.
- The field is a **hint**, not a command. `remember` never soft-deletes or
  supersedes observations automatically. `forget` with `observation_id`
  tombstones the observation and removes its FTS entry and cached embeddings;
  it does not physically erase the stored observation content. Reversible
  supersession is reserved for the reconcile audit flow.
- The similarity score is a lexical signal derived from the existing
  composite ranker. It is not a semantic contradiction score and must
  not be documented as such.
- `forget` semantics are unchanged. Adding `possible_conflicts`
  extends `remember` only; nothing in the "Not allowed to drift silently"
  list moves.
- Superseded observations are not active observations and are not candidates for
  `possible_conflicts`. A later identical write can create a new active
  observation; deterministic reconcile propose reports those duplicates, and
  `workmem reconcile --mode apply` can collapse active exact duplicates through
  audited supersession.
- The similarity threshold is provisional at launch and calibrated via
  telemetry (`conflicts_surfaced` vs `conflicts_acted_on`). Threshold
  changes are implementation-internal and do not constitute a contract
  change.

## Reconcile exact duplicates

`workmem reconcile` is an offline CLI hygiene surface. It does not change the MCP
tool schema.

- `workmem reconcile --mode propose` is read-only. It scans active observations,
  reports exact duplicate `content` values within the same entity, and writes no
  audit rows.
- `workmem reconcile --mode apply` reruns the deterministic exact-duplicate scan
  inside a transaction using current DB visibility, validates that every
  source/target observation is still active, same-entity, exact-content, and
  non-self, then sets
  `observations.superseded_by`, `superseded_at`, `superseded_reason`, and
  `superseded_by_run` on source observations.
- Apply writes one `reconcile_runs` row per invocation and one
  `reconcile_decisions` row per duplicate group. `source_obs_ids` is encoded as
  a JSON array, `content_snapshot` stores the exact duplicated content at apply
  time, and the target is the newest active duplicate by `created_at DESC, id DESC`.
- `workmem reconcile rollback <run_id>` restores sources from an apply run only
  when current DB state still matches the audit record. It refuses rollback if a
  source/target was deleted, expired, moved to another supersession run, or no
  longer matches the original exact-duplicate pair. Rollback must target the same
  scope as the original apply run.
- Supersession does not delete FTS rows. Active read paths hide superseded rows;
  physical FTS cleanup remains tied to forget/tombstone behavior.

## Semantic reconcile report mode

`workmem reconcile semantic` has two modes:

- `--mode validate` validates semantic provider configuration only. It does not
  generate semantic candidates, write reports, call embedding endpoints, open a
  memory database, or mutate memory. Validate mode ignores report-only flag
  values so stale DB/output/scan/threshold arguments cannot pull validation into
  report behavior.
- `--mode report` opens an existing global or project DB, populates/reuses the
  `observation_embeddings` cache, and writes a markdown report of same-entity
  semantic candidates. The report groups pairwise candidates into observation
  clusters and includes manual decision checkboxes so review can proceed as a
  human-authored cleanup spec. It does not run schema migrations; if the DB is
  too old for semantic report mode, it fails closed. It has no apply path.

- The default embedding provider is `none`.
- Supported provider identifiers are `none`, `openai-compatible`, `ollama`, and
  `openai`.
- Non-`none` providers require an explicit base URL, model identifier, and vector
  dimension count.
- `openai` requires the explicit `--allow-remote-embeddings` flag.
  Local-provider URLs must use literal `localhost` or a loopback IP unless that
  flag is present; host aliases are not DNS-resolved for this trust decision.
- `openai-compatible` and `ollama` are supported by report mode. `openai` remains
  config-validatable but is rejected by report mode.
- Embedding storage lives in
  `observation_embeddings` keyed by observation, provider, endpoint key, model,
  and dimensions. Provider, endpoint key, and model must be non-blank after
  trimming; embedding bytes must be a non-empty BLOB.
- `forget` removes embedding rows for tombstoned observations/entities because
  observation deletion is soft-delete and SQLite FK cascade does not run there.
- Semantic apply is not part of this contract. Exact-duplicate apply remains the
  only reconcile mutation path.
- `--mode report` must remain read-only with respect to observations,
  supersession fields, reconcile audit rows, access counts, and FTS state;
  embedding-cache writes are the only semantic-side persistence it may perform.
- Candidate generation is same-entity only and must use active observations only:
  deleted, expired, and superseded observations are excluded.
- Report mode bounds provider and comparison work with explicit knobs:
  `--max-embedding-calls` caps uncached observations embedded in a run,
  `--max-embeddings-per-request` chunks provider requests,
  `--max-observations-per-entity` caps observations compared per entity, and
  `--max-candidates-per-entity` caps emitted candidates per entity. If per-entity
  caps truncate work, the markdown report must include limit signals.
- Provider request errors may include sanitized transport causes or HTTP status,
  but must not include response bodies, prompts, endpoint keys, credentials, or
  memory content. The markdown report itself intentionally includes bounded
  candidate snippets for human review and is written as a local private file.
- Optional model-assisted cleanup proposals are outside the executable API
  contract. Users may review report files with a model/provider they choose, but
  `workmem` must not call an LLM or apply proposal output as part of semantic
  report mode.

## Project store commands

`workmem project` manages the central project store and requires
`MEMORY_PROJECT_MODE=central`; in other modes it exits non-zero without touching
anything. All subcommands accept `-db` (global DB path, used to derive the
default root) and `-env-file`; pass the instance's env-file so they resolve the
same root as its server. Relative paths resolve from the current directory
(unlike the MCP `project` argument, which resolves relative paths from home).

- `project list` prints the root and, per registry entry, id, canonical path,
  whether the path still exists, and the DB size (`MISSING` when an initialized
  store has no file). It fails when no registry exists yet.
- `project import -from <db> -path <dir>` copies an existing memory DB into the
  store and registers it for `<dir>`. The source is opened read-only: its
  database content is never modified and it is never removed (SQLite may
  create `-shm`/`-wal` sidecars beside a WAL-mode source); it must be a
  regular file outside the store root
  and pass `PRAGMA integrity_check`. The copy (`VACUUM INTO`) is migrated to
  the current schema and integrity-checked before the registry row is written,
  so no server can observe a partial import. Importing an already registered
  path fails; a failed import leaves no directory behind. When a legacy DB is
  still present in `<dir>`, the command says so: the project stays refused
  until that `.memory/` directory is moved out.
- `project move [-replace-empty] <old> <new>` re-points one registry entry.
  `<old>` is matched as given before symlink resolution, so it may no longer
  exist or may now be a compatibility symlink to `<new>`. `<new>` must be an
  existing directory. If `<new>` is already registered the move fails, unless
  `-replace-empty` is set and that store holds no entities, observations or
  events; it is then archived under `<root>/discarded/`, never deleted. The
  moved DB file does not move. If the archive rename fails (on Windows, while
  another process still has that store open) the whole move rolls back; stop
  sessions that used the new path and retry.

`reconcile` and `reconcile semantic --mode report` with `--scope project=<path>`
resolve the project DB through the same policy. In `central` mode they only
open an already registered store and never create a root, registry, entry or
DB; errors name the root that was searched. Their scope label is
`project:<registry id>` rather than the path, so `reconcile rollback` of a run
applied before a `project move` still matches after it. Legacy mode keeps
`project:<path>`. In `disabled` mode they fail.
