# ccpool

Get the most out of your fixed Claude subscription pool. Three things no existing tool
does, in one CLI:

- **`ccpool status`** — fuses the account-global `rate_limits` % (which ccusage can't see)
  with a ccusage-calibrated `$/1%` into a **dollar value for your weekly pool** + a pace
  verdict: *"9% used · ~$2,329 left of ~$2,560 (API-equiv) · resets Wed 21:00 · 20pts under
  pace, burn freely."*
- **`ccpool run -- <cmd>`** — runs `<cmd>`, **downshifting subagent model/effort**
  (`opus/high` → `haiku/low`) when you're burning ahead of pace, so an unattended `/loop` or
  fan-out conserves the pool. Verified: sets `CLAUDE_CODE_SUBAGENT_MODEL`/`_EFFORT_LEVEL`,
  which actually take effect on spawned subagents.
- **`ccpool review [days]`** — retrospective: **did you use the right model for the work?**
  Flags expensive-model turns that did trivial work (candidates to downshift). First-of-kind.
- **`ccpool check`** — time + budget + a keep-going/stop **verdict** for long or autonomous
  loops (`KEEP GOING` / `PACE DOWN` / `SESSION-LIMITED` / `WIND DOWN` / `COAST` / `BURN DOWN`),
  distinguishing a temporary 5h throttle from a real "stop for the week."
- **`ccpool warn`** — a Claude Code hook (wire at `UserPromptSubmit`/`PostToolUse`) that warns
  the agent mid-turn when it's over pace, near the 5h cap, or near context auto-compaction.

It delegates every dollar to `ccusage` (never hand-rolls pricing) and reads the `rate_limits`
% that ccusage structurally can't. Fails **open** on any missing/stale data — it never blocks
Claude Code.

## Install (dogfood)

```sh
chmod +x bin/ccpool
export PATH="$PWD/bin:$PATH"     # or symlink bin/ccpool onto your PATH
ccpool status
```

**Data source.** ccpool reads per-session `~/.claude/usage-cache-*.json` snapshots for the
`rate_limits` %. On a fresh machine those don't exist (vanilla Claude Code doesn't write them),
so wire ccpool as your statusLine to self-populate:

```jsonc
// ~/.claude/settings.json
{ "statusLine": { "type": "command", "command": "ccpool statusline" } }
```

`ccpool statusline` captures `rate_limits` from CC's payload, seeds the history the `$`
calibration needs, and renders a compact line (`pool 9% · $2.3k left · pace -20↓`). If you
*already* run a statusline that writes those snapshots (e.g. a custom one), ccpool just reads
it — no statusLine change needed.

## Usage

```sh
ccpool status                    # full readout
ccpool check                     # keep-going/stop verdict (long / autonomous loops)
ccpool run -- claude -p "..."    # or wrap a fan-out script; downshifts when ahead of pace
ccpool review 7                  # provisioning review, last 7 days
```

## Pace profiles (env)

Pace is `used%` vs how far through the week you *should* be. By default that's the plain
elapsed fraction of the rolling 7-day window — uniform 24/7, which fits a continuous
autonomous-loop operator. But the window's start is arbitrary (Anthropic-controlled) and few
humans burn evenly, so a Mon–Fri worker would look "ahead of pace" every Friday for no real
reason. Describe your rhythm with two orthogonal knobs (off either → the `CCPOOL_PACE_FLOOR`
residual, not zero, so one late night isn't read as infinitely ahead of pace):

| knob | default | meaning |
|---|---|---|
| `CCPOOL_WORK_DAYS` | `0-6` (all) | which days you're active (wday `0`=Sun … `6`=Sat) |
| `CCPOOL_WAKE_HOURS` | `0-24` (no sleep) | your waking window on those days |
| `CCPOOL_PACE_FLOOR` | `0.15` | weight for off-days / sleeping hours |

Examples: **24/7 loop operator** → *defaults*. **9–5 human** → `WORK_DAYS=1-5 WAKE_HOURS=9-17`.
**7-day indie who sleeps** → `WAKE_HOURS=8-24`. **4-day week** → `WORK_DAYS=1-4 WAKE_HOURS=8-24`.

`CCPOOL_PACE_PROFILE` is optional shorthand that just presets those knobs: `even` (default,
all/24h), `weekdays` (`1-5`/24h), `workhours` (`1-5`/`9-17`), or `custom` for graded
`CCPOOL_PACE_WEIGHTS` (7, Sun–Sat) × `CCPOOL_PACE_HOUR_WEIGHTS` (24). An explicit knob overrides
the preset. One setting steers `status`, `check`, `warn`, `run`'s downshift, and the statusline
bar together — they can't disagree.

## Config (env)

| var | default | meaning |
|---|---|---|
| `CCPOOL_PACE_MARGIN` | `3` | pts over pace before `run` downshifts / `warn` nags |
| `CCPOOL_DOWNSHIFT_MODEL` / `_EFFORT` | `haiku` / `low` | what to downshift subagents to |
| `CCPOOL_CALIB_TTL` | `21600` | seconds to cache the `$/1%` calibration |
| `CCPOOL_CCUSAGE_CMD` | `npx -y ccusage@20` | how to invoke ccusage (pinned major — see calibration.rb) |
| `USAGE_CACHE`, `CCPOOL_HISTORY`, `CCPOOL_CALIB_CACHE` | `~/.claude/...` | data paths (test isolation) |

## Honest limitations

- **Downshift is launch-time** (per `ccpool run` invocation), not continuous mid-run — Claude
  Code hooks cannot set model/effort, so the wrapper is the enforcement point. That's the right
  grain for an unattended fan-out; it won't slow a single expensive main-loop turn.
- **`$` values are API-equivalent**, not billed money (you pay a flat subscription). They're the
  right signal for "burn it or bank it," not for accounting. Self-calibrated from *your* usage;
  drifts with model mix / promos (recomputed every `CCPOOL_CALIB_TTL`).
- **Single data source.** Reads the statusline snapshot; no OAuth fallback. Stamps data age when
  stale. Robust to the known leak bug (#52326) and clamps garbage, but it's one source, not
  ccum's three-tier hierarchy (yet).
- **`review` proxies effort** from output-token volume + tool-call count (effort isn't logged
  per-turn); `ultrathink`/thinking inflate output invisibly. Treat it as a hint, not a verdict.

## Tests

```sh
ruby test_ccpool.rb   # 56 hermetic cases, no real ~/.claude access
```
