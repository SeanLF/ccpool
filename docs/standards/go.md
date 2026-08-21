# Go — house reference for ccpool

_As of 2026-08. Go moves ~2 releases/year; verify versions/experiments before relying. This is
scoped to **ccpool** (a single static Go binary), not a general Go guide. The design invariants in `AGENTS.md` are the contract; this says how to honour
them in Go._

## Why Go, and what we're porting

Driver is **distribution**: one static binary, no Ruby/`bin/ccpool`-launcher dependency, trivial
cross-compile. Port the **hot path first** — `warn` + `statusline` and the reads they need
(`pool`, `profile`, the history append, calibration-cache read) — keeping the exact on-disk
contract (snapshot JSON, `rate-limit-history.jsonl`, calibration cache) so the Go and Ruby sides
interoperate during the transition. The ~160 hermetic Ruby tests are the port's conformance oracle:
feed the same JSON fixtures, diff the output.

## Toolchain & versions

- **Go 1.27** (latest; `go1.27.0`, 2026-08). Carried over from 1.26: Green Tea GC on by default,
  `new()` taking an expression operand, self-referencing generics. New in 1.27: generic methods,
  size-specialized allocation of sub-80-byte objects, `goroutineleak` promoted from experiment to
  GA, and `encoding/json` re-backed by the v2 engine.
- **The macOS floor is 13 Ventura**, because 1.27 dropped Monterey. That is a shipped-binary
  decision, not just a build detail: the casks carry a matching `depends_on macos:`. See
  `docs/DECISIONS.md`.
- **`encoding/json` v2 is a measured ~38% regression on the isolated `review` decode, and ~4%
  on the actual command** (see `internal/analyzer/analyzer_bench_test.go` and the DECISIONS
  entry). Two lessons, both cheap: "the stdlib got faster" is a claim to benchmark, not a freebie;
  and a microbenchmark tells you what changed, not whether it matters. Get the end-to-end number
  before you act on the delta.
- The `go` line in `go.mod` (`go 1.27.0`) is a **floor, not a pin**, and worth being precise about:
  a newer local toolchain is used silently. All four workflows read `go-version-file: go.mod`, and
  `go.yml` + `release.yml` also set `check-latest: true`, so those actively take the newest matching
  release. Your machine and CI can drift apart, and from each other, with no signal. What the line
  DOES guarantee is the minimum: an older toolchain refuses the module outright, which is why "just
  build from source on an older macOS" is not the escape hatch it sounds like. Bump it deliberately.
- **Small static binary** is the goal: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"`. Track
  it across toolchain bumps: 1.27 cost +420 KB (+5.6%, 7,458,994 -> 7,879,250 bytes; ~7.5 MiB). When you compare, build both
  sides with identical flags -- omitting `CGO_ENABLED=0` on one side alone invents a percent.
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

- **JSON:** stdlib `encoding/json`, which as of Go 1.27 IS `encoding/json/v2` underneath (the v1
  API is a compatibility shim over the v2 engine; `encoding/json/v2` and `encoding/json/jsontext`
  are now importable directly). It is stricter, and **for our shape it is slower, not faster**:
  `rb.ParseObject` decodes each line into `map[string]any` with `UseNumber`, and that path lost
  ~38% in isolation, though only ~4% of `review`'s real wall time (benchmarked, see
  `docs/DECISIONS.md`). If the scan ever needs that back, the fix is a faster parse in
  `internal/rb`, not a toolchain pin. Decode into structs with explicit tags;
  tolerate unknown/extra fields (Claude's payload gains keys); that's the default, but never
  assume a field's presence, mirror the Ruby `typed?` guards.
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

The port exists to ship **one binary, easy to install and update**, and this is what shipped:
**GoReleaser v2 driven by a tag-push GitHub Action**, doing cross-compile, signing, archives,
checksums, GitHub Release and Homebrew in one run. This describes the pipeline as built; the files
themselves (`.goreleaser.yaml`, `.github/workflows/release.yml`) are the authority, and both carry
WHY-comments for the non-obvious parts.

- **`.goreleaser.yaml`**:
  - `builds:` with `env: [CGO_ENABLED=0]`, `flags: [-trimpath]`, `ldflags: -s -w` plus
    `-X main.{version,commit,date}`, over
    `goos: [darwin, linux]` x `goarch: [amd64, arm64]`. No windows: ccpool is Claude-Code-adjacent
    and mac/linux is the audience. `mod_timestamp` pins the binary mtime to the commit so builds
    are reproducible.
  - `hooks.post:` runs `scripts/macos-sign.sh` per built binary: **Apple's own codesign +
    notarytool, deliberately NOT GoReleaser's built-in `notarize.macos`** (quill). Quill's Developer
    ID signatures are rejected by AMFI at exec on some macOS builds; v0.1.1 shipped exactly that and
    SIGKILLed before `main`. The script no-ops off darwin and leaves Go's ad-hoc signature when no
    identity is set, so forks and local snapshot builds still work.
  - `archives:` + `checksum:` for tarballs and a `checksums.txt` on the Release. No SBOM or cosign
    signing today; notarization is the trust anchor that actually matters for a macOS binary.
  - `homebrew_casks:` (**casks, not `brews:`** -- the formula publisher is deprecated in v2 and a
    cask is the right shape for a prebuilt binary), publishing **two channels** to the separate tap
    repo `SeanLF/homebrew-tap`: `ccpool` with `skip_upload: "auto"` so rc tags stay out of the
    stable tap, and `ccpool-beta` taking every tag. That IS the auto-update path: install once,
    then `brew upgrade` tracks releases with no manual bumps. Both carry the macOS 13 floor via
    `custom_block` (see the Toolchain section); GoReleaser's `dependencies:` cannot express it.
- **Release workflow** (`.github/workflows/release.yml`), on `push: tags: ['v*']`, `contents: write`:
  `actions/checkout@v7` (`fetch-depth: 0` so the changelog sees prior tags), `actions/setup-go@v6`
  (`go-version-file: go.mod`), import the Developer ID cert into a throwaway keychain, stage the
  notary key, then `goreleaser/goreleaser-action@v7` with `args: release --clean`.
  - **Runs on `macos-latest`**, non-negotiable: real `codesign`/`notarytool` only exist there.
    Free and unlimited on public repos.
  - Six secrets, and a missing cert on the canonical repo refuses to release rather than silently
    shipping ad-hoc binaries: `HOMEBREW_TAP_TOKEN` (fine-grained PAT with Contents:read/write on
    the tap, since the default `GITHUB_TOKEN` cannot push cross-repo), `MACOS_SIGN_P12`,
    `MACOS_SIGN_PASSWORD`, `MACOS_NOTARY_ISSUER_ID`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_KEY`.
    The Apple credentials are team-scoped and shared with other projects.
  - `GORELEASER_CURRENT_TAG` pins the version to the triggering tag; without it GoReleaser derives
    it from `git describe`, which is ambiguous when an rc and its final tag share a commit.
- **Cutting a release:** land the change, update `CHANGELOG.md`, `git tag vX.Y.Z && git push
  --tags`. The Action does the rest; release notes are generated from conventional commits, with
  scope-tolerant filters (a bare `^docs:` regex misses `docs(readme):`, which used to leak every
  doc commit into the notes).
- **Other install paths for free:** `go install github.com/SeanLF/ccpool@latest` builds from the
  module path; the Release hosts prebuilt binaries + checksums for a curl-install. A
  `scoop`/`nix`/`AUR` publisher can join the same file if demand shows.
- **Dev-tool pinning:** `go tool` directives in `go.mod` (Go 1.24+) pin **gofumpt, staticcheck and
  govulncheck** reproducibly (no global installs, no tools.go). GoReleaser is NOT pinned there; it
  comes from the action, version-constrained to `~> v2`. `make check` runs `govulncheck ./...`, the
  Go-native vuln scanner that complements the CodeQL job.

## Pitfalls / anti-patterns

- **`panic` in a fail-open path** — the cardinal sin here. Recover at the boundary; never let a
  hook/statusline panic reach Claude Code.
- **Writing to a nil map** panics — initialize maps before use (easy to hit porting the history
  dedup).
- **Goroutine leaks**: if the warm-up uses goroutines, don't block on unreachable channels; the
  `goroutineleak` profile (experimental in 1.26, GA in 1.27) is there if we need to hunt them.
- **Over-abstraction** — no DI frameworks, no interface-per-struct ceremony. Concrete types until a
  second implementation actually exists.
- **Float formatting drift** — match the Ruby `$`/percent rounding exactly (the fixtures will catch
  divergence; verify against them, don't eyeball).
- **Silent `err` drops** (`_ = json.Unmarshal(...)`) outside a deliberate fail-open point — that's
  the Go version of an over-broad rescue. Fail open *on purpose* at the boundary, not by habit
  everywhere.

## Sources

- [Go 1.27 release notes](https://go.dev/doc/go1.27) (JSON v2 by default; macOS 13 floor) ·
  [Go 1.26 release notes](https://go.dev/doc/go1.26) · [Go 1.26 blog](https://go.dev/blog/go1.26)
- [Go 1.25 release notes](https://go.dev/doc/go1.25) (experimental JSON v2) ·
  [release history](https://go.dev/doc/devel/release)
- [Effective Go](https://go.dev/doc/effective_go) · [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- [GoReleaser: GitHub Actions](https://goreleaser.com/customization/ci/actions/) ·
  [GoReleaser: Homebrew](https://goreleaser.com/customization/homebrew/) ·
  [goreleaser/homebrew-tap example](https://github.com/goreleaser/homebrew-tap)
- [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) ·
  [go.mod tool directives (Go 1.24)](https://go.dev/doc/go1.24#tools)
