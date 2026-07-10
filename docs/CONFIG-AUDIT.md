# ccpool — config (env-var) audit (2026-07-10)

Design principle under test: **delight via sensible defaults, not config-everything.** A fresh
user must need ZERO env vars. This is the inventory + a keep / confirm / candidate-to-drop verdict
per knob. Paired with `ccpool init` making the happy path zero-config.

## Headline

**A fresh user needs zero env vars.** `ccpool init --apply` wires everything; every knob below has
a working default. ~45 vars exist, but they split cleanly:

- **8 path/data vars** — invisible plumbing (test isolation + data location). Not "config."
- **~15 documented user choices** — genuine user-shape diversity (pace rhythm, downshift policy,
  clock, colour). These earn their keep; they're *why* ccpool fits more than one operator.
- **~12 undocumented threshold knobs** — each a one-line `ENV["X"] || "default"` escape hatch on
  an internal constant. A user never sees them. Low-cost; not sprawl that hurts onboarding.
  (Down from ~22: 10 pure-internal fit/cache constants were demoted to plain constants on
  2026-07-10 with owner sign-off — see "Demoted" below.)

The red-team fear ("~30 env vars overwhelm a new user") doesn't land: the undocumented thresholds
aren't in the README, so a new user never meets them, and the documented ones are real choices, not
noise. **Recommendation: keep the defaults, and DON'T promote the threshold knobs into the
documented surface.** Optionally demote a handful of never-tuned internals to plain constants to
shrink code surface — but that's cosmetic and needs sign-off, not urgent.

## Bucket 1 — path / data vars (keep; invisible plumbing)

Load-bearing for hermetic tests (the suite redirects these to a temp dir) and for relocating data.
Never a user-facing "setting."

| var | default | role |
|---|---|---|
| `USAGE_CACHE` | `~/.claude/usage-cache.json` | snapshot path base (glob `-*.json`) |
| `CCPOOL_HISTORY` | `~/.claude/rate-limit-history.jsonl` | wk%/5h% history log |
| `CCPOOL_CALIB_CACHE` | `~/.claude/ccpool-calibration.json` | cached `$/1%` |
| `CCPOOL_BLOCKS_CACHE` | `~/.claude/ccpool-blocks-cache.json` | cached ccusage `blocks` |
| `CCPOOL_PROJECTS` | `~/.claude/projects` | transcript root (review/rhythm) |
| `CCPOOL_SETTINGS` | `~/.claude/settings.json` | `init` target |
| `CCPOOL_STATUSLINE_LOG` | `~/.claude/statusline.log` | anomaly log |
| `USAGE_TIER` | `max_20x` | pool tier tag stamped into history |

**Verdict: KEEP all.** Good defaults confirmed. `USAGE_TIER` is the only one a non-Max-20x user
might set, but it's a label, not a behaviour — fine as an env default.

## Bucket 2 — documented user choices (keep; earn their keep)

These are in the README because they encode real differences between operators. This is the
"diversity of user shape justifies the knob" set.

| var(s) | default | why it's a real choice |
|---|---|---|
| `CCPOOL_PACE_PROFILE` + `WORK_DAYS` + `WAKE_HOURS` + `PACE_FLOOR` (+ `PACE_WEIGHTS`, `PACE_HOUR_WEIGHTS`) | `even` / 24-7 | a 9-5 human vs a 24/7 loop operator genuinely pace differently; default fits the loop operator |
| `CCPOOL_DOWNSHIFT` (+ `_MODEL`, `_EFFORT`) | `auto` / `haiku` / `low` | enforce vs advise vs off is a policy call; the model/effort target is a taste |
| `CCPOOL_HISTORY_KEEP_DAYS` | `30` | real tradeoff: some want the full ~20 MB/mo raw log (`0` = keep forever) |
| `CCPOOL_HISTORY_MIN_INTERVAL` | `60` | curb file growth vs granularity |
| `CCPOOL_CCUSAGE_CMD` | `npx -y ccusage@20` | churn-defense escape hatch (pinned major; see calibration.rb) |
| `CCPOOL_CALIB_TTL` | `21600` | how often to re-spend an npx call on `$` recompute |
| `CCPOOL_CLOCK` | `24` | 12h/24h/auto — a locale preference |
| `CCPOOL_RHYTHM_WINDOW`, `CCPOOL_RHYTHM_R` | `30`, `0.5` | documented rhythm-diagnostic tuning |
| `NO_COLOR` / `TERM` | — | the standard no-color.org contract, not ours to drop |

**Verdict: KEEP all. Good defaults confirmed.** The pace family is the strongest justification for
config existing at all — it's the feature, not the clutter.

## Bucket 3 — undocumented threshold knobs (keep as escape hatches; do NOT document)

Each is an override on an internal constant: `ENV["X"] || "<calibrated default>"`. They exist so a
threshold can be tuned or a test can pin it, without a code edit. A fresh user never encounters
them (not in the README). Cost is ~1 line each; benefit is testability + a per-deployment escape
hatch. The defaults are all calibrated and sane.

- **Coast / staleness / caps:** `CCPOOL_5H_CAP` (85), `CCPOOL_COAST_SECS` (43200),
  `CCPOOL_STALE_SECS` (120), `CCPOOL_CACHE_KEEP_SECS` (3600), `CCPOOL_HISTORY_WARN_MB` (20).
- **check verdicts:** `CCPOOL_CHECK_SES_FULL` (92), `CCPOOL_CHECK_SES_SOON_SECS` (900),
  `CCPOOL_CHECK_WEEKLY_LOW` (90), `CCPOOL_CHECK_STALE_SECS` (900), `CCPOOL_CHECK_IDLE_WARN_H` (24),
  `CCPOOL_CHECK_BURNDOWN_FORFEIT` (15). *(These encode judgment calls a power user might contest —
  the override is the right pressure valve; keep undocumented.)*
- **warn thresholds:** `CCPOOL_WARN_STALE_SECS` (3600), `CCPOOL_WARN_THROTTLE_SECS` (1800),
  `CCPOOL_WARN_CTX_PCT` (85), `CCPOOL_WARN_CTX_LEFT` (30000), `CCPOOL_WARN_CTX_THROTTLE_SECS` (600),
  `CCPOOL_WARN_5H_PCT` (85).
- **runway band:** `CCPOOL_RUNWAY_FAST` (1.5), `CCPOOL_RUNWAY_SLOW` (0.7),
  `CCPOOL_RUNWAY_MIN_DENSITY` (0.5).
- **review / rhythm internals:** `CCPOOL_LOW_OUTPUT` (500), `CCPOOL_RHYTHM_PEAK` (0.25).
- **misc:** `CCPOOL_BAR_COLOR` (truecolour cyan), `CCPOOL_PRUNE` (opt-in delete flag),
  `CCPOOL_PACE_MARGIN` (3 — shared by status/check/warn/run; borderline documentable).

**Verdict: KEEP as-is, leave undocumented.** They don't burden onboarding (invisible), they're
cheap, and they make the thresholds testable. Do NOT add them to the README env table — that's how
"sensible defaults" would decay into "config-everything." (Note: the `CCPOOL_CHECK_*`, `CCPOOL_WARN_*`
and `CCPOOL_RUNWAY_*` families are kept as env overrides deliberately — they encode judgment calls
a power user might legitimately contest, so the pressure valve earns its line.)

## Demoted to constants (2026-07-10, owner sign-off)

The purest never-user-tuned internals — pure numeric fit/cache parameters with no plausible
per-user tuning story and no test pinning them — were converted from `ENV["X"] || "default"` to
plain constants, dropping 10 lines of env surface while keeping the explanatory comments (the
valuable part). Removed knobs:

- **burn-fit params** (`burn.rb`): `USAGE_BURN_DROP_RESET`, `USAGE_BURN_MIN_SPAN_H`,
  `USAGE_BURN_MIN_DELTA`, `USAGE_SES_WINDOW_SECS`, `USAGE_SES_MIN_SPAN_H`, `USAGE_SES_MIN_DELTA`.
- **statusline staleness tiers** (`statusline.rb`): `USAGE_CACHE_TTL_SECS`, `USAGE_CACHE_WARN_SECS`,
  `USAGE_CACHE_CRIT_SECS`.
- **ccusage blocks cache TTL** (`calibration.rb`): `CCPOOL_BLOCKS_TTL`.

These were the only ones where the escape hatch bought nothing (no calibration story, no test use,
no user who'd ever touch them). Everything else in Bucket 3 kept its override because it either
encodes a contestable judgment call (`CHECK_*`/`WARN_*`) or has a real tuning/testing story.

## Bottom line

The config surface is **not** the liability the red-team assumed. Zero vars are required; the
documented ones are genuine user-shape choices; the rest are invisible escape hatches. `ccpool
init` delivers the zero-config happy path the principle demands. **Action: none required** — keep
defaults, hold the documented surface where it is.
