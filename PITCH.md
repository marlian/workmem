# PITCH

## One sentence

workmem is a local-first MCP memory server distributed as a single native Go
binary.

## Problem

The product promise is:

- zero infrastructure
- local-first
- just works

Many local MCP tools weaken that promise with avoidable installation friction:

- require a language runtime
- require package-manager install steps
- pulls a non-trivial dependency tree
- rely on native bindings or environment-specific builds
- can fail for reasons unrelated to the product itself

For a tool whose whole value proposition is low friction, the install story is part of the product.

## Thesis

The product should feel like its own promise.

workmem keeps the distribution story simple:

"brew install workmem or download a binary and point your MCP client at it"

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
- a simple MCP memory model that stays local

## Product bar

workmem succeeds only if it is both:

- easy to install on a fresh machine
- behaviorally trustworthy enough to hold real local memory

If it ships as a pretty binary but drifts on recall, forget semantics, project scoping, or search ranking, it fails.
