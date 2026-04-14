# Compatibility Contract Fixtures

These fixtures define the minimum Node-vs-Go comparison surface for the port.

Files:

- `product-contract.json` holds user-visible behavior that must survive the rewrite.
- `implementation-detail.json` holds lower-level invariants that matter because the product depends on them, even if users never call them directly.

The reference implementation for these fixtures is `marlian/mcp-memory`.

Rules:

- Product contract cases should compare tool inputs and externally visible outcomes.
- Implementation detail cases should stay small and explicit; they exist to stop silent drift in tombstones, FTS cleanup, and project routing.
- New parity work should extend these fixtures before adding large amounts of code.