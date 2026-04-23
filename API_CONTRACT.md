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
- `recall` returns ranked grouped results with confidence and composite score semantics preserved as closely as practical.
- `forget` soft-deletes observations or entities and must remove deleted observations from FTS recall.
- `project`-scoped calls route to an isolated DB under the target project.
- provenance tools bypass ranking and return direct facts by identifier.

## Compatibility policy

Until the Go port is clearly stronger than the Node server, API compatibility beats elegance.

That means:

- preserve tool names
- preserve major argument names
- preserve response shape where practical
- document every intentional divergence

The initial comparison fixtures live in `testdata/contracts/` and are the baseline for Step 1.3 parity work.

## Allowed early deviations

- internal implementation details
- telemetry internals
- non-user-visible refactors

## Not allowed to drift early

- forget semantics
- project routing behavior
- result grouping shape
- compact recall behavior
- provenance response shape without explicit migration notes

## Additive response extensions (post-parity)

Once core parity is proven and the product starts earning its own
evolutionary decisions, the tool surface can grow *additively* on
existing responses without changing any tool name, argument name, or
documented field. Clients that do not know about a new field must keep
working unchanged.

### `remember` — `possible_conflicts`

Motivated by the 2026-04-22 decision (`DECISION_LOG.md`). When
`remember` stores an observation on an entity, the backend runs the
composite ranker scoped to that entity's non-deleted observations and,
if any score above a conservative similarity threshold, surfaces up to
3 of them on the response:

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
  soft-deletes on the agent's behalf. Supersession is the agent's
  decision, performed via `forget(observation_id)`.
- The similarity score is a lexical signal derived from the existing
  composite ranker. It is not a semantic contradiction score and must
  not be documented as such.
- `forget` semantics are unchanged. Adding `possible_conflicts`
  extends `remember` only; nothing in the "not allowed to drift early"
  list moves.
- The similarity threshold is provisional at launch and calibrated via
  telemetry (`conflicts_surfaced` vs `conflicts_acted_on`). Threshold
  changes are implementation-internal and do not constitute a contract
  change.