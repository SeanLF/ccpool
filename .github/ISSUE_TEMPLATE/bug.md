---
name: Bug report
about: Something ccpool does wrong, crashes on, or reports incorrectly
title: ""
labels: bug
---

**What happened, and what you expected instead.**

**Repro** — steps that don't depend on your machine. The exact command, and any relevant fixture
(a snapshot / history line) if it's data-dependent. ccpool never touches real `~/.claude` in tests,
so a hermetic repro (staged `CCPOOL_*` env) is ideal.

**Output** — paste the actual output (redact usage numbers if you like; the shape is what matters).
If it's on the statusline or a hook, note whether it broke Claude Code (it never should).

**Versions** — ccpool (commit or version), `go version`, and `ccusage` version if the `$` value is
involved.
