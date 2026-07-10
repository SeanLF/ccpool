# AGENTS.md — working in ccpool

Agent-facing guide for this repo (humans welcome too). `CLAUDE.md` / `GEMINI.md` symlink here —
edit this file only. Read `README.md` for what ccpool does and `docs/DECISIONS.md` for *why* it's
shaped the way it is (that log is authoritative when this file and your instincts disagree).

## What this is

ccpool is a small CLI that helps get the most out of a fixed Claude subscription pool: it fuses
the account-global `rate_limits` % (which ccusage can't see) with a ccusage-calibrated `$/1%` into
a dollar value + pace verdict, warns mid-turn via Claude Code hooks, downshifts subagent
model/effort when ahead of pace, and renders a statusline. It reads local `~/.claude` data and
delegates every dollar to `ccusage`.

## ⚠ Language migration: Ruby now → Go before v1

The current implementation is **Ruby** (`*.rb`, one concern per file). **Before v1 ships, the hot
path — and likely the whole tool — migrates to Go** (a single static binary; distribution is the
driver, see `docs/RUST-REIMPL.md`). Consequences for you:

- **Don't over-invest in Ruby-specific cleverness** that a rewrite throws away. Favour changes that
  translate cleanly to Go.
- **The DESIGN INVARIANTS below are language-agnostic and carry across the migration.** They are
  the durable contract; the Ruby idioms are not.
- Don't start the Go port unprompted — it's a scoped, sequenced piece of work, not a side effect
  of a feature. When it's time: `docs/standards/go.md` is the how (idioms, fail-open-via-recover,
  single-binary build, GoReleaser + Homebrew release path); `docs/RUST-REIMPL.md` is the scope.

## Commands

- **Test:** `ruby test_ccpool.rb` — one hermetic file, no deps, redirects `~/.claude` to a tempdir
  via `CCPOOL_*` env before requiring the modules. Must be all-green before any commit.
- **Lint:** `rubocop` — config in `.rubocop.yml` (Ruby 4.0). Must be clean before any commit.
- **Run:** `ruby ccpool.rb <cmd>` or `bin/ccpool <cmd>` (launcher: PATH ruby, falls back to mise).
- Preview the statusline without Claude Code: `ruby ccpool.rb statusline` (bare → renders the
  freshest snapshot). Drive real commands with the hermetic `CCPOOL_*` env to stage fixtures.

## Design invariants — deliberate choices, defended (don't "fix" these)

These look arbitrary or smell like problems to an agent doing a reflexive cleanup. They are
intentional. Changing one needs a reason in `docs/DECISIONS.md`, not a tidy-up.

- **Fail OPEN, everywhere on the hot path.** Hooks (`warn`) and the statusline must NEVER raise —
  a crash there breaks Claude Code itself. A `rescue StandardError` that swallows and continues, or
  a best-effort helper that returns `nil` on any error, is **correct here** — do not "improve" it
  into a raise or a narrower rescue that lets something escape. On-demand commands (`status`,
  `check`, `init`) are the opposite: they fail LOUD (see `init` aborting rather than clobbering).
- **One concern per file, flat in the repo root.** No `lib/`, no nesting. `pool.rb`,
  `calibration.rb`, `warn.rb`, etc. Don't reorganize into a gem layout — the flat layout is
  deliberate and the Go port won't inherit it anyway.
- **A single hermetic `test_ccpool.rb`.** Not a `test/` tree, not per-file specs. It sets `CCPOOL_*`
  paths to a tempdir *before* `require`, so nothing touches real `~/.claude`. Don't split it or add
  a framework (no minitest/rspec) — plain asserts, one file, fast.
- **Delegate every dollar to `ccusage`; never hand-roll pricing.** The `$` is ccusage's number,
  self-calibrated per your usage. Don't add a pricing table.
- **Trust the reported `rate_limits` number; never model/reverse-engineer the reset cadence.** The
  mechanics churn ~monthly and past "reset patterns" were an Anthropic bug. Read the live number.
- **Delight via sensible defaults, not config-everything.** A fresh user needs ZERO env vars.
  Threshold knobs exist as undocumented escape hatches; don't promote them to the README, and don't
  add a new `CCPOOL_*` when a good default would do. (See `docs/CONFIG-AUDIT.md`.)
- **ccusage pinned `@20`** with a fail-loud schema probe. Don't float `@latest`.

## House style (Ruby, current)

- Idioms in use and welcome: `it`/`_1`, shorthand hash, `then`/`tap` chaining, endless `def x = …`,
  guard clauses, `rescue`-modifier one-liners on best-effort reads. Avoid metaprogramming.
- Terse WHY-comments over what-comments. Explain the non-obvious decision, not the syntax.
- Match the surrounding file's density and naming; single-letter math/time locals (`h`, `t`, `n`,
  `r`) are conventional here.

## Prose, docs, commits

- **No em dashes.** Use commas, semicolons, periods, or restructure. US-keyboard characters,
  Canadian spelling.
- **Conventional commits, no emoji, explain *why* not *what*.** Squash debug/fix chains before
  pushing. End commit messages with the `Co-Authored-By` trailer if AI-assisted.
- Working docs go in `docs/` (committed) or `scratch/` (gitignored). Never commit plan/TODO/scratch
  files unless asked.

## What lands a change

- **Verified**: you ran it the way a user would and observed the behaviour — not asserted a
  version, compatibility, or output from memory. For anything on the fail-open path, confirm it
  still can't raise.
- **Reproducible**: bug repros don't depend on your machine; include ccpool + `ruby -v` (post-Go:
  `go version`) and the exact command.
- **Prior art**: you checked the authoritative tool/spec before hand-rolling a heuristic.
- A PoC that shows the behaviour or the bug is the fastest path to merge.

## AI assistance

Welcome, and used here. It doesn't lower the bar above or your ownership: you ran it, you stand
behind it. Disclosing is appreciated, never penalized.
