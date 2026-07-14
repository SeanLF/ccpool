# Changelog

All notable changes to ccpool are documented here. Format: [Keep a Changelog][kac]; ccpool aims
to follow [Semantic Versioning][semver] once it reaches 1.0 (majors reserved for output-contract /
exit-code breaks).

Pre-1.0: the API and output are still settling — expect the internals to change even where
behaviour doesn't.

## [Unreleased]

## [0.1.2] - 2026-07-13

### Fixed

- macOS binaries were killed on launch (`killed`, exit 137, no output) on stock Apple Silicon Macs.
  The v0.1.1 release signed them with quill (GoReleaser's cross-platform notarizer), whose Developer
  ID signature AMFI rejects fatally at exec on some macOS builds (`Broken signature with Team ID
  fatal`), so the process was SIGKILLed before `main()`. Signing now runs on a macOS runner with
  Apple's own `codesign` + `notarytool` (`scripts/macos-sign.sh`), which produces a signature macOS
  accepts everywhere. If you hit this on 0.1.1, `brew upgrade` to 0.1.2; to unblock an existing
  install without upgrading: `codesign --force --sign - "$(readlink "$(command -v ccpool)")"`.

## [0.1.1] - 2026-07-13

### Fixed

- Bare `ccpool` (no arguments) panicked with a slice-bounds error instead of defaulting to `status`:
  `dispatch` evaluated `args[1:]` before length-checking the argv. Fixed at the source; a CLI script
  test now covers the no-argument invocation (the entrypoint the golden suites, which call into each
  command directly, don't exercise).

## [0.1.0] - 2026-07-13

First public release: a single static Go binary that turns the account-global `rate_limits` % into
a dollar value for your weekly pool plus a pace verdict, and helps unattended sessions spend the
pool wisely. Complementary to `ccusage` and native `/status`, not a replacement.

### Added

- `ccpool status` — fuses the account-global `rate_limits` % with a ccusage-calibrated `$/1%` into a
  dollar value for your weekly pool plus a pace verdict.
- `ccpool check` — time, budget, and a keep-going/stop verdict for long or autonomous loops, with a
  working-hours runway (time-to-exhaustion measured per active hour).
- `ccpool run -- <cmd>` — downshifts subagent model/effort (`CLAUDE_CODE_SUBAGENT_MODEL` /
  `CLAUDE_CODE_EFFORT_LEVEL`) when you're burning ahead of pace, so unattended loops conserve the pool.
- `ccpool review [days]` — retrospective that flags expensive-model turns spent on trivial work.
- `ccpool warn` — Claude Code hook (`UserPromptSubmit` / `PostToolUse`) warning the agent mid-turn when
  over pace, near the 5h cap, or near context auto-compaction.
- `ccpool rhythm` — reads recent transcripts to gauge day/night rhythm strength and suggest
  `CCPOOL_WAKE_HOURS` / `CCPOOL_WORK_DAYS`; a suggester, never an auto-applier.
- `ccpool statusline` (+ `--embed`) — renders the pool gauge for a statusline, composable inside a host
  statusline as a ccstatusline widget.
- `ccpool init` — one-command onboarding that wires the statusline + `warn` hooks into
  `~/.claude/settings.json` and installs the bundled `checking-usage` skill into `~/.claude/skills/`:
  dry-run diff by default, `--apply` merges after a timestamped backup; idempotent, never-clobber,
  symlink-aware.
- Fails open across the hot path (hooks + statusline never break Claude Code) and delegates every
  dollar to `ccusage`, degrading to `%`-only when it's absent.

[kac]: https://keepachangelog.com/en/1.1.0/
[semver]: https://semver.org/spec/v2.0.0.html
