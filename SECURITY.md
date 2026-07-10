# Security Policy

## Reporting a vulnerability

Please report privately, not via a public issue:

- GitHub → the repository's **Security → Report a vulnerability** (private advisory), or
- email **contact@seanfloyd.dev**.

You'll get an acknowledgement within a few days.

## Scope

ccpool runs locally against your own `~/.claude` data. The surface worth a security report:

- **`ccpool init`** writes/merges `~/.claude/settings.json` — a bug that clobbers, corrupts, or
  injects unintended hooks/commands into that file (it's meant to be idempotent, never-clobber,
  symlink-aware, and to back up before writing).
- **The hooks/statusline path** executes as part of Claude Code — anything that could let crafted
  input (a malformed payload on stdin, a poisoned snapshot/transcript file) cause code execution,
  a crash that breaks Claude Code, or a write outside the expected paths.
- **`ccpool run`** sets subagent model/effort env and `exec`s a command — anything that could let
  it run an unintended command.

Out of scope: the accuracy of the `$`/pace estimates (they're advisory, API-equivalent, and
self-calibrated — see the README's "Honest limitations"), and issues in `ccusage` itself (report
those upstream).
