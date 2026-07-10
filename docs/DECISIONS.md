# ccpool — exploration, research & decisions (2026-07-09)

A dense one-day session. This records what we tried, what we rejected **and why**, the
research, the competitor/market analysis, and the decisions — so none of it is lost to
context compaction or the scratch wipe. (Governor-era detail also in
`~/Developer/_dotfiles/scratch/ccgovern-poc/FINDINGS.md`, which is gitignored/ephemeral.)

## TL;DR — where we landed

- Started at "extract dotfiles usage tooling into shareable mini-repos"; explored a **Claude
  Code budget *governor*** as the product; **banked the governor product** after ~40 rounds
  of deflation; **built `ccpool`** instead — a personal free tool for "get the most out of a
  fixed Claude subscription pool."
- Four differentiators no incumbent has: **pool $-value** (fuse rate_limits % × ccusage cost),
  **pace-aware fan-out downshift** (env vars — the only working lever), **provisioning review**,
  and a **rich coloured statusline**. DECISION: consolidate Sean's dotfiles usage tooling INTO
  ccpool (use it; not ship it outward).

## The journey

1. **Idea:** package dotfiles usage tooling for sharing.
2. **Governor product** explored: cap Claude Code fan-out spend against the weekly pool.
3. **Deflation cascade** (each verified, each killed — see graveyard).
4. **Fable's reframe:** *degrade, don't block* — steer model/effort by pace instead of gating.
   Best idea of the session; its proposed mechanism (hook `updatedInput` rewriting model) was
   empirically **false**, but the env-var lever works.
5. **Pivot:** on a subscription, cap on the account-global `rate_limits` % (Anthropic already
   aggregates it across the fan-out) — no ledger/estimate needed; `$` becomes a *readout*.
6. **Built ccpool** and started consolidating the existing tooling into it.

## The graveyard — tried & rejected, with reasons

- **"No one's in this space"** → FALSE. ccum (Claude-Code-Usage-Monitor, ~8.4k★, meter, no
  enforcement) + claude-ration (npm governor).
- **Meter (surface weekly %)** → redundant on arrival vs ccum.
- **Reset-correctness** ("model the real reset cadence") → the ~72h "reset" people observed was
  a **June-2026 accounting BUG** (Anthropic fixed + mass-reset Jun 19; @ClaudeDevs). Intended
  design is a **rolling 7-day window from first prompt** (Claude Code issue #51222, closed
  not-planned). Mechanics **churn ~monthly**. Lesson: *trust the number reported now; never
  model/reverse-engineer the cadence.*
- **Launch-timed pool bumps** → first killed as n=1/mundane, then evidence showed announced
  **mass resets** (May 15, Jun 19, Jul 9) clustered around competitor launches (Jul 9 = GPT-5.6)
  — the pattern is REAL but **unbuildable** (rare, discretionary, self-erasing if publicized).
- **$-reservation ledger across fan-out** (the "novel wedge") → a correct **OCS / credit-control
  primitive** (RFC 8506), PROVEN under real OS-level concurrency + a real `say-hi` fan-out (naive
  overshoots 162%; two-phase reservation bounds it, never kills in-flight). **But killed for the
  subscription audience:** (a) **no real-time $** — `cost.total_cost_usd` is 0 on ~13/19 sessions
  (Max isn't per-token-billed), so "cap actual $" governs a phantom; (b) TTL auto-refund frees
  *still-running* long turns → cap defeated; (c) multi-turn reserve overwrites accumulated actual;
  (d) incremental PostToolUse gating is blind to no-tool token burn (the exact flaw gen-1 died
  for); (e) estimate fat-tail (per-step cost max = 16× median). LiteLLM already ships this
  primitive (buggy leak, #27639); it's **API-user turf**. And the **multi-card problem**: the
  pool is drawn by app + cloud + other machines a *local* ledger can't see — so % (which Anthropic
  aggregates) is the right signal, not a local $ ledger.
- **Dynamic hook downshift** (rewrite subagent model via `updatedInput`) → empirically FALSE:
  the Agent/Task tool_input is `{description, prompt, subagent_type, run_in_background}` — **no
  `model` field**; hooks docs confirm only `SessionStart` *receives* a model field and no hook
  can *set* model/effort. The working lever is the **`CLAUDE_CODE_SUBAGENT_MODEL` /
  `CLAUDE_CODE_EFFORT_LEVEL` env vars** (VERIFIED: a subagent actually ran haiku).
- **Detect the work rhythm from usage (auto-profile)** → PoC'd against 407k real transcript
  events (2026-07-10), then banked. Findings: circular resultant length **R = 0.22–0.33**
  (all-time / last-30d) — Sean is near-**even/24-7** because the overnight loops smear the
  circadian signal, so `even` is empirically the right default. The killer insight: **detection
  is self-obviating** — pace-value and detectability both scale with R, so you only *need* a
  schedule profile when the rhythm is strong (high R = easy to read), and when it's weak (low R,
  Sean) `even` is already correct. No "valuable-and-hard" middle → no BOCPD/ADWIN change-point
  machinery. Timezone travel (UTC-stamped jsonl) further poisons naive hour-of-day detection,
  but that's a red herring given the above. **Shipped** as the opt-in `ccpool rhythm`
  *diagnostic* (`rhythm.rb`): compute R on a recency window, R-gate the output (high → suggest
  concrete `WAKE_HOURS`/`WORK_DAYS`; low → stick with `even`), SUGGEST only, never auto-apply.

## Research findings (durable)

- **Weekly limit** = rolling 7-day from first prompt; Anthropic keeps it opaque (deliberate).
- **Hooks & model/effort:** no hook can set model/effort/budget; `updatedInput` is tool-args-only;
  `$CLAUDE_EFFORT` is readable not settable. Subagents default to inherit parent model + high
  effort. Env vars are the only steering lever (and launch-time only).
- **Detection:** per-turn model = `message.model` in `~/.claude/projects/**/*.jsonl` (subagents in
  `<session>/subagents/agent-*.jsonl`; skip `<synthetic>`); **effort is NOT logged per turn** —
  proxy from output-token volume + tool-call count (normalize per model; `ultrathink` inflates
  invisibly). Background Haiku (summaries, titles, `/usage`) is out-of-band ~$0.04/session, absent
  from transcripts. No native per-task model router (`opusplan` is the only one).
- **ccusage:** its `blocks --json`/`daily --breakdown` field surface is the *most stable part* of
  the tool (survived 20 "epoch-semver" majors + a Rust rewrite). Risk is `@latest` inheriting a
  Node-version bump (v19 → Node ≥22.11) or rewrite regression → **pin `@20`**; guard fields as
  optional; `models` can be prefixed (`"[pi] gpt-5.4"`). Never hand-roll pricing.
- **$/1% calibration:** ~$26–33 of ccusage API-equivalent per 1% of the Max-20x weekly pool
  (pool ≈ $2.6–3k/wk); supersedes an older manual ~$45 estimate.
- **Prior art / correctness:** our design = OCS/credit-control (RFC 8506), NOT a rate-limiter
  (those refill; a weekly cap must stay capped). Real-world failure mode is reservation **leak**
  (LiteLLM #27639), needs lease-renewal + fencing tokens (Kleppmann) — but that's over-engineering
  for a best-effort personal guardrail.

## Competitor & market analysis

- **ccum** (8.4k★): mature meter, no enforcement, 3-tier confidence hierarchy, has $ AND % but
  never fuses them into "$ of pool left". Steal: staleness/leak-bug (#52326) robustness.
- **claude-ration** (npm v0.1.x): the only governor — but **fails open unattended** (fetches
  OAuth only in the statusline, hooks read stale cache), single global counter, no $, no per-hour.
  Steal: one-command install + MCP self-service.
- **ccusage**: cost only; can't see `rate_limits`. We delegate to it.
- **LiteLLM**: ships reserve/reconcile budget (buggy); API/team turf.
- **Anthropic direction** (their billing + docs): managed *cloud* agents (extra billing), soft
  `task_budget`/effort guidance (calls `max_tokens` a "blunt instrument"), discretionary pool
  resets. Points AWAY from a hard client-side governor — but they will NOT build weekly-pacing
  for flat-fee *local* users (misaligned incentive), so that gap is ours/the ecosystem's.
- **Native `/status` Usage + Stats tabs** (shipped ~2026-07; the biggest competitive event so
  far). Usage tab now shows weekly % (all-models AND a separate Fable bucket), 5h %, per-model $,
  AND diagnostic tips — *"81% subagent-heavy → configure a cheaper model", ">150k context",
  "8h+ loops", "4+ parallel share one limit"*. That **ate the meter + `review`'s diagnosis**
  (its first-of-kind claim). Stats tab shows a day×hour activity heatmap (the very rhythm data
  "detect-from-usage" wanted — but display-only, wired into nothing). **What survives, and it's
  exactly Sean's persona:** ccpool *projects* (working-hours runway, throttle-before-reset),
  *enforces* (`run` auto-downshift — native only *advises*), *decides* (`check` verdict for
  loops), and *delivers proactively* (mid-turn `warn`, always-on statusline — native `/status`
  is a manual pull an autonomous loop can't open). Net: as a *product* native torched most of
  the surface; as a *personal tool that drives your own loops within the pool* (the DECISIONS
  call: "use it, not ship it"), the enforce+project+decide core is still uniquely ccpool's.
  - **CORRECTION (2026-07-10 due-diligence):** the "ate `review`'s diagnosis" claim above was
    **overstated**. A fresh pass over the current Claude Code docs/changelog (`/usage`, `/stats`,
    analytics) finds native has **no** expensive-model-on-trivial-work signal (no "Opus did
    trivial work → downshift"; the diagnostic *tips* are about subagent-share / context / loop
    length, a different question than `review`'s per-turn provisioning check), and the Stats
    activity view is a **day-of-week grid, not the day×hour heatmap** claimed — so it doesn't even
    overlap `rhythm`'s hour-of-day analysis. Net: native absorbed **visibility** (richer
    dashboards) but not the **diagnosis/decision** layer. `review` and `rhythm` are **less
    subsumed than feared** — which *strengthens* the lean-KEEP, it doesn't just permit it.
    (Source: `claude-code-guide` research pass; treat as directional, re-verify against `/status`
    before any cut.)
- **Market (IICP/persona):** beachhead = the autonomous-agent operator (Sean; known best,
  positive product-strength). But conversion play is NOT out-metering ccum (sticky) — it's owning
  the unserved "get the most out of your fixed pool" category. As a *free tool* the bar is lower;
  the maintenance-treadmill fear is also lower than assumed (ccusage field surface stable + pinned;
  we read Anthropic's live number instead of modeling it).

## Design decisions

- **Governance signal = account-global `rate_limits` %** (not a local $ ledger — multi-card).
  `$` = readout (API-equivalent, self-calibrated, drifts w/ promo/tier).
- **Downshift = env vars at launch** (`ccpool run`), not hooks (can't) — coarse but right grain
  for an unattended fan-out; respects a user-set model; coasts near reset; 5h-hot triggers it too.
- **Freshness = 2min** (statusline re-renders multi-x/min when active; past it → estimate tier).
  **3-tier:** fresh / estimated-via-accrued-cost / stale-with-warning. Fail OPEN everywhere.
- **Pruning opt-in** (deleting files is never silent-by-default); `ccpool prune` + status nudge.
- **Statusline bar uses 24-bit truecolour** (16-colour cyan got remapped to pink by Ghostty).
- **ccusage pinned @20**, fail-loud schema-probe.

## Methodology (the real dividend)

Verification-as-the-work. ~40 rounds; **every exciting premise deflated under checking** — 3
adversarial "hater" passes + a Fable cold-start review + empirical spawn-and-look tests. The
optimism was always steepest where the evidence was thinnest (Fable "verified" a model-rewrite
mechanism that a 5-second test disproved). The value was the well-defended NO and a tightly
scoped build, not a big misdirected product.

## Current state & roadmap

**Update 2026-07-10** — the consolidation is DONE and LIVE. Every roadmap TODO shipped, each
review+adversarially-reviewed, ~85 hermetic tests. New modules: `warn.rb` (`ccpool warn`, now
wired at Sean's UserPromptSubmit/PostToolUse hooks), `check.rb` (`ccpool check`, the checking-usage
skill now points here via a symlink to ccpool's canonical `skills/checking-usage/SKILL.md`),
`profile.rb` (schedule-aware pace), `runway.rb` (working-hours-to-reset). Also: statusline parity
(anomaly log + per-session/ses-keyed history dedup, which surfaced + fixed a latent $/1%
calibration inflation); history write-throttle + opt-in `prune --history`; `CCPOOL_DOWNSHIFT`
enforce/advise/off toggle; context-compaction warn now keys off ABSOLUTE token headroom (a 1M
window isn't nagged at 85%). Old files retired (usage-pace-warn.rb, checking-usage/usage.rb+burn.rb,
statusline-command.rb) across ~/.claude + dotfiles.

Key design threads this session (see graveyard/competitor above): pace reorganized around two
orthogonal knobs (`WORK_DAYS × WAKE_HOURS`, 24/7 default) with named profiles as sugar; the
working-hours runway (SRE error-budget burn-rate shape) as the actionable reframe of "% left";
detect-from-usage PoC'd, banked, then shipped as the opt-in `ccpool rhythm` diagnostic. Possible
later: Rust reimpl if the warn hook's ~67ms Ruby startup ever bites a tight loop.

**Update 2026-07-10 (part 2) — the sharing pass.** Reframe: ccpool is meant to be *shared*, so
onboarding earns its keep. Shipped + decided:

- **`ccpool init` (BUILT).** The onboarding vehicle: dry-run diff by default, `--apply` merges the
  `statusLine` + `warn` hooks into `~/.claude/settings.json` after a timestamped backup.
  Idempotent, never-clobber, symlink-aware (edits the real dotfiles target, keeps the link),
  aborts loudly on a dangling symlink or unparseable settings rather than corrupting them. Zero
  required config — the happy path is `ccpool init --apply` and nothing else. (`init.rb`, tests in
  `test_ccpool.rb`.) A review + silent-failure pass caught a dangling-symlink clobber, now fixed +
  tested.
- **Rust/Go reimpl (SCOPED, not built).** `docs/RUST-REIMPL.md`. Measured the hot path: `warn`
  ~45 ms / `statusline` ~63 ms cold-process, of which **~32 ms is bare Ruby-interpreter startup**
  paid on every fire even when `warn` stays silent. Real usage = 6.1 tool-calls/turn → ~7 `warn`
  fires/turn → ~0.3 s/turn, **<1% of turn wall-clock**. Verdict: latency alone doesn't clear the
  bar; the real driver is **distribution** (one static binary, no Ruby dep) for the sharing goal —
  **defer the build**, recommend **Go** when it happens, port only the hot path first.
- **`--json` (DEFERRED).** No consumer parses ccpool today. Revisit when something actually does
  (e.g. an autonomous loop consuming `ccpool check` structured output instead of scraping text).
  Building it now is speculative surface. See `docs/CONFIG-AUDIT.md` for the env-var sweep.
- **Config audit (`docs/CONFIG-AUDIT.md`).** ~45 `CCPOOL_*`/`USAGE_*` vars inventoried against the
  "delight via sensible defaults, not config-everything" principle. Finding: a fresh user needs
  **ZERO** of them; ~15 are documented user choices that earn their keep; the ~30 undocumented
  ones are cheap `|| default` threshold escape-hatches, invisible on the happy path. The sprawl is
  low-cost and doesn't burden onboarding — keep defaults, don't grow the documented surface.
  **Followed through (owner sign-off):** demoted the 10 purest never-user-tuned internals
  (`USAGE_BURN_*`, `USAGE_SES_*`, `USAGE_CACHE_*_SECS`, `CCPOOL_BLOCKS_TTL`) to plain constants;
  kept the contestable-judgment knobs (`CHECK_*`/`WARN_*`/`RUNWAY_*`) as overrides.

**Update 2026-07-10 (part 3) — the composition pass.** `init` surfaced the real question: a shared
user who already runs a statusline (starship/powerline/ccstatusline) shouldn't have to abandon it
to adopt ccpool. Decided it's a **must-have for release** and built it, adversarially PoC-gated.

- **Positioning: ccpool is a specialized *pool gauge*, not a general statusline.** A cross-tool
  survey (8+ tools) confirmed everything a ccpool-only user would *miss* — model name, git, dir,
  cost/token breakdowns, themes, powerline — is **DELEGATE** (a host already does it well); and
  ccpool's **$-value-of-remaining-pool + pace is unique** (no surveyed tool renders it). So the gap
  closes by **composing**, not by growing features. Don't try to be a general statusline.
- **The stdin principle (learned the hard way).** Claude Code allows ONE `statusLine` command, and
  stdin is single-consumer, so two payload-hungry statuslines can't both read it. First-pass
  verification against ccstatusline's **README** said its custom-command widgets get only
  `terminal_width` → wrong conclusion ("must build a wrap-and-tee"). Verifying against **source +
  an empirical end-to-end capture (v2.2.22)** proved the opposite: ccstatusline forwards the
  **full payload incl. `rate_limits`** to widgets. **Lesson: verify against source/empirics, not
  docs — the README under-documented the contract and nearly cost a wrapper we didn't need.**
- **BUILT: `ccpool statusline --embed`** — a compact `pool 45% $1.4k +2↑` segment (weekly % ·
  $-of-pool · pace), for embedding as a ccstatusline custom-command widget. `init` detects a
  ccstatusline host and prints the widget recipe instead of clobbering it (won't replace even with
  `--replace-statusline` — composing is strictly better). ccpool stays downstream + tiny; the host
  owns layout/model/git.
- **The widget-$ fix.** The $ moat was only computed by on-demand commands, so a widget-only user
  would see it blank. The statusline path now fires a **throttled, detached, fail-open calibration
  warm-up** so the $ self-populates however ccpool is invoked. Two silent-failure gaps found in
  review + fixed: the detached child's ccusage schema-drift signal now hits the persistent log (was
  `/dev/null`); a corrupt calib cache self-heals (Hash-guard) instead of crashing the warm-up.
- **Adversarial scorecard (cut under PoC — the methodology holding):** *rhythm-weighted projection*
  — CUT (real R=0.327, weak → identical to linear; already handled by `Profile`). *Daemon
  fast-render* — DEFER (render is spawn-free, ~47 ms, <1% of turn; and it'd re-solve what the
  deferred Go port does). *Dated exhaustion landmark, API-health indicator* — DEFER (polish /
  network-fetch nice-to-haves). *Wrap-and-tee (ccpool wraps a host)* — demoted to a fallback only
  for non-forwarding hosts (powerline `&&`-chaining, CCometixLine), since ccstatusline (dominant)
  forwards the payload and B works today.
- **Upstream contribution (open):** ccstatusline's README materially under-documents that custom
  commands receive the full payload — a trivial docs PR would help every widget author. Not done.

## Roadmap → v1 (2026-07-10)

The Ruby tool is feature-complete for its lane (init, composition, downshift, verdict, warn,
statusline, review, rhythm) and well-tested (~160 hermetic cases). **Decision: freeze Ruby scope —
no new features pre-migration** (each is paid for twice) — and stay in lane (everything outside
"get the most out of a fixed pool" the competitor survey put in DELEGATE). **v1 ships in Go.** The
migration is its own focused effort, executed from a clean session per the playbook:

- `docs/GO-MIGRATION.md` — the phased execution plan + handover (Phase 0 pipeline → 1 statusline →
  2 warn → 3 on-demand → v1 binary; the on-disk contract to preserve; conformance-via-Ruby-fixtures).
- `docs/standards/go.md` — Go idioms + the GoReleaser/Homebrew release path.
- `docs/RUST-REIMPL.md` — the measurement + why-Go decision.

Immediate next actions before the port: add a git remote + push (activates CI + the issue/PR
templates, which are dormant with no remote), and grab a real in-Claude-Code statusline screenshot
for the README. Post-v1: revisit deferred items (per-model weekly buckets if the payload carries
them, `--json` if a consumer appears) from the stable Go base.

## Go migration — execution decisions (2026-07-10)

Locked at the top of the migration session; see `docs/GO-MIGRATION.md` for the phased plan.

- **Module `github.com/SeanLF/ccpool`; tap `SeanLF/homebrew-tap`; targets darwin+linux × arm64+amd64.**
  Go 1.26.5, zero shipped deps; dev tools (gofumpt/staticcheck/govulncheck) pinned via go.mod `tool`
  directives. v1 ships ALL commands in Go and retires Ruby (the distribution win per RUST-REIMPL).
- **Homebrew via GoReleaser v2 `homebrew_casks`, NOT `brews`.** `brews` (formula) is deprecated in v2;
  casks are the current path for a binary. Unsigned binary -> a `postflight` strips the Gatekeeper
  quarantine xattr, and `skip_upload: "auto"` keeps prerelease (rc) tags out of the stable tap.
- **Conformance is by EXECUTION, not by the Ruby test assertions.** The Ruby tests check substrings,
  so they don't pin exact bytes. Instead a Ruby "oracle" renders each fixture through the real
  statusline/seed_history/calibration and the Go test diffs against it: byte-identical for the
  rendered line (ANSI included) and the history rows; equal-to-4-decimals for the $/1% math. ccusage
  is mocked deterministically via `CCPOOL_CCUSAGE_CMD` (a fake script) so compute is testable without
  npx. Ruby is wired into the Go CI job so the diff runs in CI too.
- **Ruby's int-vs-float JSON distinction is preserved via `json.Number`.** `JSON.parse` keeps `45`
  Integer vs `45.0` Float, which changes `fmt_dur`/history-row output; Go's default decode flattens
  both to float64. Decoding with `json.Number` keeps the original literal. Ruby's `String#to_i/#to_f`
  and `Float#round` semantics are reimplemented in `internal/rb` (unit-tested against Ruby values).
- **Snapshot files: semantic-identical, byte-identical for realistic payloads — NOT guaranteed
  byte-identical for exotic JSON literals.** Ruby writes `JSON.generate(parsed.merge("captured_at"))`,
  which re-tokenizes numbers (`1e5` -> `100000.0`) and re-escapes strings. Go can't reproduce that
  canonicalization without an ordered re-serializer (maps don't preserve key order), so instead we
  `json.Compact` the raw payload and splice `captured_at` last — byte-identical when the payload uses
  canonical tokens (every real Claude payload does; verified end-to-end against Ruby), differing only
  on inputs that never occur. The snapshot is machine-read (never displayed), so semantic equality is
  the true interop contract; this is a deliberate, bounded deviation from "byte-identical every file."

## Post-v1 architecture — measured options (2026-07-10)

With the Go migration complete, revisited "what makes this a *solid Go binary*, not just a faithful
port." Two framing corrections drove the analysis (owner): **(a) byte-identity to Ruby was a
migration constraint, not a product one** — now that Ruby is deleted, goldens are Go-defined and we
can re-baseline freely; **(b) binary size and zero-dep are NOT sacred** — 1 MB vs 5 MB is noise to a
`brew`/curl install, and runtime deps are acceptable. So decisions collapse to engineering merit:
does a change fix a real problem, or is it polish? Two spikes measured the load-bearing unknowns
(isolated throwaway modules; Go 1.26.5, M4 Pro; `CGO_ENABLED=0 -trimpath -s -w`).

**Binary-size spike (delta vs 1.64 MB baseline):** termenv +0.41 MB (6 mods), lipgloss +1.30 (16),
cobra +0.90 (7), cobra+fang +2.43 (**41 mods** — fang's supply-chain surface is the real cost, not
bytes), kong +1.93 (4), urfave/cli/v3 +1.59 (5), **modernc.org/sqlite +4.72 (25)**. All build
static/no-cgo and cross-compile to linux/arm64; modernc/sqlite confirmed genuinely pure-Go.

**SQLite hot-path latency spike (cold fork+exec, n=50):** sqlite (WAL, busy_timeout) **6.6 ms
median** vs json-files+flock **3.3 ms** vs bare-exec baseline 2.5 ms — i.e. ~+3.3 ms real DB work
per invocation. Both storage models pass an 8-way concurrent-process integrity test cleanly (WAL
absorbs contention; flock+tmp-rename has no torn appends). So SQLite does **not** fix a concurrency
bug we hit today; the current file+flock approach is correct.

**Verdicts (size no longer a counter-argument):**

- **Terminal rendering → `lipgloss` + `termenv`: DO.** Fixes a *real latent bug*: the statusline
  hardcodes 24-bit truecolor with no downgrade and gates only on `NO_COLOR`+`TERM=dumb`, so it
  renders wrong on 256/16-colour terminals and misses `COLORTERM`/CI detection. termenv is the
  colour-profile floor; lipgloss for styled layout. Highest leverage-per-risk. Re-baseline the
  statusline goldens to lipgloss output.
- **Storage → embedded SQLite (`modernc.org/sqlite`, pure-Go): DO, deliberately.** Verdict *flipped*
  once size stopped counting. It doesn't fix a live bug, but it *dissolves a whole category of
  bespoke, subtle code*: the 64 KB-tail dedup on concurrent history append, the glob-and-parse-every-
  snapshot reconcile (O(sessions) file reads ~7x/turn), the write-then-truncate prune, and — the
  big one — the interleaved-multi-session-log reconstruction (`Burn.envelope`) that exists *only*
  because history is an append log; in SQL it's a query. Cost is +3.3 ms/invocation (imperceptible)
  and a rewrite of a working subsystem — but the golden suite pins the *output*, so the storage swap
  is verifiable against the current behaviour. Needs a one-time importer for the accumulated history
  (snapshots/caches can start empty; they self-refresh). Use `sqlc` for typed SQL, not an ORM. This
  is the biggest v2 rock.
- **CLI → `cobra`: DO; `fang`: optional/defer.** cobra (+0.90 MB, 7 mods) buys shell completions,
  consistent `--help`, man pages — real adoption UX for a tool meant to be shared, for a small dep
  surface. fang (charm-pretty help) costs 41 transitive modules for cosmetics — skip until wanted.
  (kong is the minimal-surface alternative at 4 mods if we don't want cobra's tree.)
- **Testing → `pgregory.net/rapid` (property tests on pace/burn/runway invariants) + stdlib fuzzing
  (JSON/transcript/number parsers — the real threat model) + `testscript` (CLI e2e): DO.** Test-only,
  zero binary cost; the durable strategy vs fixture-matching. Keep goldens as regression.
- **`log/slog` for diagnostics; drop the `rb.To[IF]` shims for *config* parsing (clean strconv +
  validation), keeping `internal/rb` only for the on-disk `json.Number` data contract: DO.** Free
  cleanups.
- **ccusage: KEEP (do NOT reimplement cost natively).** Owning a churning pricing table is the
  danger the "delegate every dollar" invariant exists to avoid (owner call, 2026-07-10). Reconfirmed.
- **Not worth it:** a config framework (fights the zero-config-defaults product invariant), an ORM
  over SQLite, `x/text` for comma-grouping.

**Sequencing:** (1) lipgloss/termenv + slog + rb-shim cleanup + the Go-native test stack — low-risk,
land first, re-baseline goldens. (2) cobra. (3) the SQLite storage rework behind a one-time history
importer, verified against the golden suite. None of this is required for a shippable v1 — the
current Go binary is complete — it's the "built like it belongs in Go" pass.
