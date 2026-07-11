# ccpool — config file design (2026-07-10)

Status: design, pre-implementation (under review). Companion to `docs/CONFIG-AUDIT.md` (which
inventories the env surface) and `docs/DECISIONS.md`.

## Goal

Persist a user's durable choices to a config file so they survive without keeping env vars exported,
and so expensive detection runs ONCE (off the hot path) instead of per render. A fresh user still
needs nothing: every setting keeps its default and detection seeds sensible values.

Not zero-config for its own sake; "no *need* to config." The file is where a chosen or detected value
lives; env stays the override/escape hatch.

## Non-goals

- **Not a config framework.** No multi-source precedence matrix beyond `env > file > default`, no
  schema-validation engine, no live reload. (Roadmap "Not doing": config framework.)
- **Thresholds and paths stay env-only.** The CONFIG-AUDIT bucket-3 escape hatches
  (`CCPOOL_CHECK_*`/`WARN_*`/`RUNWAY_*`, etc.) and bucket-1 plumbing paths (`USAGE_CACHE`,
  `CCPOOL_HISTORY`, ...) are NOT in the file. They remain undocumented env overrides (test isolation,
  power-user tuning). Only the ~user-shape choices persist.
- **No `config set` in v1.** Detect + hand-edit covers it; a mutating subcommand is a fast-follow if
  wanted (YAGNI).

## Resolution model

`internal/env` is the single resolution point. For every knob it already owns (all numeric, post-A2)
plus the newly-routed string knobs:

```
value(key) = os.LookupEnv(key)         if set        (top override: tests, one-offs)
           = configFile[key]           if present    (persisted choice)
           = builtin default                          (fallback)
```

Consequence of A2: the numeric knobs already flowing through `env` get file support for free —
`pace.floor` (`env.Float`), `history.keep_days`/`min_interval` (`env.Float`/`Int`). The string/CSV
settings still read `os.Getenv` directly, so they need a new `env.String(key, def)` and light
rerouting at their call sites:

| setting(s) | env key(s) | call site |
|---|---|---|
| pace.profile / work_days / wake_hours / weights / hour_weights | `CCPOOL_PACE_PROFILE` / `WORK_DAYS` / `WAKE_HOURS` / `PACE_WEIGHTS` / `PACE_HOUR_WEIGHTS` | `profile.Load` |
| clock | `CCPOOL_CLOCK` | `clock.Mode` |
| colour | `CCPOOL_COLOR` | `statusline.colorProfile` |
| downshift.mode / model / effort | `CCPOOL_DOWNSHIFT` / `_MODEL` / `_EFFORT` | `run` (`envS`) |
| tier | `USAGE_TIER` (note: not `CCPOOL_`-prefixed) | `history` |

The config layer returns values in their **string form** (as if the env var had been set), so a
config value flows through the exact same parse + validation as an env value — including A2's
non-finite-float rejection and fail-open-to-default. One parsing path, no divergence.

## File

- Location: `~/.claude/ccpool.json` (override `CCPOOL_CONFIG`, resolved fresh per process like the
  other paths in `internal/paths`).
- Format: JSON (stdlib, zero new deps, consistent with every other on-disk artifact ccpool
  reads/writes). No comments; `ccpool config show` + the README carry the explanations.
- Schema (friendly, lightly nested; all fields **pointers** so absent ≠ zero):

```json
{
  "enabled": true,
  "pace":      { "profile": "weekdays", "work_days": "1-5", "wake_hours": "9-17",
                 "floor": 0.15, "weights": [1,1,1,1,1,0.3,0.3], "hour_weights": [] },
  "downshift": { "mode": "auto", "model": "haiku", "effort": "low" },
  "clock":     "24",
  "colour":    "truecolor",
  "tier":      "max_20x",
  "history":   { "keep_days": 30, "min_interval": 60 }
}
```

`pace.weights` / `pace.hour_weights` are JSON arrays (day-of-week × hour graded weights); they only
matter when `pace.profile = custom`. The mapping joins them to the CSV form the `CCPOOL_PACE_WEIGHTS`
parser already expects (array `[1,1,0.3]` → `"1,1,0.3"`), so one parse path still holds.

Presence-aware decode (pointer fields, or a `map[string]json.RawMessage` presence pass) is
**load-bearing**: a missing `enabled` must mean *on*, not the zero-value `false`; a missing number
must fall through to its default, not become `0`.

### Friendly-key ↔ env-key mapping

An explicit table in `internal/config` maps each friendly path to its `CCPOOL_*` key and extracts the
string form from the parsed struct (present only when the pointer is non-nil). ~14 entries, e.g.
`pace.profile → CCPOOL_PACE_PROFILE`, `clock → CCPOOL_CLOCK` (string "12"/"24"/"auto"),
`downshift.mode → CCPOOL_DOWNSHIFT`. This decouples the user-facing file shape from the internal env
names and gives one place to see the full documented surface. `env.String` looks values up by env key
(the reverse direction of the same table).

## Kill-switch

Top-level `enabled` (default true). `warn.Hook` and `statusline.Command` check `config.Enabled()`
first and return a clean no-op when false (empty statusline, no warning) — a quiet install for
holidays/focus without unwiring `init`. Order: `CCPOOL_ENABLED` env (escape hatch) > file `enabled` >
true. A missing OR corrupt config never disables (fail-open must not accidentally silence the tool).

## Commands

Dry-run by default, `--apply` writes — matching the existing `ccpool init` hook contract (and the
"init aborts rather than clobbers" invariant). Nothing touches disk without `--apply`.

- `ccpool init` — wires hooks (unchanged) **and** seeds the config. Dry-run shows BOTH the hook plan
  and the config plan (detected values + the file diff it would write); `--apply` writes both.
  Idempotent / re-runnable: the hook step already no-ops when wired; the config step creates the file
  if absent and otherwise fills only missing keys, **never clobbering a user's edited values**. So a
  user can safely re-run `ccpool init --apply` after an upgrade.
- `ccpool config show` — read-only. Render each in-scope setting: effective value + source (`env` /
  `file` / `default`). Detected values live in the file, so once seeded they read as `file`
  (provenance isn't separately tracked). Needs a provenance-aware resolver: `env` exposes a
  `(value, source)` variant alongside the plain getters. The "why is my pace X?" answer. Fails LOUD
  on a corrupt file.
- `ccpool config init` — the config step on its own. Dry-run shows the detected values + planned
  file; `--apply` writes (fill-missing-only merge, same as `init`); `--apply --force` regenerates
  from scratch (re-detect + overwrite). Fails LOUD.

## Detection (off the hot path, at seed time only)

Detection is why the file exists (persist an expensive result once). It runs only at
`init` / `config init`, never per render. It is a HINT, not a promise — sick days, holidays, and
irregular weeks make any rhythm estimate approximate, so `even` (no-schedule) stays the safe default
and every detected value is trivially overridable in the file.

Only **pace.profile** and **clock** are detected. Everything else is written at its plain default
(`downshift=auto/haiku/low`, `colour=truecolor`, `tier=max_20x`, `history.keep_days=30`, ...) for the
user to change. Colour specifically CANNOT be detected — the hook renders to a non-tty pipe (the A1
finding), so it defaults to truecolor and is a manual choice.

- **pace.profile** — from `rhythm`'s transcript analysis (reuse its suggestion logic; extract a
  callable). Expensive (scans `~/.claude/projects`), hence persisted.
- **clock** — resolve `clock`'s `auto` mode ONCE and persist the concrete `12`/`24`. Real cost win,
  measured: `clock.Mode()` under `auto` shells out to `defaults read` (~8ms) on every
  status/check/rhythm call (not the statusline hot path — clock isn't on it). Each hook/command is a
  fresh short-lived process, so in-process memoization wouldn't survive across calls; only the
  persisted config value caches it across invocations. Persisting removes the subprocess entirely.
- **tier** — NOT detected. Verified against a live snapshot: the hook payload carries
  `session_id, model, version, effort, thinking, fast_mode, cost, context_window,
  rate_limits{five_hour, seven_day}` — but **no plan/tier field**, and percentages can't reveal the
  absolute pool size. `tier` stays a plain user-set value (default `max_20x`); it's only a history
  label anyway (CONFIG-AUDIT bucket 1).

## Fail-open

- Hot path (statusline/warn): a missing OR unparseable config file is silently ignored — env +
  defaults win, the render never blanks, `Enabled()` stays true. The existing top-level `recover`
  guards remain.
- On-demand (`config show`/`config init`, `status`/`check`): a corrupt config is reported LOUDLY
  (these already fail loud by contract).
- Load once per process (short-lived hooks = one small JSON read per invocation; cheap).

## Testing

- `internal/config` unit tests: presence-aware decode (absent vs zero, esp. `enabled`), the
  friendly↔env mapping, corrupt-file → fail-open (no error escapes), `Enabled()` precedence.
- `internal/env` matrix test: `env > file > default` for a representative int and string knob.
- Kill-switch: `statusline.Command` / `warn.Hook` no-op when `enabled:false`; still render when absent.
- **Conformance isolation:** the readout/statusline harness must set `CCPOOL_CONFIG` to a nonexistent
  temp path so the developer's real `~/.claude/ccpool.json` can't leak into hermetic tests (add it to
  the redirected-env set alongside `USAGE_CACHE`/`CCPOOL_HISTORY`). Existing goldens stay green: the
  suite sets env, and env still wins.
- `ccpool config show` / `config init` golden or `.txtar` (dry-run detection with staged fixtures).

## Compatibility / migration

No on-disk breakage. Existing users are unaffected: no file → pure current behaviour (env +
defaults). Env still wins over the file, so anyone with `CCPOOL_*` exported keeps that behaviour. The
config file is purely additive.

## Scope summary (what's IN the file)

`enabled`, `pace.{profile,work_days,wake_hours,floor,weights,hour_weights}`,
`downshift.{mode,model,effort}`, `clock`, `colour`, `tier`, `history.{keep_days,min_interval}`.

The pace family is complete on purpose: a `custom`-profile user expresses their whole shape through
`weights`/`hour_weights`/`floor`, so those are genuine user choices, not internal tuning — they earn
a place in the file. `history.min_interval` rides along with `keep_days` (same "manage the history
file" concern).

Everything else stays env-only per CONFIG-AUDIT: the plumbing paths (bucket 1), `ccusage_cmd`, cache
TTLs, and all the `CHECK_*`/`WARN_*`/`RUNWAY_*`/`RHYTHM_*` threshold escape hatches (bucket 3) — those
encode contestable judgment calls or pure-internal fit params a normal user never reaches for.
