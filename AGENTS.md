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

## Implementation: Go (single static binary)

ccpool is **Go** — one static binary, no runtime deps beyond optional `ccusage` for the `$`.
`docs/standards/go.md` records the idioms (fail-open via `recover`, near-stdlib, the GoReleaser +
Homebrew release path). Package layout is lean `internal/*` (one concern per package:
`pool`, `calib`, `statusline`, `warn`, `report`, …); `main.go` dispatches on `os.Args[1]`.

## Commands

- **Test / gate:** `make check` (gofumpt + vet + staticcheck + govulncheck + `go test ./...`). Must
  be green before any commit. Prefix go commands with `unset GOROOT` if a stale GOROOT is exported.
- **Conformance:** the `internal/*/conformance_test.go` suites diff Go output against committed
  **golden files** (`conformance/golden/`), Go-defined. After
  an intentional output change, refresh with `CCPOOL_UPDATE_GOLDEN=1 go test ./...` and review the diff.
- **Run:** `go run . <cmd>` or `make build && ./ccpool <cmd>`. Bare `ccpool` → `status`.
- Preview the statusline without Claude Code: `./ccpool statusline` in a terminal (renders the
  freshest snapshot). Drive real commands with the hermetic `CCPOOL_*`/`USAGE_*` env to stage fixtures.

## Design invariants — deliberate choices, defended (don't "fix" these)

These look arbitrary or smell like problems to an agent doing a reflexive cleanup. They are
intentional. Changing one needs a reason in `docs/DECISIONS.md`, not a tidy-up.

- **Fail OPEN, everywhere on the hot path.** Hooks (`warn`) and the statusline must NEVER panic out
  — a crash there breaks Claude Code itself. A top-level `defer func(){ recover() }()` at each hook
  entry, plus best-effort helpers that return a zero value on any error, are **correct here** — do
  not "improve" them into a propagated panic that could escape. On-demand commands (`status`,
  `check`, `init`) are the opposite: they fail LOUD (see `init` aborting rather than clobbering).
- **Lean `internal/*` packages, one concern each.** `internal/pool`, `internal/calib`,
  `internal/warn`, etc. Resist over-structuring (no DI, no interface-per-struct) — it's a small CLI.
  Near-stdlib and ~zero shipped deps by design; don't pull cobra/viper for a `switch` on `os.Args[1]`.
- **Conformance is golden-file, not a framework.** `internal/*/conformance_test.go` stage the shared
  `conformance/*_fixtures.json`, run the Go code, and byte-diff against `conformance/golden/` (via
  `internal/golden`). Ruby-semantics shims live in `internal/rb`; `json.Number` preserves the
  int/float distinction the on-disk contract depends on.
- **Delegate every dollar to `ccusage`; never hand-roll pricing.** The `$` is ccusage's number,
  self-calibrated per your usage. Don't add a pricing table.
- **Trust the reported `rate_limits` number; never model/reverse-engineer the reset cadence.** The
  mechanics churn ~monthly and past "reset patterns" were an Anthropic bug. Read the live number.
- **Delight via sensible defaults, not config-everything.** A fresh user needs ZERO env vars.
  Threshold knobs exist as undocumented escape hatches; don't promote them to the README, and don't
  add a new `CCPOOL_*` when a good default would do. (See `docs/CONFIG-AUDIT.md`.)
- **ccusage pinned `@20`** with a fail-loud schema probe. Don't float `@latest`.

## House style (Go)

- Idiomatic Go per `docs/standards/go.md`: errors as values (`%w`, `errors.Is`), typed constants for
  the `fresh/estimated/stale`-style tiers, comma-ok on every type assertion (adversarial payloads
  must not panic), small concrete types over interfaces. Read env fresh per call (honours test env).
- Terse WHY-comments over what-comments. Explain the non-obvious decision, not the syntax.
- Match the surrounding file's density and naming; short math/time locals (`h`, `t`, `n`, `r`) are
  conventional. `gofumpt` + `staticcheck` are the gate — keep both clean.

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
- **Reproducible**: bug repros don't depend on your machine; include ccpool + `go version` and the
  exact command.
- **Prior art**: you checked the authoritative tool/spec before hand-rolling a heuristic.
- A PoC that shows the behaviour or the bug is the fastest path to merge.

## AI assistance

Welcome, and used here. It doesn't lower the bar above or your ownership: you ran it, you stand
behind it. Disclosing is appreciated, never penalized.
