# Config File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist a user's durable choices to `~/.ccpool/ccpool.json` (resolved `env > file > default`), seeded by one-time detection off the hot path, with a top-level kill-switch.

**Architecture:** `internal/env` becomes the single resolution point (`os env → config file → default`); because A2 already routes numeric knobs through it, they get file support for free. A new `internal/config` owns the JSON schema (pointer fields for presence), a fail-open cached load, the friendly↔env key mapping, `Enabled()`, detection, and the `config show`/`config init` commands. Four string call-sites reroute through a new `env.String`.

**Tech Stack:** Go, stdlib `encoding/json` (no new deps). Spec: `docs/config-file-design.md`.

## Global Constraints

- **Go, near-stdlib, zero new shipped deps.** JSON via stdlib.
- **Fail-open on the hot path:** statusline/warn must never panic or blank on a missing/corrupt config; on-demand commands (`config show`/`config init`) fail LOUD.
- **`env > file > default`** resolution order, everywhere. Env still wins (existing conformance sets env and must stay green).
- **Dry-run by default; `--apply` writes.** Nothing touches disk without `--apply` (matches existing `ccpool init`).
- **Gate:** `make check` (gofumpt + vet + staticcheck + govulncheck + `go test ./...`) green before every commit. Prefix go commands with `unset GOROOT`.
- **Commit gate:** run pr-review-toolkit code-reviewer + code-simplifier before each commit (silent-failure-hunter for the fail-open/kill-switch task). The PreToolUse hook drops staging on block — re-`git add` after a denial.
- **No em dashes in prose/comments/commits.** Conventional commits, `Co-Authored-By` trailer.

## File structure

- `internal/paths/paths.go` (modify) — add `Config()` path.
- `internal/config/config.go` (new) — LOW-LEVEL, imported by `env`: `Config` struct, `Load`, `HooksEnabled`, `Lookup`, `Merge`, `Write`. Imports only `paths` + stdlib — **must not import rhythm/clock/env** (see cycle note).
- `internal/config/config_test.go` (new) — unit tests.
- `internal/configcmd/configcmd.go` (new) — HIGH-LEVEL, imported only by `main`: `Detect` (rhythm+clock), `Show`, `Init`. May import `config`, `rhythm`, `clock`, `env`.
- `internal/configcmd/configcmd_test.go` (new) — unit tests.

> **Import-cycle note (load-bearing):** `env` imports `config` (for the file layer). After Task 4, `clock` imports `env`, and `rhythm` imports `clock`. So if `config` imported `rhythm`, the cycle `config -> rhythm -> clock -> env -> config` would form. Detection therefore lives in a SEPARATE package `internal/configcmd` (imported only by `main`), keeping `internal/config` a leaf that `env` can safely depend on.
- `internal/env/env.go` (modify) — add `String`; route all getters through config; add `Resolve` (provenance).
- `internal/env/env_test.go` (modify) — config-layer + `String` tests.
- `internal/profile/profile.go`, `internal/clock/clock.go`, `internal/statusline/statusline.go`, `internal/run/run.go`, `internal/history/history.go` (modify) — reroute string knobs through `env.String`.
- `internal/statusline/command.go`, `internal/warn/warn.go` (modify) — kill-switch check.
- `internal/rhythm/rhythm.go` (modify) — extract `Detect` decision from `Suggestion`.
- `main.go` (modify) — `config` command dispatch; `init` seeds config.
- `internal/status/conformance_test.go`, `internal/statusline/conformance_test.go` (modify) — isolate `CCPOOL_CONFIG`.

---

### Task 1: Config file path

**Files:**
- Modify: `internal/paths/paths.go`
- Test: `internal/paths/paths_test.go` (create)

**Interfaces:**
- Produces: `paths.Config() string` — `CCPOOL_CONFIG` or `~/.ccpool/ccpool.json`, `~`-expanded.

- [ ] **Step 1: Write the failing test**

Create `internal/paths/paths_test.go`:

```go
package paths

import (
	"path/filepath"
	"testing"
)

func TestConfigHonoursEnv(t *testing.T) {
	t.Setenv("CCPOOL_CONFIG", "/tmp/x/ccpool.json")
	if got := Config(); got != "/tmp/x/ccpool.json" {
		t.Errorf("Config() = %q, want the env override", got)
	}
}

func TestConfigDefault(t *testing.T) {
	t.Setenv("CCPOOL_CONFIG", "")
	got := Config()
	if filepath.Base(got) != "ccpool.json" {
		t.Errorf("Config() = %q, want a ccpool.json default", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/paths/ -run TestConfig -v`
Expected: FAIL, `undefined: Config`.

- [ ] **Step 3: Add the path accessor**

In `internal/paths/paths.go`, after the `StatuslineLog` func, add:

```go
// Config is the ccpool config file (CCPOOL_CONFIG || ~/.ccpool/ccpool.json). The one file a user's
// persisted choices live in; read fresh per process so the hermetic test env is honoured.
func Config() string {
	return resolve("CCPOOL_CONFIG", "~/.ccpool/ccpool.json")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `unset GOROOT && go test ./internal/paths/ -run TestConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat(paths): add ccpool.json config path (CCPOOL_CONFIG override)"
```

---

### Task 2: Config schema, fail-open load, Enabled, key lookup

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `paths.Config()`, `github.com/SeanLF/ccpool/internal/rb` (only if needed — prefer stdlib json).
- Produces:
  - `type Config struct { ... }` (pointer fields, JSON-tagged).
  - `func Load() (*Config, error)` — always returns a non-nil `*Config` (empty on any error), plus the error for on-demand callers; hot-path callers ignore the error.
  - `func (c *Config) Enabled() bool` — file `enabled` (nil -> true).
  - `func (c *Config) Lookup(envKey string) (string, bool)` — the friendly-field value in env-string form, present only when set.

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ccpool.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", p)
}

func TestLoadLookup(t *testing.T) {
	write(t, `{"pace":{"profile":"weekdays","floor":0.2,"weights":[1,1,0.3]},"clock":"12","history":{"keep_days":7}}`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := map[string]string{
		"CCPOOL_PACE_PROFILE": "weekdays",
		"CCPOOL_PACE_FLOOR":   "0.2",
		"CCPOOL_PACE_WEIGHTS": "1,1,0.3", // array joined to CSV
		"CCPOOL_CLOCK":        "12",
		"CCPOOL_HISTORY_KEEP_DAYS": "7",
	}
	for k, want := range cases {
		if got, ok := c.Lookup(k); !ok || got != want {
			t.Errorf("Lookup(%q) = (%q,%v), want (%q,true)", k, got, ok, want)
		}
	}
	if _, ok := c.Lookup("CCPOOL_COLOR"); ok {
		t.Error("absent CCPOOL_COLOR should not be present")
	}
}

func TestEnabledDefaultsTrue(t *testing.T) {
	write(t, `{"pace":{"profile":"even"}}`) // no "enabled" key
	c, _ := Load()
	if !c.Enabled() {
		t.Error("absent enabled must default to true")
	}
	write(t, `{"enabled":false}`)
	c, _ = Load()
	if c.Enabled() {
		t.Error("enabled:false must disable")
	}
}

func TestLoadFailOpen(t *testing.T) {
	// missing file -> empty config, no error surfaced as fatal for the hot path
	t.Setenv("CCPOOL_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	c, err := Load()
	if err != nil {
		t.Errorf("missing file must not error, got %v", err)
	}
	if _, ok := c.Lookup("CCPOOL_CLOCK"); ok {
		t.Error("empty config must have nothing present")
	}
	if !c.Enabled() {
		t.Error("empty config must be enabled")
	}
	// corrupt file -> empty usable config + a surfaced error (for on-demand loud reporting)
	write(t, `{ not valid json`)
	c, err = Load()
	if err == nil {
		t.Error("corrupt file must surface an error for on-demand callers")
	}
	if c == nil || c.Enabled() != true {
		t.Error("corrupt file must still yield a usable, enabled config for the hot path")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/config/ -v`
Expected: FAIL, package `config` does not exist.

- [ ] **Step 3: Write the config package**

Create `internal/config/config.go`:

```go
// Package config reads a user's persisted ccpool choices from ~/.ccpool/ccpool.json. It is the
// middle layer of env > file > default: internal/env consults it after os env, before the builtin
// default. Fail-open: a missing OR corrupt file yields an empty, usable Config (hot-path callers
// ignore the error), while on-demand callers (config show/init) surface it. Detection seeds it once
// off the hot path; see Detect. Data that must honour the on-disk json.Number contract stays in rb;
// this is user config, decoded with plain stdlib json into pointer fields so absent != zero.
package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/SeanLF/ccpool/internal/paths"
)

type Config struct {
	Enabled   *bool      `json:"enabled,omitempty"`
	Pace      *Pace      `json:"pace,omitempty"`
	Downshift *Downshift `json:"downshift,omitempty"`
	Clock     *string    `json:"clock,omitempty"`
	Colour    *string    `json:"colour,omitempty"`
	Tier      *string    `json:"tier,omitempty"`
	History   *History   `json:"history,omitempty"`
}

type Pace struct {
	Profile     *string   `json:"profile,omitempty"`
	WorkDays    *string   `json:"work_days,omitempty"`
	WakeHours   *string   `json:"wake_hours,omitempty"`
	Floor       *float64  `json:"floor,omitempty"`
	Weights     []float64 `json:"weights,omitempty"`
	HourWeights []float64 `json:"hour_weights,omitempty"`
}

type Downshift struct {
	Mode   *string `json:"mode,omitempty"`
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
}

type History struct {
	KeepDays    *float64 `json:"keep_days,omitempty"`
	MinInterval *int     `json:"min_interval,omitempty"`
}

// Load reads and decodes the config file. The returned *Config is ALWAYS non-nil and usable, even on
// error (empty config), so hot-path callers can ignore err and fail open; on-demand callers report it.
func Load() (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(paths.Config())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // absent is normal: zero-config default
		}
		return c, err // unreadable: surface for on-demand, empty c for hot path
	}
	if err := json.Unmarshal(b, c); err != nil {
		return &Config{}, err // corrupt: empty usable config + the error
	}
	return c, nil
}

// Enabled reports whether the hooks should run (default true; only an explicit enabled:false disables).
func (c *Config) Enabled() bool {
	return c.Enabled == nil || *c.Enabled // NOTE: field/method name clash resolved in Step 3b
}

// Lookup returns the file's value for an env key in string form (as if the env var were set), present
// only when the field is set. This lets internal/env flow file values through the same parse+validate
// path as env values.
func (c *Config) Lookup(envKey string) (string, bool) {
	switch envKey {
	case "CCPOOL_PACE_PROFILE":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.Profile }))
	case "CCPOOL_WORK_DAYS":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.WorkDays }))
	case "CCPOOL_WAKE_HOURS":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.WakeHours }))
	case "CCPOOL_PACE_FLOOR":
		return floatP(pick(c.Pace, func(p *Pace) *float64 { return p.Floor }))
	case "CCPOOL_PACE_WEIGHTS":
		if c.Pace != nil && c.Pace.Weights != nil {
			return csv(c.Pace.Weights), true
		}
	case "CCPOOL_PACE_HOUR_WEIGHTS":
		if c.Pace != nil && c.Pace.HourWeights != nil {
			return csv(c.Pace.HourWeights), true
		}
	case "CCPOOL_DOWNSHIFT":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Mode }))
	case "CCPOOL_DOWNSHIFT_MODEL":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Model }))
	case "CCPOOL_DOWNSHIFT_EFFORT":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Effort }))
	case "CCPOOL_CLOCK":
		return strP(c.Clock)
	case "CCPOOL_COLOR":
		return strP(c.Colour)
	case "USAGE_TIER":
		return strP(c.Tier)
	case "CCPOOL_HISTORY_KEEP_DAYS":
		return floatP(pick(c.History, func(h *History) *float64 { return h.KeepDays }))
	case "CCPOOL_HISTORY_MIN_INTERVAL":
		return intP(pick(c.History, func(h *History) *int { return h.MinInterval }))
	}
	return "", false
}

// --- small extractors ---

func pick[T, R any](group *T, f func(*T) *R) *R {
	if group == nil {
		return nil
	}
	return f(group)
}

func strP(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

func floatP(p *float64) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatFloat(*p, 'f', -1, 64), true
}

func intP(p *int) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.Itoa(*p), true
}

func csv(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}
```

- [ ] **Step 3b: Fix the field/method name clash**

`Config.Enabled` is both a field and a method — Go rejects that. Rename the field's method to `HooksEnabled`:

```go
// HooksEnabled reports whether the hooks should run (default true; only explicit enabled:false disables).
func (c *Config) HooksEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}
```

And in the test (`config_test.go`), change both `c.Enabled()` calls to `c.HooksEnabled()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `unset GOROOT && go test ./internal/config/ -v`
Expected: PASS (TestLoadLookup, TestEnabledDefaultsTrue, TestLoadFailOpen).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): JSON schema, fail-open load, HooksEnabled, key lookup"
```

---

### Task 3: env consults config; add String and provenance Resolve

**Files:**
- Modify: `internal/env/env.go`
- Test: `internal/env/env_test.go`

**Interfaces:**
- Consumes: `config.Load`, `(*config.Config).Lookup`.
- Produces:
  - `func String(key, def string) string` — os env > file > def.
  - `Int`/`Int64`/`Float` now also consult the file (between env and default).
  - `func Resolve(key, def string) (value, source string)` — source in `{"env","file","default"}`, for `config show`.

- [ ] **Step 1: Write the failing test**

Append to `internal/env/env_test.go`:

```go
func TestStringAndFileLayer(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ccpool.json")
	if err := os.WriteFile(p, []byte(`{"pace":{"profile":"weekdays"},"history":{"keep_days":7}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", p)

	// file supplies the value when env is unset
	os.Unsetenv("CCPOOL_PACE_PROFILE")
	if got := String("CCPOOL_PACE_PROFILE", "even"); got != "weekdays" {
		t.Errorf("String from file = %q, want weekdays", got)
	}
	// numeric knobs get the file layer for free
	if got := Float("CCPOOL_HISTORY_KEEP_DAYS", 30); got != 7 {
		t.Errorf("Float from file = %v, want 7", got)
	}
	// env still WINS over the file
	t.Setenv("CCPOOL_PACE_PROFILE", "workhours")
	if got := String("CCPOOL_PACE_PROFILE", "even"); got != "workhours" {
		t.Errorf("env must win over file, got %q", got)
	}
	// default when neither set
	if got := String("CCPOOL_NOPE", "d"); got != "d" {
		t.Errorf("default = %q, want d", got)
	}
}

func TestResolveProvenance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ccpool.json")
	os.WriteFile(p, []byte(`{"clock":"12"}`), 0o644)
	t.Setenv("CCPOOL_CONFIG", p)

	os.Unsetenv("CCPOOL_CLOCK")
	if v, s := Resolve("CCPOOL_CLOCK", "24"); v != "12" || s != "file" {
		t.Errorf("Resolve = (%q,%q), want (12,file)", v, s)
	}
	t.Setenv("CCPOOL_CLOCK", "24")
	if v, s := Resolve("CCPOOL_CLOCK", "24"); v != "24" || s != "env" {
		t.Errorf("Resolve = (%q,%q), want (24,env)", v, s)
	}
	if v, s := Resolve("CCPOOL_MISSING", "d"); v != "d" || s != "default" {
		t.Errorf("Resolve = (%q,%q), want (d,default)", v, s)
	}
}
```

Ensure `env_test.go` imports `os` and `path/filepath`.

- [ ] **Step 2: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/env/ -run 'TestStringAndFileLayer|TestResolveProvenance' -v`
Expected: FAIL, `undefined: String` / `undefined: Resolve`.

- [ ] **Step 3: Wire config into env**

In `internal/env/env.go`, add the import `"github.com/SeanLF/ccpool/internal/config"` and a file-lookup helper, then route the getters through it:

```go
// fileValue returns the config-file value for key (fail-open: any load error yields no value, so env
// falls through to its default rather than breaking the hot path).
func fileValue(key string) (string, bool) {
	c, err := config.Load()
	if err != nil {
		return "", false
	}
	return c.Lookup(key)
}

// String returns key resolved os env > config file > def.
func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	if v, ok := fileValue(key); ok {
		return v
	}
	return def
}

// Resolve returns the effective value AND which layer supplied it, for `config show`.
func Resolve(key, def string) (string, string) {
	if v, ok := os.LookupEnv(key); ok {
		return v, "env"
	}
	if v, ok := fileValue(key); ok {
		return v, "file"
	}
	return def, "default"
}
```

Then update `parseInt` and `Float` to consult the file between env and default. Replace `parseInt`:

```go
func parseInt(key string) (int64, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		v, ok = fileValue(key)
	}
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
```

And `Float`:

```go
func Float(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		v, ok = fileValue(key)
	}
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return f
}
```

(`Int`/`Int64` already call `parseInt`, so they inherit the file layer.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `unset GOROOT && go test ./internal/env/ -v`
Expected: PASS (existing env tests + the two new ones).

Guard against an import cycle: `config` imports `paths` (+ stdlib) only, `env` imports `config`; no cycle. Confirm with `unset GOROOT && go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/env/env.go internal/env/env_test.go
git commit -m "feat(env): consult config file (env > file > default); add String + Resolve"
```

---

### Task 4: Reroute the string knobs through env.String

**Files:**
- Modify: `internal/profile/profile.go`, `internal/clock/clock.go`, `internal/statusline/statusline.go`, `internal/run/run.go`, `internal/history/history.go`

**Interfaces:**
- Consumes: `env.String`.

Each edit swaps a direct `os.Getenv`/local getter for `env.String(<key>, <default>)` so the file layer applies. Exact sites:

- [ ] **Step 1: profile.go** — in `Load`, replace the five reads:
  - `getenv("CCPOOL_PACE_PROFILE", "even")` -> `env.String("CCPOOL_PACE_PROFILE", "even")`
  - `os.Getenv("CCPOOL_WORK_DAYS")` -> `env.String("CCPOOL_WORK_DAYS", "")`
  - `os.Getenv("CCPOOL_WAKE_HOURS")` -> `env.String("CCPOOL_WAKE_HOURS", "")`
  - `os.Getenv("CCPOOL_PACE_WEIGHTS")` -> `env.String("CCPOOL_PACE_WEIGHTS", "")`
  - `os.Getenv("CCPOOL_PACE_HOUR_WEIGHTS")` -> `env.String("CCPOOL_PACE_HOUR_WEIGHTS", "")`

  Add the `internal/env` import. Keep the local `getenv` only if still used elsewhere; otherwise remove it. `floorValue` already uses `env.Float` (A2) — no change.

- [ ] **Step 2: clock.go** — in `Mode`, replace `getenv("CCPOOL_CLOCK", "24")` -> `env.String("CCPOOL_CLOCK", "24")`. Add the import; drop the local `getenv` if now unused.

- [ ] **Step 3: statusline.go** — in `colorProfile`, replace `os.Getenv("CCPOOL_COLOR")` -> `env.String("CCPOOL_COLOR", "")`. (`env` is already imported.)

- [ ] **Step 4: run.go** — replace the `envS("CCPOOL_DOWNSHIFT", ...)`-style reads (mode/model/effort) with `env.String("CCPOOL_DOWNSHIFT", <def>)`, `env.String("CCPOOL_DOWNSHIFT_MODEL", <def>)`, `env.String("CCPOOL_DOWNSHIFT_EFFORT", <def>)` using the SAME defaults `envS` used. Delete the local `envS`. Confirm defaults against the current code before editing.

- [ ] **Step 5: history.go** — replace the `USAGE_TIER` read (`os.Getenv("USAGE_TIER")` with its `max_20x` default) with `env.String("USAGE_TIER", "max_20x")`. Add the import.

- [ ] **Step 6: Verify nothing regressed (conformance still green)**

Run: `unset GOROOT && TZ=UTC go test ./...`
Expected: PASS. The existing goldens set env, which still wins, so output is unchanged. If any statusline/status golden shifts, STOP — it means a default drifted; reconcile before continuing.

- [ ] **Step 7: Commit**

```bash
git add internal/profile/profile.go internal/clock/clock.go internal/statusline/statusline.go internal/run/run.go internal/history/history.go
git commit -m "refactor: route string config knobs through env.String (file layer)"
```

---

### Task 5: Kill-switch (hooks no-op when disabled)

**Files:**
- Modify: `internal/statusline/command.go`, `internal/warn/warn.go`
- Test: `internal/statusline/kill_test.go` (create)

**Interfaces:**
- Consumes: `config.Load`, `(*config.Config).HooksEnabled`.
- Produces: `func enabled() bool` in each hook package (or a shared `config.HooksEnabled()` free helper — see Step 3).

- [ ] **Step 1: Write the failing test**

Create `internal/statusline/kill_test.go`:

```go
package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKillSwitchNoOps(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "ccpool.json")
	os.WriteFile(cfg, []byte(`{"enabled":false}`), 0o644)
	t.Setenv("CCPOOL_CONFIG", cfg)

	payload := `{"rate_limits":{"seven_day":{"used_percentage":50,"resets_at":9999999999}}}`
	old := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString(payload)
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = old }()

	// Capture stdout
	oldOut := os.Stdout
	or, ow, _ := os.Pipe()
	os.Stdout = ow
	Command(1720000000, false)
	ow.Close()
	os.Stdout = oldOut
	buf := make([]byte, 4096)
	n, _ := or.Read(buf)
	if strings.TrimSpace(string(buf[:n])) != "" {
		t.Errorf("disabled statusline must print nothing, got %q", buf[:n])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/statusline/ -run TestKillSwitch -v`
Expected: FAIL (statusline still renders the wk line).

- [ ] **Step 3: Add a shared enabled helper**

In `internal/config/config.go`, add a free function so hook packages don't each reimplement it:

```go
// HooksEnabled resolves the kill-switch fail-open: CCPOOL_ENABLED env (escape hatch) > file enabled >
// true. A missing OR corrupt config never disables (an inability to read config must not silence the
// tool). Errors are swallowed here by design (hot path).
func HooksEnabled() bool {
	if v, ok := os.LookupEnv("CCPOOL_ENABLED"); ok {
		return v != "0" && !strings.EqualFold(v, "false")
	}
	c, _ := Load()
	return c.HooksEnabled()
}
```

- [ ] **Step 4: Guard the hooks**

In `internal/statusline/command.go`, at the very top of `Command` (inside the deferred recover, first line of the body):

```go
if !config.HooksEnabled() {
	return
}
```

In `internal/warn/warn.go`, at the top of `Hook` (after its recover):

```go
if !config.HooksEnabled() {
	return
}
```

Add the `internal/config` import to both.

- [ ] **Step 5: Run tests to verify they pass**

Run: `unset GOROOT && go test ./internal/statusline/ ./internal/warn/ -v`
Expected: PASS, including TestKillSwitchNoOps. Existing goldens: green (no config file in the harness => enabled). If the harness leaks the dev's real config, Task 6 fixes it — run Task 6 first if a golden flips here.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/statusline/command.go internal/warn/warn.go internal/statusline/kill_test.go
git commit -m "feat(config): enabled kill-switch no-ops the statusline + warn hooks"
```

---

### Task 6: Isolate CCPOOL_CONFIG in the conformance harness

**Files:**
- Modify: `internal/status/conformance_test.go` (`stageReadout`), `internal/statusline/conformance_test.go` (its stage function)

**Interfaces:** none (test-only).

- [ ] **Step 1: Point CCPOOL_CONFIG at a nonexistent temp path**

In each harness's staging function, alongside the existing `t.Setenv("CCPOOL_HISTORY", ...)` lines, add:

```go
t.Setenv("CCPOOL_CONFIG", filepath.Join(inputDir, "no-config.json")) // isolate: never read the dev's real ~/.ccpool/ccpool.json
```

Add `CCPOOL_CONFIG` to the `readoutEnvKeys` cleared-before-each-case slice in `internal/status/conformance_test.go` (and any equivalent in the statusline harness).

- [ ] **Step 2: Verify hermetic + green**

Run: `unset GOROOT && TZ=UTC go test ./internal/status/ ./internal/statusline/ -count=1`
Expected: PASS. Prove isolation by temporarily creating `~/.ccpool/ccpool.json` with `{"enabled":false}` locally and re-running: tests must STILL pass (config ignored). Delete that file afterward.

- [ ] **Step 3: Commit**

```bash
git add internal/status/conformance_test.go internal/statusline/conformance_test.go
git commit -m "test: isolate CCPOOL_CONFIG in conformance harness"
```

---

### Task 7: Extract rhythm.Detect (decision) from Suggestion (formatting)

**Files:**
- Modify: `internal/rhythm/rhythm.go`
- Test: `internal/rhythm/rhythm_test.go` (append)

**Interfaces:**
- Produces: `func Detect(r float64, hours [24]int, wdays [7]int) (profile, workDays, wakeHours string)` — the structured decision the display string is built from. `profile` is `even`/`weekdays`/`workhours`/`custom`; `workDays`/`wakeHours` are "" when not applicable.

- [ ] **Step 1: Read current Suggestion**

Read `internal/rhythm/rhythm.go` `Suggestion` (around line 227) to capture the exact R thresholds and window logic it uses to choose `even` vs a concrete window. The extraction must preserve those exact rules (Suggestion's output must not change).

- [ ] **Step 2: Write the failing test**

Append to `internal/rhythm/rhythm_test.go` (create if absent, `package rhythm`):

```go
func TestDetectLowRIsEven(t *testing.T) {
	var hours [24]int
	var wdays [7]int
	profile, wd, wh := Detect(0.0, hours, wdays) // R=0 -> no schedule
	if profile != "even" || wd != "" || wh != "" {
		t.Errorf("low R: got (%q,%q,%q), want (even,,)", profile, wd, wh)
	}
}
```

(Add a high-R case mirroring whatever concrete-window branch Suggestion has, using inputs that Step 1 shows produce a window.)

- [ ] **Step 3: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/rhythm/ -run TestDetect -v`
Expected: FAIL, `undefined: Detect`.

- [ ] **Step 4: Extract Detect and refactor Suggestion to use it**

Pull the decision logic out of `Suggestion` into `Detect` (returning the structured profile/workDays/wakeHours), and rewrite `Suggestion` to call `Detect` and format its result into the existing human string (byte-identical output). Show the concrete extracted code based on Step 1's reading.

- [ ] **Step 5: Run tests to verify they pass**

Run: `unset GOROOT && TZ=UTC go test ./internal/rhythm/ -v`
Expected: PASS, including the existing rhythm conformance (Suggestion output unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/rhythm/rhythm.go internal/rhythm/rhythm_test.go
git commit -m "refactor(rhythm): extract Detect decision from Suggestion formatting"
```

---

### Task 8: config.Merge + config.Write (pure, stays in the low-level package)

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/detect_test.go` (create)

**Interfaces:**
- Produces (both pure `Config` ops with NO rhythm/clock import, so they stay in `internal/config`):
  - `func Merge(base, add *Config) *Config` — fill only base's nil fields from add (never clobber).
  - `func Write(path string, c *Config) error` — atomic marshal+write (temp + rename), 0644.
- (Detection — `configcmd.Detect` — is Task 9, in `internal/configcmd`, to avoid the cycle.)

- [ ] **Step 1: Write the failing test**

Create `internal/config/detect_test.go`:

```go
package config

import "testing"

func TestMergeFillsMissingOnly(t *testing.T) {
	weekdays := "weekdays"
	even := "even"
	base := &Config{Pace: &Pace{Profile: &weekdays}}     // user already set profile
	add := &Config{Pace: &Pace{Profile: &even}, Clock: ptr("24")}
	out := Merge(base, add)
	if *out.Pace.Profile != "weekdays" {
		t.Error("Merge must NOT overwrite an existing value")
	}
	if out.Clock == nil || *out.Clock != "24" {
		t.Error("Merge must fill a missing value from add")
	}
}

func ptr(s string) *string { return &s }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `unset GOROOT && go test ./internal/config/ -run TestMerge -v`
Expected: FAIL, `undefined: Merge`.

- [ ] **Step 3: Implement Merge and Write**

Add to `internal/config/config.go`:

```go
// Merge returns base with each NIL field filled from add -- never overwriting a value base already
// has (fill-missing-only, so re-seeding can't clobber a user's edits).
func Merge(base, add *Config) *Config {
	if base == nil {
		base = &Config{}
	}
	if add == nil {
		return base
	}
	if base.Enabled == nil {
		base.Enabled = add.Enabled
	}
	if base.Clock == nil {
		base.Clock = add.Clock
	}
	if base.Colour == nil {
		base.Colour = add.Colour
	}
	if base.Tier == nil {
		base.Tier = add.Tier
	}
	base.Pace = mergePace(base.Pace, add.Pace)
	base.Downshift = mergeDownshift(base.Downshift, add.Downshift)
	base.History = mergeHistory(base.History, add.History)
	return base
}

func mergePace(b, a *Pace) *Pace {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Profile == nil {
		b.Profile = a.Profile
	}
	if b.WorkDays == nil {
		b.WorkDays = a.WorkDays
	}
	if b.WakeHours == nil {
		b.WakeHours = a.WakeHours
	}
	if b.Floor == nil {
		b.Floor = a.Floor
	}
	if b.Weights == nil {
		b.Weights = a.Weights
	}
	if b.HourWeights == nil {
		b.HourWeights = a.HourWeights
	}
	return b
}

func mergeDownshift(b, a *Downshift) *Downshift {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Mode == nil {
		b.Mode = a.Mode
	}
	if b.Model == nil {
		b.Model = a.Model
	}
	if b.Effort == nil {
		b.Effort = a.Effort
	}
	return b
}

func mergeHistory(b, a *History) *History {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.KeepDays == nil {
		b.KeepDays = a.KeepDays
	}
	if b.MinInterval == nil {
		b.MinInterval = a.MinInterval
	}
	return b
}

// Write marshals c (indented) to path atomically (temp + rename).
func Write(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `unset GOROOT && go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/detect_test.go
git commit -m "feat(config): Merge (fill-missing) and atomic Write"
```

---

### Task 9: internal/configcmd (Detect + Show + Init) + main dispatch + init seeds config

**Files:**
- Create: `internal/configcmd/configcmd.go` (imports `config`, `rhythm`, `clock`, `env` — NOT imported by `env`, so no cycle)
- Modify: `main.go`
- Test: `main_test.go` (add `.txtar` script), `internal/configcmd/configcmd_test.go`

**Interfaces:**
- Consumes: `config.{Load,Merge,Write}`, `env.Resolve`, `rhythm.Detect`, `clock.Mode`.
- Produces:
  - `func Detect(now int64) *config.Config` — pace via `rhythm.Detect` on the reconstructed histogram (reuse/export rhythm's histogram builder); clock via `clock.Mode()` resolved to a concrete "12"/"24"; everything else nil (plain defaults).
  - `func Show(now int64) (lines []string, code int)` — for each in-scope env key, `env.Resolve(key, def)` -> `key value (source)`.
  - `func Init(args []string, now int64) (lines []string, code int)` — dry-run unless `--apply`; `--force` re-detects + overwrites (skips Merge).

- [ ] **Step 1: Write the failing e2e**

Create `testdata/script/config.txtar`:

```
env CCPOOL_CONFIG=$WORK/ccpool.json
env CCPOOL_HISTORY=$WORK/h.jsonl
env CCPOOL_PROJECTS=$WORK/projects

# config init is a DRY RUN by default: prints a plan, writes nothing
exec ccpool config init
stdout 'DRY RUN'
! exists $WORK/ccpool.json

# --apply writes the file
exec ccpool config init --apply
exists $WORK/ccpool.json

# config show reports effective values + a source column
exec ccpool config show
stdout 'enabled'
stdout 'source'
```

- [ ] **Step 2: Run to verify it fails**

Run: `unset GOROOT && go test . -run TestCLIScripts/config -v`
Expected: FAIL, `unknown command "config"`.

- [ ] **Step 3: Implement configcmd.go**

Create `internal/configcmd/configcmd.go`:
- `Detect(now)` — build the histogram the way `rhythm.Report` does (read `rhythm`'s exported histogram builder; if it is unexported, export a `rhythm.Histogram(now) (r float64, hours [24]int, wdays [7]int)` in a small Task 7 addendum), call `rhythm.Detect`, and set `Pace.Profile`/`WorkDays`/`WakeHours` from its result; set `Clock` from `strconv.Itoa(clock.Mode())` (resolves `auto` once); leave all else nil.
- `Show(now)` — iterate an ordered `[]struct{key, def string}` of the in-scope env keys, call `env.Resolve(key, def)`, format `printf("%-26s %-12s (%s)", friendlyName, value, source)`. Include an `enabled` row (via `config.HooksEnabled()` -> source is env/file/default; resolve manually).
- `Init(args, now)` — `detected := Detect(now)`; `existing, err := config.Load()` (surface err LOUD, exit 2, on corrupt); `--force` -> `final := detected` else `final := config.Merge(existing, detected)`; if NOT `--apply` -> print a "DRY RUN" plan (the JSON it would write) and return `code 0` without writing; if `--apply` -> `config.Write(paths.Config(), final)`.

Show the concrete Detect/Show/Init code at execution, reading `rhythm`'s current histogram/Detect signatures first.

- [ ] **Step 4: Wire main.go**

In `main.go` `dispatch`, add `case "config":` routing `args[1]` to `configcmd.Show`/`configcmd.Init` (unknown subcommand -> stderr + exit 2). In the `init` case, after the existing hook flow, call `configcmd.Init(args, now)` and print its lines so `ccpool init` / `ccpool init --apply` also seed (idempotent via Merge). Update `usage()` to list `config show` / `config init`. Import `internal/configcmd`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `unset GOROOT && go test . ./internal/configcmd/ -v`
Expected: PASS (the config.txtar script + configcmd unit tests). Confirm no import cycle: `unset GOROOT && go build ./...`.

- [ ] **Step 6: Full gate + real-run check**

Run: `unset GOROOT && make check`
Then verify on the real machine: `./ccpool config init` (dry-run shows a detected plan), `./ccpool config init --apply`, `./ccpool config show` (values + sources), `cat ~/.ccpool/ccpool.json`. Confirm `ccpool statusline`/`check` still render with the file present. Delete or keep the file as desired.

- [ ] **Step 7: Commit**

```bash
git add internal/configcmd/ main.go main_test.go testdata/script/config.txtar
git commit -m "feat(config): config show/init commands, main dispatch, init seeds config"
```

---

## Docs (fold into the final task's commit or a follow-up)

- [ ] Update `README.md` to document the config file (the in-scope settings, `env > file > default`, `enabled`, `ccpool config show`/`init`). Update `docs/CONFIG-AUDIT.md`'s headline to note bucket-2 choices now persist to `ccpool.json`. These are the ONE place the JSON's no-comments gap is compensated.

## Self-review notes (verified against the spec)

- Resolution `env > file > default`: Tasks 3 (env), 4 (reroutes). Env-wins proven by unchanged goldens (Task 4/6).
- Presence-aware (absent != zero): pointer fields (Task 2); `enabled` nil->true (Task 2, 5).
- Fail-open hot path / loud on-demand: `Load` returns usable config + error (Task 2); hooks ignore, `config show/init` surface (Tasks 5, 9).
- Kill-switch: Task 5. Dry-run/apply: Task 9. Detection off hot path (pace+clock only): Tasks 7 (rhythm.Detect) + 9 (configcmd.Detect). Conformance isolation: Task 6. Array<->CSV mapping: Task 2 (`csv`) + test.
- Not in scope (thresholds/paths stay env-only): no task touches them — correct.
- Import cycle avoided: `internal/config` is a leaf (no rhythm/clock/env); detection + commands live in `internal/configcmd` (main-only). Verified by the `go build ./...` checks in Tasks 3, 9.
