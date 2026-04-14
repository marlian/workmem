# PITCH

## One sentence

workmem is the native binary version of mcp-memory: same local-first memory server, same MCP workflow, but distributed as a single executable instead of a Node application with native addons.

## Problem

The current product pitch is effectively:

- zero infrastructure
- local-first
- just works

The current installation story weakens that pitch:

- requires Node
- requires `npm install`
- pulls a non-trivial dependency tree
- relies on native bindings in the current implementation
- can fail for reasons unrelated to the product itself

For a tool whose whole value proposition is low friction, the install story is part of the product.

## Thesis

The product should feel like its own promise.

A Go binary changes the distribution story from:

"clone repo, install dependencies, hope your environment likes native builds"

to:

"brew install mcp-memory or download a binary and point your MCP client at it"

That is not a cosmetic improvement. It is product alignment.

## Why Go

- single static binary distribution
- strong cross-compilation story in CI
- very low memory overhead for a stdio server
- battle-tested fit for local tools and SQLite-backed utilities
- easy packaging for Homebrew and release artifacts

## Core promise

Users should get:

- one file
- no runtime prerequisite
- no dependency install step
- no infrastructure to manage
- the same mental model as today

## Product bar

This rewrite succeeds only if it is both:

- easier to install than the current server
- behaviorally trustworthy enough to replace it

If it ships as a pretty binary but drifts on recall, forget semantics, project scoping, or search ranking, it fails.