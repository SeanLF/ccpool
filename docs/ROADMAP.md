# ccpool — v2 roadmap (release-prep)

_Working plan promoted from scratch/. Validated by spikes (2026-07-10); rationale in docs/DECISIONS.md._

Migration complete (all commands Go, byte-verified, Ruby deleted, golden conformance, one static
binary). This is the v2 pass. Rationale + measurements: `docs/DECISIONS.md`. Framing: size is noise,
runtime deps are fine, byte-identity-to-Ruby is dead (goldens are Go-defined) — decide on merit.
ccusage stays. **Scope: single user today; releasing to outside users is the goal AFTER this v2 pass
— so treat it as release-prep.** No legacy user base to migrate (reformat the on-disk store freely
now), but a robust store (B, with your history imported) and adoption UX (C) are on the path to
release, not optional. The dormant Phase-0 release pipeline (GoReleaser → Homebrew tap) gets activated
at the end.

Verify changes by re-baselining goldens (`CCPOOL_UPDATE_GOLDEN=1 go test ./...`) and reviewing the diff.

**Hot-path acceptance gate (statusline / warn):** any new dep on a render path must degrade to
silent/plain on failure, never panic out to Claude Code. Keep the top-level `recover()`. (Fuzz below
confirms the current parsers already hold this.)

---

## Pre-handoff spike findings (all resolved — these were the plan's shaky assumptions)

- **A1 viable — CONFIRMED.** `lipgloss.SetColorProfile(termenv.TrueColor)` emits real truecolor on
  Claude Code's non-tty pipe (beats termenv's auto-strip); 256/16 degradation is a one-constant swap
  (library does the colour-matching); no panic on degenerate input. **Gotcha (hard requirement):**
  forcing the profile *bypasses* NO_COLOR, so gate manually — `if out.EnvNoColor() { profile = termenv.Ascii }`.
  **Honest value reframe:** from a non-tty hook we still can't *auto-detect* the target terminal, so
  A1 buys a cleaner rendering layer + proper NO_COLOR + **opt-in** degradation (driven by a
  `COLORTERM` passthrough or a `CCPOOL_COLOR` knob) — NOT an automatic fix for the 16-colour case. So
  it's "cleaner + explicit control," worth doing, but not the urgent bugfix I first framed it as.
- **A4 fail-open — CONFIRMED clean.** 7 fuzz targets, ~60M execs, zero panics across all parsers.
  4 vet-clean fuzz files added (`internal/{rb,statusline,calib,history}/*_fuzz_test.go`) — the first
  A4 increment; assert "never panics" (the fail-open contract), correctness stays with the goldens.
- **B envelope→SQL — CONFIRMED, with one load-bearing condition.** `Burn.envelope` reduces to a
  single query producing byte-identical output on the real 56k-row history — **only** with
  `max(wk) OVER (ORDER BY t, rowid ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)`. SQLite's
  *default* `RANGE` frame lumps tied timestamps into one peer group and diverges — and this history is
  **76% tied timestamps**, so the naive query would silently corrupt burn. Also requires insertion
  order == arrival order (for the `rowid` tie-break). This is B's real risk, now known up front.
- **Live data-hygiene finding (independent of any rework):** the real `rate-limit-history.jsonl` has a
  synthetic `{"wk":30,"wk_reset":9999999999,"session":"bench"}` sentinel — it holds the max `wk_reset`,
  so it becomes "the current window" and **collapses the weekly burn envelope to a single sample right
  now**. Purge it to restore weekly burn/runway. One-liner, worth doing regardless of storage.

---

## Sprint A — hardening + cleanup (the near-term plan; start here)

**A4. Test hardening (started — highest value / lowest risk).** Fuzz targets exist + pass (above).
Remaining: `pgregory.net/rapid` property tests on the math (pace `elapsed_fraction ∈ [0,1]`, runway
monotonic in burn rate, burn ≥ 0, reconcile = newest-reset/max-used); `testscript` `.txtar` CLI e2e
(status/check/init dry-run). Optionally commit seed corpora.

**A2. Config parsing cleanup.** Drop `rb.ToI`/`ToF` for ENV config → `strconv` + validation + clear
errors. Keep `internal/rb` only for the on-disk `json.Number` data contract. Conformance stays green.

**A3. Diagnostics → `log/slog`** (stdlib) for the anomaly/warm-calib trail.

**A5. Tighten `calib.blocksArray`** — drop the "first array-valued field" fallback so a real ccusage
schema rename trips the fail-loud probe. (Confirmed: `ccusage@20` via npm IS the Rust binary, schema
`blocks`/`startTime`/`isGap`, our parser matches; one schema in play, no adapter needed.)

**A1. Terminal → `lipgloss` + `termenv`** (viable per spike). Pattern: force profile, gate on
`out.EnvNoColor()` for NO_COLOR, expose an explicit `CCPOOL_COLOR=truecolor|256|16|auto` override for
degradation (no auto-detect from a non-tty). Replace `palette`/meter/`sev`; re-baseline statusline
goldens; eyeball a truecolor/256/16/NO_COLOR matrix; keep the `recover()`. Medium value (cleaner +
control), not a critical fix — sequence after the A2–A5 cleanups.

---

## Sprint B — SQLite storage (on the release path; sequence after A)

Not a bugfix (flock passes 8-way concurrency) but it dissolves the subtlest bespoke code (tail-dedup,
glob reconcile, prune, and `Burn.envelope` → the query above) — worth it for a tool about to have
outside users, and the merit is concurrency across *processes* (multiple Claude Code windows), which
one user already has.

Schema draft + query mapping as earlier (snapshots/history/kv tables; reconcile/data-age/prune →
queries). **Load-bearing:** the `ROWS ... UNBOUNDED PRECEDING` frame + `ORDER BY t, rowid` +
arrival-order inserts (per the B spike). `sqlc` for typed queries, no ORM.

**Importer — a one-off script, NOT Go/product code.** It only migrates *your* existing
`rate-limit-history.jsonl` into the DB once; outside users install fresh (empty DB, self-populates),
so there is no importer in the shipped binary. A throwaway `sqlite3`/python one-liner: read the JSONL
in file/arrival order (preserves the `rowid` tie-break), **skip the synthetic `session="bench"` /
`wk_reset=9999999999` sentinel**, `INSERT` into `history`. The Go side only owns the schema + read/write.

Verify via the golden suite (identical status/check/burn output IS the proof) + a hot-path bench + the
fail-open gate. Keep unit tests for the non-numeric-reset and `hasLatest==false` branches (no SQL
analogue). Cost: +4 MB / +3.3 ms, both noise.

---

## Sprint C — cobra CLI (pre-release; deferred but on the path)

Since release is the goal, adoption UX earns its place — shell completions, consistent `--help`,
man-pages. cobra (+0.90 MB / 7 modules; skip `fang`). Map `switch os.Args[1]` → cobra commands,
keeping the entry funcs (`statusline.Command`, `warn.Hook`, `status.Report`, …) as bodies. Do it
close to release (after A/B), so the CLI surface stabilises first. Verify: `testscript` e2e + help goldens.

## Release (after A–C) — activate the dormant Phase-0 pipeline

The GoReleaser/Homebrew machinery already exists (Phase 0) but has never fired. To ship to outside
users: push to `github.com/SeanLF/ccpool` (remote is configured locally, unpushed); create the
`SeanLF/homebrew-tap` repo + a `HOMEBREW_TAP_TOKEN` PAT secret; then `git tag vX.Y.Z && git push
--tags` → GoReleaser cross-compiles + publishes the Release + the Homebrew cask. Pre-flight: a real
in-Claude-Code statusline screenshot for the README, and a fresh `ccpool init` walkthrough.

---

## Not doing (decided): native cost calc (pricing churn — ccusage-delegation invariant), config
framework (fights zero-config defaults), ORM, `x/text`, `fang`, a Go↔Rust FFI bridge to ccusage (cgo
kills static/cross-compile; no C ABI; off the hot path + cached), a ccusage schema adapter (one schema
in play). Node-runtime lever if wanted: `cargo install` ccusage (same schema) + `CCPOOL_CCUSAGE_CMD`.
