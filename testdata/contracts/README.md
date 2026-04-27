# Product Contract Fixtures

These fixtures define the minimum externally visible behavior that `workmem`
must preserve across refactors.

Files:

- `product-contract.json` holds user-visible behavior that must stay stable.
- `implementation-detail.json` holds lower-level invariants that matter because the product depends on them, even if users never call them directly.

Rules:

- Product contract cases should compare tool inputs and externally visible outcomes.
- Implementation detail cases should stay small and explicit; they exist to stop silent drift in tombstones, FTS cleanup, and project routing.
- New contract-sensitive work should extend these fixtures before adding large amounts of code.
