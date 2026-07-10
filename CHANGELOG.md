# Changelog

All notable changes to ccpool are documented here. Format: [Keep a Changelog][kac]; ccpool aims
to follow [Semantic Versioning][semver] once it reaches 1.0 (majors reserved for output-contract /
exit-code breaks).

Pre-1.0: the API and output are still settling, and **the implementation migrates from Ruby to Go
before 1.0** (see `docs/RUST-REIMPL.md`) — expect the internals to change even where behaviour
doesn't.

## [Unreleased]

### Added

- `ccpool init` — one-command onboarding that wires the statusLine + `warn` hooks into
  `~/.claude/settings.json`. Dry-run diff by default; `--apply` merges after a timestamped backup.
  Idempotent, never-clobber, symlink-aware, aborts rather than corrupting a dangling symlink or an
  unparseable settings file.
- `ccpool statusline --embed` — compact `pool % · $-left · pace` segment for embedding in a host
  statusline (verified as a ccstatusline custom-command widget). `init` detects a ccstatusline host
  and prints the widget recipe instead of replacing it.
- Background, throttled, fail-open calibration warm-up on the statusline path, so the `$` value
  self-populates even for a statusline-only / embedded user.

### Changed

- Demoted 10 never-user-tuned internal `ENV` knobs (`USAGE_BURN_*`, `USAGE_SES_*`,
  `USAGE_CACHE_*_SECS`, `CCPOOL_BLOCKS_TTL`) to plain constants — no behaviour change.
- Hardened the calibration cache to self-heal on corruption and to surface ccusage schema-drift on
  the background path.

[kac]: https://keepachangelog.com/en/1.1.0/
[semver]: https://semver.org/spec/v2.0.0.html
