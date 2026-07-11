# ccpool — config file design (2026-07-10)

Status: approved design, pre-implementation. Companion to `docs/CONFIG-AUDIT.md` (which inventories
the env surface) and `docs/DECISIONS.md`.

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

Consequence of A2: the numeric knobs get file support for free (they already flow through `env`).
Only the four string settings (`CCPOOL_PACE_PROFILE`, `CCPOOL_CLOCK`, `CCPOOL_DOWNSHIFT`,
`CCPOOL_COLOR`) need a new `env.String(key, def)` and light rerouting at their call sites
(`profile.Load`, `clock`, `run`, `statusline.colorProfile`).

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
  "pace":      { "profile": "weekdays", "work_days": "1-5", "wake_hours": "9-17" },
  "downshift": { "mode": "auto", "model": "haiku", "effort": "low" },
  "clock":     24,
  "colour":    "auto",
  "tier":      "max_20x",
  "history":   { "keep_days": 30 }
}
```

Presence-aware decode (pointer fields, or a `map[string]json.RawMessage` presence pass) is
**load-bearing**: a missing `enabled` must mean *on*, not the zero-value `false`; a missing number
must fall through to its default, not become `0`.

### Friendly-key ↔ env-key mapping

An explicit table in `internal/config` maps each friendly path to its `CCPOOL_*` key and extracts the
string form from the parsed struct (present only when the pointer is non-nil). ~12 entries, e.g.
`pace.profile → CCPOOL_PACE_PROFILE`, `clock → CCPOOL_CLOCK` (int 24 → "24"),
`downshift.mode → CCPOOL_DOWNSHIFT`. This decouples the user-facing file shape from the internal env
names and gives one place to see the full documented surface.

## Kill-switch

Top-level `enabled` (default true). `warn.Hook` and `statusline.Command` check `config.Enabled()`
first and return a clean no-op when false (empty statusline, no warning) — a quiet install for
holidays/focus without unwiring `init`. Order: `CCPOOL_ENABLED` env (escape hatch) > file `enabled` >
true. A missing OR corrupt config never disables (fail-open must not accidentally silence the tool).

## Commands

- `ccpool config show` — render each in-scope setting: effective value + source
  (`env` / `file` / `detected` / `default`). The "why is my pace X?" answer. Fails LOUD on a corrupt
  file (on-demand).
- `ccpool config init [--force]` — detect + write `~/.claude/ccpool.json`. Refuses to clobber an
  existing file without `--force` (reports what it would change). Fails LOUD.
- `ccpool init` (unchanged: hooks) prints a one-line nudge when no config file exists yet:
  "run `ccpool config init` to personalize pace/clock/colour."

## Detection (off the hot path, at `config init` only)

Detection is why the file exists (persist an expensive result). It runs only at `config init`, never
per render. It is a HINT, not a promise — sick days, holidays, and irregular weeks make any rhythm
estimate approximate, so `even` (no-schedule) stays the safe default and every detected value is
trivially overridable in the file.

- **pace.profile** — from `rhythm`'s transcript analysis (reuse its suggestion logic; extract a
  callable). Expensive (scans `~/.claude/projects`), hence persisted.
- **clock** — from locale (`LC_TIME`/`LANG`; US-style → 12, else 24); falls back to `auto`.
- **tier** — from the hook payload's plan label ("Max (20x)" → `max_20x`) IF the payload carries it.
  **Implementation must verify the payload actually exposes the tier** before relying on it; if not,
  keep the `max_20x` default and let the user set it. Do not assert availability from memory.

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

`enabled`, `pace.{profile,work_days,wake_hours}`, `downshift.{mode,model,effort}`, `clock`, `colour`,
`tier`, `history.keep_days`. Everything else (paths, `pace.floor`/weight vectors, all `CHECK_*`/
`WARN_*`/`RUNWAY_*`/`RHYTHM_*` thresholds, `ccusage_cmd`, cache TTLs) stays env-only per CONFIG-AUDIT.
(`pace.weights`/`hour_weights` and `history.min_interval` are borderline — start env-only; promote
later only if a user actually reaches for them.)
