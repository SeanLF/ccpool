# Contributing to ccpool

Thanks for helping. ccpool is a small, opinionated tool; the bar is less about volume and more
about care. `AGENTS.md` has the working conventions and the design invariants — read it first, it
also applies to you.

## Before you start

- Skim `README.md` (what it does) and `docs/DECISIONS.md` (why it's shaped this way — a lot of
  "obvious" changes were already tried and deliberately rejected there).
- ccpool is a single static **Go** binary. Before starting anything large, open an issue first.

## What makes a contribution land

- **Verified** — you ran it the way a user actually invokes it and observed the behaviour. No
  version, compatibility, or output asserted from memory. For anything on the fail-open path
  (`warn`, `statusline`), confirm it still can't raise — that path must never break Claude Code.
- **Reproducible** — bug reports include steps that don't depend on your machine, plus the ccpool
  and `go version` and the exact command/output.
- **Prior art** — you checked how the authoritative tool or spec handles it before hand-rolling a
  heuristic (this is how ccpool avoids re-inventing ccusage's pricing, Anthropic's reset cadence,
  etc.).
- **A PoC that shows the behaviour or the bug is the fastest path to merge.**

## Running it

```sh
make check           # gofumpt + vet + staticcheck + govulncheck + go test ./... — must be green
go run . <cmd>       # or: make build && ./ccpool <cmd>
```

`make check` must pass before you open a PR. Conformance suites diff each command against committed
golden files and fake `~/.claude` via `CCPOOL_*` env, so they never touch your real usage data — use
the same pattern to stage fixtures. Refresh goldens after an intentional change with
`CCPOOL_UPDATE_GOLDEN=1 go test ./...`.

## Style

`AGENTS.md` covers it: fail-open on the hot path, one concern per file, terse why-comments, no em
dashes, conventional commits (no emoji, explain *why*). Match the surrounding code.

## AI assistance

Welcome, and used here too. It doesn't change the bar above and it doesn't lower your ownership:
you ran it, you stand behind it. Disclosing that you used it is appreciated, never penalized.
