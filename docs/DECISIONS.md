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

Built, tested (30 hermetic), committed, and Sean's LIVE statusLine. Files: `pool.rb` (reconcile
engine), `calibration.rb`, `analyzer.rb`, `burn.rb`, `statusline.rb`, `ccpool.rb`, `bin/ccpool`,
`test_ccpool.rb`, `README.md`.

Consolidation (dotfiles tooling → ccpool): DONE = reconcile engine, burn projection (`status`),
rich coloured statusline. TODO = port `usage-pace-warn.rb` → `ccpool warn` (still wired at Sean's
UserPromptSubmit/PostToolUse hooks); `ccpool check` verdict (from checking-usage skill); statusline
parity (anomaly log to `statusline.log`, per-session history dedup); then retire the old files +
point settings/hooks at ccpool. Possible later: Rust reimplementation once the Ruby proves out.
