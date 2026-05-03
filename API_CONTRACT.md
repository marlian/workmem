# API CONTRACT

## Intent

The Go implementation should preserve the MCP tool surface unless there is a deliberate, documented reason to change it.

## Initial compatibility target

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

- `remember` stores or reuses an entity and appends a fact.
- `remember`, `remember_batch`, and `remember_event` accept `confidence`
  only in the inclusive `0.0-1.0` range when provided; out-of-range,
  NaN, or infinite values are validation errors and must not mutate the DB.
- `recall` returns ranked grouped results with confidence and composite score semantics preserved as closely as practical.
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
- `project`-scoped calls route to an isolated DB under the target project.
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
- project routing behavior
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
above a conservative similarity threshold, surfaces up to 3 of them on the
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
- The field is a **hint**, not a command. The backend never
  soft-deletes or supersedes on the agent's behalf. `forget(observation_id)`
  remains a deletion/privacy-erasure path. Reversible supersession is reserved
  for the reconcile audit flow.
- The similarity score is a lexical signal derived from the existing
  composite ranker. It is not a semantic contradiction score and must
  not be documented as such.
- `forget` semantics are unchanged. Adding `possible_conflicts`
  extends `remember` only; nothing in the "not allowed to drift early"
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
- Apply writes one `reconcile_runs` row and one `reconcile_decisions` row per
  duplicate group. `source_obs_ids` is encoded as a JSON array,
  `content_snapshot` stores the exact duplicated content at apply time, and the
  target is the newest active duplicate by `created_at DESC, id DESC`.
- `workmem reconcile rollback <run_id>` restores sources from an apply run only
  when current DB state still matches the audit record. It refuses rollback if a
  source/target was deleted, expired, moved to another supersession run, or no
  longer matches the original exact-duplicate pair. Rollback must target the same
  scope as the original apply run.
- Supersession does not delete FTS rows. Active read paths hide superseded rows;
  physical FTS cleanup remains tied to forget/tombstone behavior.

## Semantic reconcile substrate

`workmem reconcile semantic` is a post-v0 substrate command. It validates semantic
provider configuration only; it does not generate semantic candidates, write
reports, call embedding endpoints, open a memory database, or mutate memory.

- The default embedding provider is `none`.
- Supported provider identifiers are `none`, `openai-compatible`, `ollama`, and
  `openai`.
- Non-`none` providers require an explicit base URL, model identifier, and vector
  dimension count.
- `openai` requires the explicit `--allow-remote-embeddings` flag.
  Local-provider URLs must use literal `localhost` or a loopback IP unless that
  flag is present; host aliases are not DNS-resolved for this trust decision.
- Embedding storage, when populated by future semantic work, lives in
  `observation_embeddings` keyed by observation, provider, endpoint key, model,
  and dimensions. Provider, endpoint key, and model must be non-blank after
  trimming; embedding bytes must be a non-empty BLOB.
- `forget` removes embedding rows for tombstoned observations/entities because
  observation deletion is soft-delete and SQLite FK cascade does not run there.
- Semantic apply is not part of this contract. Exact-duplicate apply remains the
  only reconcile mutation path.
