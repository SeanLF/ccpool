## What and why

<!-- What changes, and the reason it's worth making. Link the issue if there is one. -->

## Checklist

- [ ] `make check` passes (gofumpt + vet + staticcheck + govulncheck + `go test ./...`)
- [ ] Claims verified by running it the way a user would, not asserted from memory
- [ ] Anything on the fail-open path (`warn` / `statusline`) still can't raise — it must never
      break Claude Code
- [ ] A repro or PoC is included if this is a bug fix
- [ ] `CHANGELOG.md` `[Unreleased]` updated if this changes behaviour
- [ ] Design invariants in `AGENTS.md` respected (or the deviation is justified in the PR / a
      `docs/DECISIONS.md` note)
