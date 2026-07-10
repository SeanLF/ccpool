# Contributing to ccpool

Thanks for helping. ccpool is a small, opinionated tool; the bar is less about volume and more
about care. `AGENTS.md` has the working conventions and the design invariants — read it first, it
also applies to you.

## Before you start

- Skim `README.md` (what it does) and `docs/DECISIONS.md` (why it's shaped this way — a lot of
  "obvious" changes were already tried and deliberately rejected there).
- **Heads up:** ccpool is currently Ruby but migrates to **Go before v1**. Small Ruby fixes are
  welcome now; before starting anything large, open an issue so we don't build on a floor that's
  about to move.

## What makes a contribution land

- **Verified** — you ran it the way a user actually invokes it and observed the behaviour. No
  version, compatibility, or output asserted from memory. For anything on the fail-open path
  (`warn`, `statusline`), confirm it still can't raise — that path must never break Claude Code.
- **Reproducible** — bug reports include steps that don't depend on your machine, plus the ccpool
  and `ruby -v` versions and the exact command/output.
- **Prior art** — you checked how the authoritative tool or spec handles it before hand-rolling a
  heuristic (this is how ccpool avoids re-inventing ccusage's pricing, Anthropic's reset cadence,
  etc.).
- **A PoC that shows the behaviour or the bug is the fastest path to merge.**

## Running it

```sh
ruby test_ccpool.rb   # hermetic test suite, no deps — must be all-green
rubocop               # lint — must be clean
ruby ccpool.rb <cmd>  # or bin/ccpool <cmd>
```

Both must pass before you open a PR. Tests are hermetic (they fake `~/.claude` via `CCPOOL_*`
env), so they never touch your real usage data — use the same pattern to stage fixtures.

## Style

`AGENTS.md` covers it: fail-open on the hot path, one concern per file, terse why-comments, no em
dashes, conventional commits (no emoji, explain *why*). Match the surrounding code.

## AI assistance

Welcome, and used here too. It doesn't change the bar above and it doesn't lower your ownership:
you ran it, you stand behind it. Disclosing that you used it is appreciated, never penalized.
