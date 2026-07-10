# ccpool — native-binary reimplementation: scope & decision (2026-07-10)

> **Filename is legacy** ("RUST-REIMPL") — the measurement below lands on **Go**, not Rust, and
> on **defer the build**, not do-it-now. This is a decision doc, gated on measurement. No Rust
> or Go was written for it.

## TL;DR

- **Measured the hot path.** Ruby 4.0 (+Prism) cold-process cost: `warn` ~45 ms median / 65 ms
  p90; `statusline` ~63 ms / 69 ms. Of that, **~32 ms is bare interpreter startup** (`ruby -e ''`)
  — paid on *every* fire, *including the common case where `warn` decides to stay silent*, because
  the pace/throttle logic lives inside the process the boot pays for.
- **Aggregate per turn is small.** Real transcripts (20 largest, 1,347 user turns, 8,187 tool
  calls) → **6.1 tool calls/turn**, so `warn` fires ~7×/turn (1 UserPromptSubmit + ~6 PostToolUse).
  ~7 × 45 ms ≈ **0.3 s of hook startup per turn**. Against a turn that spends tens of seconds in
  model + tool time, that's **well under 1%**. Noticeable only as ~45 ms tacked onto each *fast*
  tool call (a sub-100 ms Bash), invisible behind a slow one (a 5 s test run).
- **Verdict: latency alone does NOT clear the bar today.** The measured pain is modest. The
  reimpl's real driver is **distribution** (ship one static binary, no Ruby dependency) — which
  matters for the *sharing* goal `ccpool init` exists to serve, but isn't urgent while the user
  base is ~1. **Recommendation: DEFER the build; keep this as the ready-to-execute design.**
- **When it's built: Go**, port only `warn` + `statusline` first (the hot path), leave the
  on-demand commands in Ruby where startup is irrelevant.

## The measurement (the gate)

Method: `bench.rb` timed full cold-process invocations (fork+exec ruby → `require` the module
graph → run → exit) with `Process::CLOCK_MONOTONIC`, 3 warmup rounds, n=40, representative stdin
payloads. Ruby 4.0.5 +PRISM, arm64 (M4 Pro).

| command | median | p90 | what it isolates |
|---|---:|---:|---|
| `ruby -e ''` | 31.9 ms | 33.2 ms | interpreter floor (unavoidable in Ruby) |
| `ccpool help` | 40.3 ms | 49.9 ms | + `require`-ing the whole module graph (~8 ms) |
| `ccpool warn` (HOT) | 45.4 ms | 65.3 ms | + read stdin, load snapshots, decide, (usually) stay silent |
| `ccpool statusline` (HOT) | 62.9 ms | 69.3 ms | + capture snapshot, seed history, render the coloured line |

Fires per turn, from real usage (`~/.claude/projects/**/*.jsonl`, 20 largest transcripts):

- **6.1 tool calls / user turn** → PostToolUse `warn` fires ~6×, plus 1 UserPromptSubmit `warn`.
- `statusline` fires event-driven (`refreshInterval: 10` + on activity) — call it a few ×/min
  during active use.

**The insight that most favours a port:** `warn` pays the full ~32 ms interpreter floor on every
one of those ~7 fires/turn *even when it emits nothing* — under pace, stale data, or throttled
(PostToolUse repeat-suppression happens *inside* the booted process). A compiled binary drops that
floor to ~1–3 ms, i.e. a **~15–30× cut on the exact path that runs most and most-often does
nothing.** That's the strongest single argument. It's just multiplied by a small enough base that
the absolute saving (~0.3 s/turn) doesn't hurt today.

## Which driver dominates?

Two independent reasons to port. Be honest about which one is real:

1. **Hot-path latency** — *measured, modest.* <1% of turn wall-clock; ~45 ms per fast tool call.
   By the brief's gate ("justified ONLY if measurement shows pain") this **does not, on its own,
   justify the rewrite today.** It would start to bite a *very* tool-dense tight loop (dozens of
   sub-100 ms tool calls back-to-back), which isn't ccpool's normal shape.
2. **Distribution** — *the actual driver, not yet urgent.* `ccpool init` exists so *other people*
   can adopt ccpool. Ruby-as-dependency is a real adoption tax (version skew, `mise`/`rbenv`, the
   `bin/ccpool` launcher already exists only to paper over "which ruby"). A single static Go binary
   — `brew install` or download-one-file, `GOOS/GOARCH` cross-compiled — is a materially better
   "get the most out of your pool in 30 seconds" story. **Caveat:** it can't reach *zero* runtime
   deps, because the `$` calibration shells out to `ccusage` (Node/npx). But ccusage is optional
   (fail-open), so "one binary + optional Node for the dollar readout" still beats "Ruby + optional
   Node."

**So: the port is a distribution play, not a performance play.** It becomes worth building when
there's a second user who hits the Ruby-install friction — not before. Premature now.

## Language: Go (when it happens)

| | Go | Rust |
|---|---|---|
| startup | ~1–3 ms | ~1 ms |
| cross-compile | `GOOS/GOARCH`, trivial, static by default | needs `cross`/targets; musl faff for static Linux |
| JSON | `encoding/json` — ccpool is *all* JSON I/O | `serde` (excellent, but more ceremony) |
| binary size | ~2–5 MB | ~0.5–1.5 MB |
| dev velocity | high (GC, simple) | lower (borrow-checker tax on glue code) |
| fit for ccpool | **I/O + JSON + print; zero hot inner loops** | overkill; its wins (no GC, μs-latency) are moot here |

**Recommend Go.** ccpool has no perf-critical inner loop — it reads a few small JSON files,
does arithmetic, prints a line. Rust's advantages (smaller binary, no GC pause, memory control)
buy nothing measurable here, while its cross-compile-to-static story is fussier than Go's and its
iteration is slower. Go's trivial `GOOS=darwin/linux GOARCH=arm64/amd64` matrix is exactly the
distribution win that motivates the port in the first place. (If binary size ever became the whole
point — e.g. embedding — revisit Rust.)

## Scope (when built)

**Phase 1 — port the hot path only.** `warn` + `statusline`, plus the shared reads they need:
`pool` (snapshot reconcile), `profile` (pace weighting), the `rate-limit-history.jsonl`
append/dedup, and the `resolve_weekly` tiering. That's the ~800 lines that run on every
prompt/tool/render. Everything else — `status`, `check`, `review`, `rhythm`, `run`, `init`,
`prune` — is on-demand; 45 ms there is invisible, so **leave it in Ruby.** A shared on-disk
contract (the snapshot files, the history JSONL, the calibration cache) already decouples the two,
so a Go `warn`/`statusline` and a Ruby `status`/`check` coexist with zero IPC.

**Phase 2 (only if single-binary distribution is the goal)** — port the on-demand commands too, so
there's one binary and no Ruby at all. Bigger job (`review`/`rhythm` do the most logic), justified
only by the "one file, no runtime" pitch, not by speed.

## Effort & coexistence

- **Phase 1:** ~2–4 focused days. The logic is small and already well-specified by the Ruby +
  its 140+ hermetic tests, which become the Go port's conformance oracle (feed the same JSON
  fixtures, diff the output). Risk is low precisely because the behaviour is pinned by tests.
- **Coexistence during transition:** both implementations read/write the *same* files. `ccpool
  init` wires whichever binary is on PATH; a Go `warn` and a Ruby `status` interoperate through the
  snapshot/history/calibration files with no shared process state. Ship the Go binary as
  `ccpool-hot` (or replace `bin/ccpool`'s hot-path dispatch), keep Ruby for the rest, and migrate
  command-by-command. Fail-open contract is unchanged: a Go hook that errors must still exit 0 and
  emit nothing, exactly like the Ruby one.
- **Migration ordering:** port `statusline` first (it's the data *producer* — get capture/seed
  byte-identical and verified against fixtures before anything reads it), then `warn` (the
  *consumer*). Keep the Ruby versions runnable until the Go ones pass the full fixture suite.

## Decision

**DEFER.** The build is correctly scoped and ready, but neither driver is pressing: latency is
sub-1% per turn, and distribution demand is theoretical until a second user appears. Revisit when
(a) someone hits Ruby-install friction adopting ccpool, or (b) a genuinely tool-dense tight loop
makes the ~45 ms/fire visible. Until then, this doc is the plan; the Ruby stays.
