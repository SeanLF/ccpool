---
name: checking-usage
description: Use when you need the current time and how much Claude usage budget is left (session/5h window + weekly pool) - deciding whether to keep going in a long or autonomous loop, or before claiming a budget-bounded task is done. Reports time, session %, weekly %, reset countdowns, pace, working-hours runway, and a keep-going-or-stop verdict.
---

# Checking time + remaining usage budget

Answers "what time is it" and "how much budget is left" (session 5h window + weekly pool, with reset countdowns, a working-hours runway, and a decision) in one command.

## Run it

```bash
ruby /Users/sean/Developer/ccpool/ccpool.rb check
```

No arguments. Read the `VERDICT` line — it's the decision, not just numbers. Exit 0 = report; exit 2 = no/garbled data (don't guess).

## The decision rule (the reason this exists)

The mistake this prevents: **stopping with weekly budget left because the session window looked full.** They're different things.

- **SESSION (5h) near full** → _temporary_. Resets in hours. If the weekly pool has room, this is "pause and resume after the reset," NOT "done."
- **WEEKLY pool near full** → the real stop signal. Land in-flight work and stop for the week.
- **Both have room** → keep going. If asked to spend a budget, _spend it_ — under-pace means push harder.

`pace` shows if you're ahead of or behind your pace (uniform by default; weighted by `CCPOOL_PACE_PROFILE` if you set a work rhythm). The `pace guide` (~N%/day) is an even-burn share to make the weekly pool last — a hint, **not a hard daily cap**. The `runway` line reframes "% left" as WORKING hours before reset: *budget-limited* → you'll throttle early; *calendar-limited* → the week resets first, burn freely.

## Two honest limitations

- **Freshness** — the numbers are only as fresh as the last status-line render (the payload reaches only that process; there's no pollable API). Interactive: seconds old. Pure background job with no TUI: can be stale/absent — `check` says so (`STALE` / "no snapshots") instead of guessing. If stale and it matters, get an interactive window to render once, then re-run.
- **`seven_day` is the ALL-MODELS weekly window.** The separate per-model weekly caps that `/status` shows (Sonnet-only: [issue #27915](https://github.com/anthropics/claude-code/issues/27915); a distinct Fable weekly bucket too) are NOT in this payload. For model-heavy work, treat a healthy weekly % as necessary-but-not-sufficient and check `/status` before a big push.

## Tunables (env, optional)

- `CCPOOL_CHECK_SES_FULL` (default 92) — session % that counts as "almost full"
- `CCPOOL_CHECK_WEEKLY_LOW` (default 90) — weekly % that triggers WIND DOWN
- `CCPOOL_CHECK_STALE_SECS` (default 900) — data age before it's flagged STALE
- `CCPOOL_PACE_PROFILE` / `CCPOOL_WORK_DAYS` / `CCPOOL_WAKE_HOURS` — weight pace by your work rhythm (default: uniform 24/7). See ccpool's README.
