# Go migration — execution playbook & handover

> **STATUS (2026-07-10): MIGRATION COMPLETE.** All phases done and committed on `main`. Every command
> (`statusline`/`warn`/`status`/`check`/`run`/`review`/`rhythm`/`init`/`prune`) is Go, verified
> byte-identical to the Ruby reference; the Ruby tool + oracles are deleted. Conformance now runs
> against committed golden files (`conformance/golden/`, via `internal/golden`) — no Ruby dependency.
> CI/CodeQL/dependabot are Go-only. Post-v1 architecture options (measured) are in `docs/DECISIONS.md`.
> The doc below is retained as the historical execution record.

**Start-here doc for the Ruby → Go migration** (committed for v1). This is the *execution* plan; do
it in a fresh session with a clean context. Read alongside:

- `docs/standards/go.md` — the **how** (idioms, fail-open-via-recover, static-binary build,
  GoReleaser + Homebrew release path). Verify Go versions against go.dev before relying.
- `docs/RUST-REIMPL.md` — the **why & scope** (measurement, why Go, hot-path-first).
- `AGENTS.md` — the **invariants** that must survive the port (fail-open, near-stdlib, etc.).

## The one rule that makes this safe

**Reimplement in idiomatic Go — do NOT transliterate the Ruby line-by-line.** The Ruby is the
*spec and conformance oracle*, not the template. Match the **observable outputs and the on-disk
contract**, not the source structure. Write idiomatic Go per `go.md` (fail-open via `recover`,
typed constants for the `:fresh/:estimated/:stale` tiers, errors-as-values, small packages) —
transliterating Ruby idioms (rescue-modifiers, symbols, duck typing) produces bad Go and breaks the
invariants. We understand this tool deeply now; bake the lessons in cleanly rather than port warts.

**The Ruby + its ~160 hermetic `test_ccpool.rb` cases are the conformance oracle.** Correct = same
*output* for the same input. So:

1. Keep Ruby runnable and unchanged during the migration (reference, not legacy to delete).
2. Build a piece, then diff Go output against Ruby output over shared JSON fixtures (reuse the ones
   the Ruby tests build; pin `now`). **Byte-identical** for the rendered statusline string (ANSI
   included), the `warn` emit text + hook JSON, and every on-disk file it writes. **Equal at
   displayed precision** for the `$`/percent/pace math (compare the rounded output, not raw floats,
   so FP jitter can't fail a diff). Internals are free to differ; outputs and files are not.
3. Ruby and Go **coexist through the on-disk contract below** — a Go `statusline` and a Ruby
   `status` interoperate with zero shared process state (that's *why* the files must be
   byte-compatible). Migrate command-by-command; retire Ruby only when Go passes the whole suite.

## The on-disk contract (preserve byte-for-byte)

All under `~/.claude` (each overridable by the `CCPOOL_*`/`USAGE_*` env the tests use). The Go port
MUST read/write these exact shapes so it interoperates with any Ruby still running.

- **Per-session snapshots** — `usage-cache-<session_id>.json` (glob `usage-cache-*.json`).
  Contents = the **raw Claude Code statusLine payload** plus `"captured_at": <unix_int>`. Fields
  read: `rate_limits.{seven_day,five_hour}.{used_percentage, resets_at}`, `context_window.*`,
  `session_id`, `cost.total_cost_usd`. Written atomically (tmp + rename). Unknown keys pass
  through — never assume a field is present (mirror the Ruby `typed?` guards).
- **History log** — `rate-limit-history.jsonl`, one JSON object per line:
  `{"t":<int>, "wk":<num>, "wk_reset":<num>, "ses":<num|null>, "ses_reset":<num|null>,
  "tier":"max_20x", "cost":<num|null>, "session":"<id>"}`. Appended under an exclusive flock;
  per-session + ses-keyed dedup + a min-interval throttle on 5h-only writes (see `seed_history`).
- **Calibration cache** — `ccpool-calibration.json` = `{"dpp":<num>, "at":<int>}`.
- **ccusage blocks cache** — `ccpool-blocks-cache.json` = `{"raw":"<ccusage stdout>", "at":<int>}`.
- **Anomaly log** — `statusline.log`, capped ~200 lines.

`$/1%` is derived by delegating to `ccusage blocks --json` (pinned `@20`) over monotonic no-reset
runs in the history — never hand-rolled. The reset cadence is never modelled; read the reported
number.

## Phases (sequenced; each independently shippable)

**Phase 0 — pipeline before code.** Stand up the Go module + the release machinery FIRST, so every
later phase is installable from day one.
- `go mod init <module path>`; decide the package layout (`internal/pool`, `internal/calib`,
  `internal/statusline`, …; keep it lean).
- CI: a Go workflow (`go test ./...`, `go vet`, `staticcheck`, `govulncheck ./...`) replacing the
  Ruby lint/test jobs; keep the Ruby jobs until Ruby is retired.
- `.goreleaser.yaml` (v2) + `release.yml` (tag `v*` → cross-compile darwin/linux × amd64/arm64 →
  GitHub Release + Homebrew tap). Create the `homebrew-tap` repo + `HOMEBREW_TAP_TOKEN`. See
  `go.md` § Release engineering.
- **Done when:** a trivial `ccpool version` Go binary tags, releases, and `brew install`s.

**Phase 1 — port `statusline`** (the data *producer* — get it right first, everything reads what it
writes).
- Payload parse (stdin JSON) → capture snapshot + seed history (exact contract above) → render the
  full coloured line AND `--embed` compact line → the throttled, detached, fail-open calibration
  warm-up.
- **Done when:** Go `statusline` output is byte-identical to Ruby's over the fixture payloads
  (colour + plain), and it writes snapshots/history Ruby can still read.

**Phase 2 — port `warn`** (the consumer). Pace / 5h / context signals, the emit throttle, the
`UserPromptSubmit` (plain text) vs `PostToolUse` (`additionalContext` JSON) shapes. **Done when:**
signal set + emitted text match Ruby over the same snapshots.

**Phase 3 — port the on-demand commands** — `status`, `check`, `run` (env downshift + exec),
`review`, `rhythm`, `init`, `prune`. Then delete the Ruby once the full fixture suite passes in Go.

**v1 — the full Go binary, shipped via Homebrew.** Retire `bin/ccpool` (the launcher existed only
to paper over "which Ruby"). Flip CI/CodeQL/dependabot to Go (notes are inline in each file).

## Fail-open is the porting risk

Every hot-path entry (`warn`, `statusline`) needs a top-level `defer func(){ _ = recover() }()` so
a panic can't reach Claude Code. On-demand commands stay fail-loud. Re-read `go.md` § Philosophy
before writing the first hook.

## Decisions to lock at the top of the session

- Module path (e.g. `github.com/<owner>/ccpool`) — drives `go install` + GoReleaser.
- Package layout (resist over-structuring — it's a small CLI).
- OS/arch targets (recommend darwin + linux, arm64 + amd64; skip windows unless asked).
- Tap repo name (`<owner>/homebrew-tap`).
- Whether v1 ships ALL commands in Go or just the hot path with Ruby retained for the rest
  (RUST-REIMPL leans full-binary for the distribution win; confirm).

## Do NOT

Start the port as a side effect of something else; change the on-disk contract; add Ruby features
first (paid for twice); pull heavy Go deps (cobra/viper) for a tiny CLI; let a hook panic escape.
