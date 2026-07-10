# Go — house reference for the ccpool port

_As of 2026-07. Go moves ~2 releases/year; verify versions/experiments before relying. This is
scoped to **ccpool's Ruby → Go migration** (committed for v1 — see `docs/RUST-REIMPL.md`), not a
general Go guide. The design invariants in `AGENTS.md` are the contract; this says how to honour
them in Go._

## Why Go, and what we're porting

Driver is **distribution**: one static binary, no Ruby/`bin/ccpool`-launcher dependency, trivial
cross-compile. Port the **hot path first** — `warn` + `statusline` and the reads they need
(`pool`, `profile`, the history append, calibration-cache read) — keeping the exact on-disk
contract (snapshot JSON, `rate-limit-history.jsonl`, calibration cache) so the Go and Ruby sides
interoperate during the transition. The ~160 hermetic Ruby tests are the port's conformance oracle:
feed the same JSON fixtures, diff the output.

## Toolchain & versions

- **Go 1.26** (latest; `go1.26.5`, 2026-07). Green Tea GC is now default (free ~10-40% GC win, not
  that ccpool is GC-bound). `new()` takes an expression operand; generics may self-reference.
- Pin the toolchain in `go.mod` (`go 1.26`) so builds are reproducible; let `GOTOOLCHAIN=auto`
  fetch it.
- **Small static binary** is the goal: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"`.
  Cross-compile is just `GOOS`/`GOARCH` — ship `darwin/{arm64,amd64}` + `linux/{arm64,amd64}` from
  one machine, no toolchain juggling (this is the whole reason we chose Go over Rust).
- Format `gofmt`/`gofumpt`; vet `go vet`; lint with **staticcheck** (or golangci-lint) — the
  analog of the Ruby rubocop gate. Wire the same into CI (swap the Ruby job).

## Philosophy & idioms (honour the invariants)

- **Fail OPEN on the hot path — via `recover`, not by ignoring errors.** Ruby's blanket
  `rescue StandardError` on `warn`/`statusline` becomes: return `error` values normally, AND put a
  single `defer func(){ recover() }()` at the very top of each hook/statusline entry point so an
  unexpected panic (nil deref, bad index) can NEVER escape and break Claude Code. A panic that
  reaches Claude Code is the Go equivalent of the bug we spent this project avoiding. On-demand
  commands (`status`/`check`/`init`) stay fail-LOUD — return the error, exit non-zero.
- **Errors are values.** Wrap with `%w`, test with `errors.Is`/`errors.As`. A best-effort read
  returns `(zero, err)` and the caller decides; don't `log.Fatal` inside a library path.
- **Make illegal states unrepresentable.** Model the pace/verdict/confidence tiers as typed
  constants / small sum-type-ish enums with an exhaustive `switch` (no catch-all `default` when a
  new case should force a compile error), not bare strings/bools. This is the Go answer to the
  Ruby symbol tiers (`:fresh`/`:estimated`/`:stale`).
- **Stay near stdlib — resist frameworks.** ccpool is ~zero-dep by design; keep it that way.
  `encoding/json`, `os`, `os/exec`, `time`, `flag` cover the whole tool. **Do NOT** pull cobra/viper
  for a handful of subcommands — a `switch` on `os.Args[1]` mirrors the current Ruby dispatch and
  keeps the binary tiny. Every dependency is a supply-chain + size cost for a tool whose selling
  point is "one small binary."
- **One concern per file, small packages.** The flat Ruby layout doesn't port 1:1; use a lean
  package split (e.g. `internal/pool`, `internal/calib`, `internal/statusline`) but resist
  over-structuring — this is a small CLI, not a service.

## Defaults worth adopting

- **JSON:** stdlib `encoding/json` for now. `encoding/json/v2` exists but is **experimental**
  (opt-in `GOEXPERIMENT=jsonv2`) — watch it (it's faster and stricter), don't depend on it in a
  shipped binary yet. Decode into structs with explicit tags; tolerate unknown/extra fields
  (Claude's payload gains keys) — that's the default, but never assume a field's presence, mirror
  the Ruby `typed?` guards.
- **Subprocess (`ccpool run`, ccusage):** `os/exec` with a `context.Context` timeout so a hung
  `npx ccusage` can't block; `syscall.Exec` for `run`'s true passthrough if we want to replace the
  process image like the Ruby `exec`.
- **Background warm-up (the calibration warmer):** a detached child via `exec.Command` with the
  process fully released — or, cleaner in one binary, a goroutine that writes the cache and is
  allowed to outlive the render only if we double-fork; keep the same throttle-marker + fail-open
  shape.
- **Time:** `time.Now().Unix()` for the epoch stamps; keep everything in the machine's local zone
  as the Ruby does (rhythm/pace depend on it).
- Logging: `log/slog` if we want structured logs; the statusline anomaly log stays a capped file.

## Release engineering & distribution (the whole point of the port)

The migration exists to ship **one binary, easy to install and update**. The modern Go answer is
**GoReleaser (v2) driven by a tag-push GitHub Action** — it does cross-compile + archives +
checksums + GitHub Release + Homebrew in one run. Sketch (verify the schema against current
GoReleaser docs — keys like `brews`/`homebrew_casks` shift between majors):

- **`.goreleaser.yaml`** at the repo root:
  - `builds:` — `env: [CGO_ENABLED=0]`, `flags: [-trimpath]`, `ldflags: -s -w`, and a
    `goos: [darwin, linux]` × `goarch: [amd64, arm64]` matrix (drop windows unless a user asks;
    ccpool is a Claude-Code-adjacent tool, mac/linux is the audience).
  - `archives:` + `checksum:` — tarballs + a `checksums.txt` on the Release.
  - `brews:` (Homebrew formula publisher) — points at a **separate tap repo** (`sean/homebrew-tap`)
    with a token; on each release GoReleaser writes/commits `Formula/ccpool.rb` there. **That is
    the Homebrew auto-update:** `brew install sean/tap/ccpool` once, then `brew upgrade` always
    pulls the newest release — no manual formula bumps.
  - Optional supply-chain: `sboms:` (SBOM), `signs:`/cosign (sign the checksums), SLSA provenance.
- **Release workflow** (`.github/workflows/release.yml`): trigger on `push: tags: ['v*']`, steps =
  `actions/checkout` (with `fetch-depth: 0` for the changelog) → `setup-go` →
  `goreleaser/goreleaser-action@v6` with `version: "~> v2"`, `args: release --clean`. Secrets:
  the built-in `GITHUB_TOKEN` for the Release, plus a **PAT (`HOMEBREW_TAP_TOKEN`) with `repo`
  scope on the tap repo** so GoReleaser can push the formula cross-repo. Scope permissions
  `contents: write`.
- **Cutting a release** becomes: land the change, update `CHANGELOG.md`, `git tag vX.Y.Z && git
  push --tags`. The Action does the rest. Release notes: let GoReleaser generate from commits, or
  feed the `[Unreleased]` changelog section.
- **Other install paths for free:** `go install github.com/sean/ccpool@latest` works off the module
  path (source build); the GitHub Release hosts prebuilt binaries + checksums for a curl-install.
  A `scoop`/`nix`/`AUR` publisher can be added to the same `.goreleaser.yaml` later if demand shows.
- **Modern dev-tool pinning:** use `go tool` directives in `go.mod` (Go 1.24+) to pin
  `staticcheck`, `govulncheck`, and `goreleaser` reproducibly (no global installs, no separate
  tools.go). Run **`govulncheck ./...`** in CI — the Go-native vuln scanner, the analog of the
  CodeQL job.

## Pitfalls / anti-patterns

- **`panic` in a fail-open path** — the cardinal sin here. Recover at the boundary; never let a
  hook/statusline panic reach Claude Code.
- **Writing to a nil map** panics — initialize maps before use (easy to hit porting the history
  dedup).
- **Goroutine leaks** — if the warm-up uses goroutines, don't block on unreachable channels; Go
  1.26 ships a `goroutineleak` profile if we need to hunt them.
- **Over-abstraction** — no DI frameworks, no interface-per-struct ceremony. Concrete types until a
  second implementation actually exists.
- **Float formatting drift** — match the Ruby `$`/percent rounding exactly (the fixtures will catch
  divergence; verify against them, don't eyeball).
- **Silent `err` drops** (`_ = json.Unmarshal(...)`) outside a deliberate fail-open point — that's
  the Go version of an over-broad rescue. Fail open *on purpose* at the boundary, not by habit
  everywhere.

## Sources

- [Go 1.26 release notes](https://go.dev/doc/go1.26) · [Go 1.26 blog](https://go.dev/blog/go1.26)
- [Go 1.25 release notes](https://go.dev/doc/go1.25) (experimental JSON v2) ·
  [release history](https://go.dev/doc/devel/release)
- [Effective Go](https://go.dev/doc/effective_go) · [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- [GoReleaser: GitHub Actions](https://goreleaser.com/customization/ci/actions/) ·
  [GoReleaser: Homebrew](https://goreleaser.com/customization/homebrew/) ·
  [goreleaser/homebrew-tap example](https://github.com/goreleaser/homebrew-tap)
- [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) ·
  [go.mod tool directives (Go 1.24)](https://go.dev/doc/go1.24#tools)
