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