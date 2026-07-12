# ccpool

**How much of your weekly Claude pool is left — in dollars — and are you burning it too fast?**

Your usage tools show a percentage and a reset time. Neither tells you what that percentage is
*worth*, whether you're ahead of pace, or when you'll actually run dry. ccpool does — and it's
**complementary to `ccusage` and native `/status`, not a replacement**: it delegates every dollar
to ccusage and reads the account-global `rate_limits` % that ccusage structurally can't see.

![ccpool statusline and status readout](demo/overview.gif)

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
  distinguishing a temporary 5h throttle from a real "stop for the week." Includes a **working-
  hours runway** — *"~18–38 working-hours of pool left → you'd throttle before reset"* vs
  *"budget outlasts the week → burn freely"* — the time-to-exhaustion reframe of "% left",
  measured per *active* hour (so sleep doesn't dilute it) and bounded by which resource binds.
- **`ccpool warn`** — a Claude Code hook (wire at `UserPromptSubmit`/`PostToolUse`) that warns
  the agent mid-turn when it's over pace, near the 5h cap, or near context auto-compaction.

It delegates every dollar to `ccusage` (never hand-rolls pricing) and reads the `rate_limits`
% that ccusage structurally can't. Fails **open** on any missing/stale data — it never blocks
Claude Code.

## Install

ccpool is a single static Go binary (no runtime deps beyond optional `ccusage` for the `$`).

```sh
brew install SeanLF/tap/ccpool        # once released; then `brew upgrade` tracks new versions
# or from source:
go install github.com/SeanLF/ccpool@latest
# or build locally:
make build && export PATH="$PWD:$PATH"

ccpool init                      # dry-run: shows exactly what it would wire, writes nothing
ccpool init --apply              # wires it in (timestamped backup first) -- zero config needed
```

![ccpool init dry-run](demo/init.gif)

`ccpool init` is the whole setup: it adds the `statusLine` command plus the mid-turn `warn`
hooks to `~/.claude/settings.json`. It's **dry-run by default** (prints a diff so you see the
exact change first), **idempotent** (re-run it anytime — it says "already set up" instead of
duplicating), **never-clobber** (merges alongside your other hooks/permissions, never replaces
them), and **symlink-aware** (if `settings.json` is a symlink to a dotfiles source it edits the
real target and leaves the link intact). `--apply` takes a `settings.json.bak.<ts>` backup
before writing. No env vars required — good defaults are the point.

**Data source.** ccpool reads per-session `~/.claude/usage-cache-*.json` snapshots for the
`rate_limits` %. On a fresh machine those don't exist (vanilla Claude Code doesn't write them) —
the `statusLine` command `ccpool init` wires is what self-populates them. Doing it by hand instead:

```jsonc
// ~/.claude/settings.json
{ "statusLine": { "type": "command", "command": "ccpool statusline" } }
```

`ccpool statusline` captures `rate_limits` from CC's payload, seeds the history the `$`
calibration needs, and renders a compact line (`pool 9% · $2.3k left · pace -20↓`). If you
*already* run a statusline that writes those snapshots (e.g. a custom one), ccpool just reads
it — no statusLine change needed (`init` detects it and flags the conflict rather than
clobbering; re-run with `--replace-statusline` to take it over). Run **`ccpool statusline` bare
in a terminal** to preview what the line looks like (it renders from the freshest snapshot
instead of hanging on stdin), and **`ccpool help`** for the full command list.

### Keep your statusline — compose, don't replace

ccpool is a *specialized pool gauge*, not a general statusline (it deliberately shows no
model/git/dir — that's your host statusline's job). So if you already run one, add ccpool
*inside* it instead of switching. [ccstatusline](https://github.com/sirmalloc/ccstatusline)
forwards Claude's full payload (incl. `rate_limits`) to its **Custom Command** widgets
(verified), so ccpool renders natively as a widget:

```
# in ccstatusline's config, add a Custom Command widget with command:
ccpool statusline --embed
```

`--embed` prints just ccpool's differentiator — `pool 45% $1.4k +2↑` (weekly % · $-of-pool
left · pace) — and leaves ctx/5h/model/git to the host. `ccpool init` auto-detects a
ccstatusline statusLine and prints this recipe instead of offering to replace it. The `$`
self-populates even if ccpool is *only* ever a widget: each render kicks off a throttled
background calibration warm-up (never blocking the line). (claude-powerline and CCometixLine
don't forward the payload / don't take external commands, so there ccpool has to be the
statusLine — `ccpool init --replace-statusline`.)

## Usage

```sh
ccpool init --apply              # one-time: wire ccpool into Claude Code (dry-run without --apply)
ccpool status                    # full readout
ccpool check                     # keep-going/stop verdict (long / autonomous loops)
ccpool run -- claude -p "..."    # or wrap a fan-out script; downshifts when ahead of pace
ccpool review 7                  # provisioning review, last 7 days
ccpool rhythm                    # read-only: your work rhythm + a suggested pace profile
```

`ccpool rhythm` reads your last 30d of transcripts (in the *current* machine's local time, so
timezone travel doesn't corrupt it) and measures rhythm strength `R` — the circular resultant
over a 24h clock. High `R` = a sharp day/night rhythm, so it prints a concrete `CCPOOL_WAKE_HOURS`
(+ `CCPOOL_WORK_DAYS`) to adopt; low `R` = continuous loops fill the clock, so it says stick with
`even`. It's a suggester, never an auto-applier — the honest read is that a schedule only helps
when the rhythm is strong enough to detect in the first place. Tune with `CCPOOL_RHYTHM_WINDOW`
(days, default 30) and `CCPOOL_RHYTHM_R` (the strong/weak gate, default 0.5).

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

## Config file

ccpool reads a config file at `~/.ccpool/ccpool.json` (override `CCPOOL_CONFIG`). Zero-config still
works, every setting below has a default; the file just persists your choices so they survive
without keeping env vars exported. Resolution order is **env > file > default**; env stays the
override/escape hatch, the file is where a chosen or detected value lives.

In scope: `enabled`, `pace` (`profile`/`work_days`/`wake_hours`/`floor`/`weights`/`hour_weights`),
`downshift` (`mode`/`model`/`effort`), `clock`, `colour`, `tier`, `history`
(`keep_days`/`min_interval`). A realistic file:

```jsonc
{
  "enabled": true,
  "pace":      { "profile": "workhours", "work_days": "1-5", "wake_hours": "9-17" },
  "downshift": { "mode": "auto", "model": "haiku", "effort": "low" },
  "clock":     "24",
  "colour":    "truecolor",
  "tier":      "max_20x",
  "history":   { "keep_days": 30, "min_interval": 60 }
}
```

`enabled: false` is a kill-switch: the statusline and `warn` hook go quiet (no-op) without
unwiring anything from `settings.json`, handy for a holiday or a focus block.

**Commands.** `ccpool config show` prints the effective value of every setting plus which layer
supplied it (`env`/`file`/`default`): the "why is my pace X?" answer. `ccpool config init` seeds
the file: dry-run by default (prints the plan, writes nothing), `--apply` writes it
(fill-missing-only, never clobbers a value you already set), `--apply --force` re-detects
everything and overwrites from scratch. `ccpool init --apply` also seeds the config as part of
first-time setup, so a single command wires the hooks *and* the file.

Detection is off the hot path (only `init`/`config init` run it, never a render): it infers
`work_days`/`wake_hours` from your work rhythm (the same analysis behind `ccpool rhythm`) and
resolves `clock`'s `auto` mode once to a concrete `12`/`24`, persisting both so future renders skip
the scan/subprocess. `pace.profile` itself is left unset by detection (so it resolves to its
`even` default until you set it by hand); everything else seeds at its plain default too.
`colour` is never detected (the hook renders to a non-tty pipe, so there's nothing to probe) and
`tier` is never detected (the hook payload carries no plan/tier field).

The threshold escape hatches (`CCPOOL_CHECK_*`/`WARN_*`/`RUNWAY_*` and friends, below) are
deliberately **not** in the file; they're power-user overrides on internal judgment calls, not
user-shape settings.

## Config (env)

| var | default | meaning |
|---|---|---|
| `CCPOOL_PACE_MARGIN` | `3` | pts over pace before `run` downshifts / `warn` nags |
| `CCPOOL_DOWNSHIFT` | `auto` | `auto` (enforce) · `advise` (print, don't apply — like the native tab) · `off` |
| `CCPOOL_DOWNSHIFT_MODEL` / `_EFFORT` | `haiku` / `low` | what to downshift subagents to |
| `CCPOOL_CALIB_TTL` | `21600` | seconds to cache the `$/1%` calibration |
| `CCPOOL_CCUSAGE_CMD` | `npx -y ccusage@20` | how to invoke ccusage (pinned major — see internal/calib) |
| `CCPOOL_HISTORY_KEEP_DAYS` | `30` | `prune --history` cutoff; `0` = keep raw forever (some prefer the full ~20 MB/mo) |
| `CCPOOL_HISTORY_MIN_INTERVAL` | `60` | min seconds between 5h-only history writes (curbs file growth) |
| `CCPOOL_CLOCK` | `24` | wall-clock time format everywhere: `24` · `12` · `auto` (best-effort OS detect, macOS-only, falls back to 24) |
| `NO_COLOR` / `TERM=dumb` | — | standard contract ([no-color.org](https://no-color.org)): any **non-empty** `NO_COLOR` (or `TERM=dumb`) strips all ANSI from the statusline (degrades to plain text) |
| `CCPOOL_HOME`, `CCPOOL_DB` | `~/.ccpool`, `$HOME/ccpool.db` | ccpool state dir + SQLite store path (test isolation) |

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
- **`seven_day` is only the ALL-MODELS weekly window.** Anthropic tracks *separate* per-model
  weekly caps — a Sonnet-only one ([#27915](https://github.com/anthropics/claude-code/issues/27915))
  and a distinct Fable weekly bucket — that `/status` shows but that are **not** in the
  `rate_limits` payload ccpool reads. So you could hit a per-model weekly cap with ccpool showing
  the main pool healthy. Minor for mixed use; a churn risk if Anthropic adds more buckets. Treat a
  healthy weekly % as necessary-but-not-sufficient for model-heavy work and check `/status`.
- **`review` proxies effort** from output-token volume + tool-call count (effort isn't logged
  per-turn); `ultrathink`/thinking inflate output invisibly. Treat it as a hint, not a verdict.

## Tests

```sh
make check    # gofumpt + vet + staticcheck + govulncheck + go test ./...
```

Conformance suites diff every command's output against committed golden files (no `~/.claude`
access; hermetic `CCPOOL_*` env). ccusage is mocked in tests via `CCPOOL_CCUSAGE_CMD`.
