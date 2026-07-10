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
ccpool run -- claude -p "..."    # or wrap a fan-out script; downshifts when ahead of pace
ccpool review 7                  # provisioning review, last 7 days
```

## Config (env)

| var | default | meaning |
|---|---|---|
| `CCPOOL_PACE_MARGIN` | `3` | pts over pace before `run` downshifts |
| `CCPOOL_DOWNSHIFT_MODEL` / `_EFFORT` | `haiku` / `low` | what to downshift subagents to |
| `CCPOOL_CALIB_TTL` | `21600` | seconds to cache the `$/1%` calibration |
| `CCPOOL_CCUSAGE_CMD` | `npx -y ccusage@latest` | how to invoke ccusage |
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
ruby test_ccpool.rb   # 22 hermetic cases, no real ~/.claude access
```
