# SQLite storage (Sprint B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## STATUS (updated mid-sprint)

- **DONE + committed + gate-green:** Phase 1 (T1-T2), Phase 2 (T3-T5), Phase 3 (T6-T10, incl. the
  live import + parity proof), Phase 4 **T11**. 15 commits `6f1d1cd`..`755e84a`. Phase 3 is
  live-verified (57,055 rows imported; `check` byte-identical on history-derived lines).
- **REMAINING:** Phase 4 T12 (snapshot readers -> store; needs a snapshot conformance seeder first,
  like T7 needed the history one), T13 (kv), T14 (snapshot prune). Phase 5 (T15-T19).
- **DEVIATIONS from the task text below (locked, reasoned):** `cost` kept / `tier` dropped from
  history; **added T7b** (calib wkRuns = SQL GROUP BY + Go run-split); envelope `reset` is
  `interface{}` -> facade-normalized (sqlc can't type it), `DataAge` = `CAST(COALESCE(max,0))`;
  quick_check dropped from `Open` (hot-path perf); DSN via `url.URL`; ingest guard nulls the reset
  (not drop-row); the **seeders were pulled forward** (T15's `SeedHistoryJSONL` used by T7/prune);
  JSONL-byte goldens (history-seed, prune) retired for DB-outcome tests. Full context in the commit
  messages, `docs/DECISIONS.md` (Sprint B entries + follow-ups), and `scratch/next-session-brief.md`.
- **NOT rebuilt live yet:** the running statusline is the Phase-1 file-based binary; rebuild only
  after Phase 4 closes (re-import first). See the resume brief.

**Goal:** Replace ccpool's JSONL/JSON file storage (history + per-session snapshots + small state files) with one embedded SQLite database, dissolving the bespoke tail-dedup / glob reconcile / prune / `Burn.envelope` code into typed SQL, with byte-identical command output.

**Architecture:** A new `internal/store` package owns a `*sql.DB` (driver `modernc.org/sqlite`, pure-Go), an embedded `schema.sql`, sqlc-generated typed queries (`internal/store/db`), and a thin fail-open facade returning a typed 3-way read state (`OK`/`Corrupt`/`Transient`). History reconcile becomes a SQL window query; snapshot reconcile stays in Go over raw payload blobs. Shipped in 5 sequenced phases behind the facade, hot-path callers switched last.

**Tech Stack:** Go (single static binary), `modernc.org/sqlite` (no cgo), `sqlc` (build-time codegen), SQLite WAL. Full design + spike evidence: `docs/sqlite-storage-design.md`.

## Global Constraints

- **Gate (must be green before every commit):** `unset GOROOT && make check` (gofumpt + vet + staticcheck + govulncheck + `go test ./...`).
- **Go commands:** prefix with `unset GOROOT`; add `unset GOBIN` for `go install`.
- **Golden conformance:** on an *intentional* output change only, re-baseline with `CCPOOL_UPDATE_GOLDEN=1 TZ=UTC go test ./...` and review the diff. This migration targets **no** golden change (byte-identical is the proof); a golden shift is a regression to investigate.
- **Fail OPEN on the hot path:** `warn` and `statusline` must NEVER panic. Every hot-path facade call degrades to empty on any non-OK state. On-demand commands (`status`, `check`, `init`) fail LOUD and distinguish states.
- **Delegate every dollar to `ccusage`; never hand-roll pricing.** Untouched here.
- **No em dashes** in prose/docs/commits. Conventional commits, no emoji, explain *why*. End AI-assisted commits with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Commit gate:** the PreToolUse hook requires review + simplifier agents (pr-review-toolkit:code-reviewer + code-simplifier) before EACH commit; silent-failure-hunter for error-handling changes. Touch `/tmp/claude-pr-review-done-<session_id>` in a SEPARATE Bash call before any push/PR.
- **sqlc required tweaks (or codegen fails / emits `interface{}`):** qualify every column in the `latest` CTE (`history.t`, not `t`); `CAST(... AS REAL/INTEGER)` the window/aggregate/subquery result columns.
- **Driver DSN pragmas:** `journal_mode=WAL`, `busy_timeout=5000`, `synchronous=NORMAL`.
- **Location:** all ccpool-owned state under `~/.ccpool/` (`CCPOOL_HOME` override); DB at `CCPOOL_DB` else `$CCPOOL_HOME/ccpool.db` else `~/.ccpool/ccpool.db`. Tests set `CCPOOL_HOME`/`CCPOOL_DB` for hermeticity.

---

## Phase 1 - `~/.ccpool/` home-dir move (isolated, golden-neutral)

Ships as its own commit BEFORE any SQLite work. Rewrites `internal/paths` defaults to a single home dir; migrates the existing user's files; updates docs. No storage-format change.

### Task 1: Introduce `CCPOOL_HOME` and relocate ccpool-owned paths

**Files:**
- Modify: `internal/paths/paths.go` (all `~/.claude/...` defaults for ccpool-owned files)
- Test: `internal/paths/paths_test.go`

**Interfaces:**
- Produces: `paths.Home() string` (resolves `CCPOOL_HOME` else `~/.ccpool`); `paths.DB() string` (resolves `CCPOOL_DB` else `$Home/ccpool.db`). Existing resolvers (`History`, `Config`, `CalibCache`, `BlocksCache`, `StatuslineLog`) now default under `paths.Home()`, each keeping its own env override. `SnapshotFor`/`SnapshotGlob`/`SnapshotCache` stay under `~/.claude` for Phase 1 (moved/removed in Phase 4). `Projects()` stays `~/.claude/projects` (external, read-only).

- [ ] **Step 1: Write the failing test**

```go
// internal/paths/paths_test.go
func TestHomeResolution(t *testing.T) {
	t.Setenv("CCPOOL_HOME", "/tmp/ccpool-home-x")
	t.Setenv("CCPOOL_DB", "")
	if got := paths.Home(); got != "/tmp/ccpool-home-x" {
		t.Fatalf("Home() = %q, want /tmp/ccpool-home-x", got)
	}
	if got := paths.DB(); got != "/tmp/ccpool-home-x/ccpool.db" {
		t.Fatalf("DB() = %q, want .../ccpool.db", got)
	}
}

func TestHistoryDefaultsUnderHome(t *testing.T) {
	t.Setenv("CCPOOL_HOME", "/tmp/ccpool-home-y")
	t.Setenv("CCPOOL_HISTORY", "")
	if got := paths.History(); got != "/tmp/ccpool-home-y/rate-limit-history.jsonl" {
		t.Fatalf("History() = %q, want under home", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `unset GOROOT && go test ./internal/paths/ -run TestHome -v`
Expected: FAIL (`paths.Home`/`paths.DB` undefined).

- [ ] **Step 3: Implement `Home()` + `DB()`, re-point ccpool-owned defaults**

Add to `paths.go`:

```go
// Home is ccpool's own state dir. Only ccpool-owned files live here; reads of
// Claude Code's own data (projects transcripts) stay under ~/.claude.
func Home() string { return resolve("CCPOOL_HOME", "~/.ccpool") }

// DB is the SQLite database path.
func DB() string { return resolve("CCPOOL_DB", filepath.Join(Home(), "ccpool.db")) }
```

Change the defaults of `History()`, `Config()`, `CalibCache()`, `BlocksCache()`, `StatuslineLog()` from `~/.claude/<name>` to `filepath.Join(Home(), "<name>")` (keep each existing env override as the first-precedence value). Leave `SnapshotFor`/`SnapshotGlob`/`SnapshotCache`/`Projects` unchanged this phase.

- [ ] **Step 4: Run to verify it passes**

Run: `unset GOROOT && go test ./internal/paths/ -v`
Expected: PASS.

- [ ] **Step 5: Full gate**

Run: `unset GOROOT && make check`
Expected: all green.

- [ ] **Step 6: Commit** (touch the commit-force marker first only if trivial; this touches paths + tests so run the review agents)

```bash
git add internal/paths/
git commit -m "feat(paths): ccpool-owned state under ~/.ccpool (CCPOOL_HOME); DB path resolver"
```

### Task 2: One-time file migration + docs update

**Files:**
- Modify: `README.md` (the `~/.claude/ccpool.json` reference), `docs/config-file-design.md`, `docs/CONFIG-AUDIT.md`, `docs/config-file-plan.md` (occurrences of `~/.claude/ccpool.json` -> `~/.ccpool/ccpool.json`)
- Modify: `internal/initcmd/init.go` if it prints or seeds config at a `~/.claude` path
- Create: `scratch/migrate-home.sh` (one-off, gitignored, NOT shipped) that `mkdir -p ~/.ccpool` and `mv` the user's existing `ccpool.json`, `ccpool-calibration.json`, `ccpool-blocks-cache.json`, `statusline.log`, and `rate-limit-history.jsonl` from `~/.claude` to `~/.ccpool` (history moves as a file now; it becomes the DB in Phase 3).

- [ ] **Step 1: Update docs** - replace every `~/.claude/ccpool.json` with `~/.ccpool/ccpool.json`; grep to confirm none remain: `grep -rn "claude/ccpool.json" README.md docs/ || echo clean`.
- [ ] **Step 2: Write + run the migration script** against the live machine (idempotent `mv -n`); verify `ls ~/.ccpool/`.
- [ ] **Step 3: Run `unset GOROOT && go run . config show` and `go run . status`** - confirm they read from `~/.ccpool` with no errors.
- [ ] **Step 4: Full gate** `unset GOROOT && make check`.
- [ ] **Step 5: Commit**

```bash
git add README.md docs/
git commit -m "docs: point config path at ~/.ccpool after the home-dir move"
```

---

## Phase 2 - `internal/store` foundation (behind the facade, no callers switched)

### Task 3: Add deps, schema, queries, sqlc config, generate

**Files:**
- Create: `internal/store/schema.sql`, `internal/store/query.sql`, `sqlc.yaml`
- Create: `internal/store/db/` (sqlc output; generated, committed)
- Modify: `go.mod`/`go.sum` (add `modernc.org/sqlite`)
- Create: `internal/store/gen.go` (`//go:generate` directive documenting regen)

**Interfaces:**
- Produces: generated package `internal/store/db` with `New(*sql.DB) *Queries` and methods `AppendHistory`, `EnvelopeWeekly`, `EnvelopeFiveHour`, `PutSnapshot`, `Snapshots`, `GetKV`, `PutKV`, `PruneHistory`, `PruneSnapshots`, `DataAge`, `LastSessionRow`. Row structs: `EnvelopeWeeklyRow{T int64; Running float64; Reset sql.NullInt64}` (5h analogous), `SnapshotsRow{Session string; CapturedAt int64; Payload string}`.

- [ ] **Step 1: Write `schema.sql`** (verbatim from design):

```sql
CREATE TABLE IF NOT EXISTS history (
  id        INTEGER PRIMARY KEY,   -- rowid = arrival-order tie-break; prune deletes oldest only, never max, so rowid is never reused (envelope depends on this)
  t         INTEGER NOT NULL,
  wk        REAL    NOT NULL,
  wk_reset  INTEGER,
  ses       REAL,
  ses_reset INTEGER,
  tier      TEXT    NOT NULL,
  cost      REAL,
  session   TEXT
);
CREATE INDEX IF NOT EXISTS history_t ON history(t);
CREATE TABLE IF NOT EXISTS snapshots (
  session     TEXT    PRIMARY KEY,
  captured_at INTEGER NOT NULL,
  payload     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_captured ON snapshots(captured_at);
CREATE TABLE IF NOT EXISTS kv (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
```

- [ ] **Step 2: Write `query.sql`** with sqlc annotations. The two envelope queries (weekly shown; 5h identical on `ses`/`ses_reset`) apply the required tweaks (qualified `latest` CTE, `CAST` on computed columns, `reset` left un-CAST so it stays `sql.NullInt64`):

```sql
-- name: AppendHistory :exec
INSERT INTO history (t, wk, wk_reset, ses, ses_reset, tier, cost, session)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: LastSessionRow :one
SELECT * FROM history
WHERE (sqlc.narg('session') IS NULL OR session = sqlc.narg('session'))
ORDER BY id DESC LIMIT 1;

-- name: EnvelopeWeekly :many
WITH latest AS (
  SELECT max(history.wk_reset) AS r FROM history
  WHERE history.wk IS NOT NULL AND history.wk_reset IS NOT NULL AND history.t >= @cutoff
),
kept AS (
  SELECT h.t, h.wk AS f, h.id FROM history h, latest
  WHERE h.t >= @cutoff AND h.wk IS NOT NULL
    AND CASE WHEN latest.r IS NOT NULL
             THEN h.wk_reset IS NOT NULL AND latest.r - h.wk_reset <= 300
             ELSE h.wk_reset IS NULL END
)
SELECT kept.t,
       CAST(max(kept.f) OVER (ORDER BY kept.t, kept.id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS REAL) AS running,
       (SELECT r FROM latest) AS reset
FROM kept ORDER BY kept.t, kept.id;

-- name: EnvelopeFiveHour :many
WITH latest AS (
  SELECT max(history.ses_reset) AS r FROM history
  WHERE history.ses IS NOT NULL AND history.ses_reset IS NOT NULL AND history.t >= @cutoff
),
kept AS (
  SELECT h.t, h.ses AS f, h.id FROM history h, latest
  WHERE h.t >= @cutoff AND h.ses IS NOT NULL
    AND CASE WHEN latest.r IS NOT NULL
             THEN h.ses_reset IS NOT NULL AND latest.r - h.ses_reset <= 300
             ELSE h.ses_reset IS NULL END
)
SELECT kept.t,
       CAST(max(kept.f) OVER (ORDER BY kept.t, kept.id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS REAL) AS running,
       (SELECT r FROM latest) AS reset
FROM kept ORDER BY kept.t, kept.id;

-- name: PutSnapshot :exec
INSERT INTO snapshots (session, captured_at, payload) VALUES (?, ?, ?)
ON CONFLICT(session) DO UPDATE SET captured_at = excluded.captured_at, payload = excluded.payload;

-- name: Snapshots :many
SELECT session, captured_at, payload FROM snapshots;

-- name: DataAge :one
SELECT CAST(max(captured_at) AS INTEGER) AS newest FROM snapshots;

-- name: GetKV :one
SELECT value FROM kv WHERE key = ?;

-- name: PutKV :exec
INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: PruneHistory :execrows
DELETE FROM history WHERE t < ?;

-- name: PruneSnapshots :execrows
DELETE FROM snapshots WHERE captured_at < ?;
```

- [ ] **Step 3: Write `sqlc.yaml`** (verbatim from design; `version: "2"`, engine `sqlite`, `sql_package: database/sql`, `out: internal/store/db`, `emit_empty_slices: true`).
- [ ] **Step 4: Add the driver** `unset GOROOT && go get modernc.org/sqlite@latest`.
- [ ] **Step 5: Generate** `unset GOROOT && sqlc generate`. Expected: exit 0, `internal/store/db/*.go` created.
- [ ] **Step 6: Inspect generated types** - confirm `EnvelopeWeeklyRow.Running` is `float64`, `.Reset` is `sql.NullInt64`, `DataAge` returns `int64`. If any is `interface{}`, the CAST/qualify tweak was missed; fix `query.sql` and regenerate.
- [ ] **Step 7: Gate + commit**

```bash
unset GOROOT && make check
git add internal/store/ sqlc.yaml go.mod go.sum
git commit -m "feat(store): sqlite schema, queries, sqlc codegen (modernc driver, no cgo)"
```

### Task 4: `store.Open` - path resolution, pragmas, typed states, corruption self-heal

**Files:**
- Create: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces:**
- Produces:
  ```go
  type ReadState int
  const ( StateOK ReadState = iota; StateCorrupt; StateTransient )
  type Store struct { q *db.Queries; sqlDB *sql.DB }
  func Open() (*Store, ReadState)   // creates dir+DB if absent; self-heals a corrupt file
  func (s *Store) Close() error
  func classify(err error) ReadState // maps SQLITE_CORRUPT/NOTADB->Corrupt, SQLITE_BUSY/IO->Transient, nil->OK
  ```
- Consumes: `paths.DB()`, `internal/store/db`.

- [ ] **Step 1: Write failing tests**

```go
// internal/store/store_test.go
func TestOpenCreatesFreshDB(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil { t.Fatalf("Open fresh = %v", st) }
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "ccpool.db")); err != nil {
		t.Fatalf("db not created: %v", err)
	}
}

func TestOpenSelfHealsCorruptDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)
	os.WriteFile(dbPath, []byte("this is not a sqlite file"), 0o644)
	s, st := store.Open()
	if st != store.StateOK || s == nil { t.Fatalf("Open corrupt should self-heal to OK, got %v", st) }
	defer s.Close()
	// corrupt file quarantined aside
	matches, _ := filepath.Glob(dbPath + ".corrupt-*")
	if len(matches) == 0 { t.Fatal("expected corrupt file quarantined aside") }
}
```

- [ ] **Step 2: Run to verify fail** `unset GOROOT && go test ./internal/store/ -run TestOpen -v` -> FAIL.
- [ ] **Step 3: Implement `Open`**

```go
func Open() (*Store, ReadState) {
	path := paths.DB()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, StateTransient
	}
	s, st := openAt(path)
	if st == StateCorrupt {
		quarantine(path) // rename path + -wal + -shm aside as .corrupt-<pid>
		s, st = openAt(path) // recreate empty
	}
	return s, st
}

func openAt(path string) (*Store, ReadState) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil { return nil, classify(err) }
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec(schemaSQL); err != nil { // embedded schema.sql, idempotent
		sqlDB.Close(); return nil, classify(err)
	}
	if _, err := sqlDB.Exec(`PRAGMA quick_check`); err != nil {
		sqlDB.Close(); return nil, classify(err)
	}
	return &Store{q: db.New(sqlDB), sqlDB: sqlDB}, StateOK
}
```

Embed the schema with `//go:embed schema.sql`. `classify` inspects the error string / `modernc` error codes for `SQLITE_CORRUPT`/`SQLITE_NOTADB` -> `StateCorrupt`, `SQLITE_BUSY`/IO -> `StateTransient`, else `StateOK` on nil. Use comma-ok assertions; never panic.

- [ ] **Step 4: Run to verify pass** `unset GOROOT && go test ./internal/store/ -run TestOpen -v` -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): Open with WAL pragmas, typed read-state, corrupt self-heal quarantine"
```

### Task 5: Facade read/write methods with typed states + round-trip tests

**Files:**
- Modify: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces:**
- Produces (all return `ReadState` where reads can distinguish transient/corrupt):
  ```go
  type HistoryRow struct { T int64; Wk float64; WkReset *int64; Ses *float64; SesReset *int64; Tier string; Cost *float64; Session *string }
  type EnvRow struct { T int64; Value float64; Reset sql.NullInt64 }
  func (s *Store) AppendHistory(r HistoryRow) error
  func (s *Store) LastSessionRow(sid *string) (map[string]any, ReadState) // shape matches rb.ParseObject output for skip()
  func (s *Store) EnvelopeWeekly(now int64) ([]EnvRow, ReadState)
  func (s *Store) EnvelopeFiveHour(now int64) ([]EnvRow, ReadState)
  func (s *Store) PutSnapshot(session string, capturedAt int64, payload []byte) error
  func (s *Store) CaptureAndAppend(session string, capturedAt int64, payload []byte, r HistoryRow) error // ONE txn
  func (s *Store) Snapshots() ([]map[string]any, ReadState) // each = rb.ParseObject(payload) + captured_at spliced
  func (s *Store) DataAge(now int64) (age int64, ok bool, st ReadState)
  func (s *Store) GetKV(key string) ([]byte, bool, ReadState)
  func (s *Store) PutKV(key string, value []byte) error
  func (s *Store) PruneHistory(cutoff int64) (int64, error)
  func (s *Store) PruneSnapshots(cutoff int64) (int64, error)
  ```
- Consumes: `internal/rb` (payload parse), `internal/store/db`.

- [ ] **Step 1: Write failing round-trip tests** - append 3 history rows, assert `EnvelopeWeekly` running-max; `PutSnapshot` then `Snapshots` returns the parsed payload with `captured_at`; `PutKV`/`GetKV` round-trips; `CaptureAndAppend` writes both tables atomically.

```go
func TestEnvelopeRunningMax(t *testing.T) {
	s := freshStore(t) // helper: Open with CCPOOL_DB in t.TempDir()
	now := int64(1_800_000_000); reset := now + 3*86400
	for _, wk := range []float64{25, 40, 30} { // running max should be 25,40,40
		s.AppendHistory(store.HistoryRow{T: now, Wk: wk, WkReset: &reset, Tier: "max_20x"})
		now += 60
	}
	rows, st := s.EnvelopeWeekly(now)
	if st != store.StateOK { t.Fatalf("state %v", st) }
	got := []float64{}; for _, r := range rows { got = append(got, r.Value) }
	if !reflect.DeepEqual(got, []float64{25, 40, 40}) { t.Fatalf("running max = %v", got) }
}
```

- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Implement each method** wrapping the generated queries; map DB errors via `classify`; convert `db.EnvelopeWeeklyRow` -> `EnvRow`; `Snapshots` parses `payload` via `rb.ParseObject` and splices `captured_at`; `CaptureAndAppend` opens a `sql.Tx`, runs `PutSnapshot` + `AppendHistory` on `s.q.WithTx(tx)`, commits.
- [ ] **Step 4: Run to verify pass** `unset GOROOT && go test ./internal/store/ -v` -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/store/
git commit -m "feat(store): facade read/write methods with typed states + atomic capture-append"
```

---

## Phase 3 - History cutover (append, envelope, prune, importer, guard)

### Task 6: Route history append through the store

**Files:**
- Modify: `internal/history/history.go` (`Seed` writes via `store.AppendHistory`; keep `skip()` policy; replace `lastSessionRow` file-tail scan with `store.LastSessionRow`)
- Modify: `internal/history/history_test.go`

**Interfaces:**
- Consumes: `store.Open`, `store.AppendHistory`, `store.LastSessionRow`, `store.HistoryRow`.
- Preserves: `skip(last map[string]any, r row, now int64) bool` unchanged (dedup identical wk/wk_reset; 60s throttle when only ses moved).

**Note (latent fragility, fix here):** `burn.Envelope` and `calib.wkRuns` trust `wk_reset`/`ses_reset` with no sanity bound, unlike `pool.GetWindow` which already skips `reset > now+maxAhead`. A single absurd reset (e.g. the `9999999999` sentinels found in live data on 2026-07-11) poisons BOTH burn (envelope `latest = max(reset)` collapses the window) AND calibration (`wkRuns` delta-weight skews, inflating `$/1%` ~4.4x -> the statusline `$` was 4.4x too high). The importer skips sentinels, but LIVE ingest does not validate. **Add a cheap guard in `Seed`: reject a row whose `wk_reset`/`ses_reset` is beyond `now + 30*86400` (or below `now`), so newly-arriving bad data cannot recur the bug.** Cover it with a unit test (a row with `wk_reset = now + 400d` is not written).

- [ ] **Step 1: Update the existing `Seed` throttle/dedup tests** to run against a temp DB (set `CCPOOL_DB`), asserting the same skip/append outcomes as today (identical wk+wk_reset+ses -> no new row; only-ses-moved within 60s -> no new row; wk moved -> new row).
- [ ] **Step 2: Run to verify fail** (`Seed` still writes JSONL) -> FAIL.
- [ ] **Step 3: Rewrite `Seed`** - build the row as today; `s, st := store.Open()`; on non-OK return best-effort nil (fail-open, no history is not fatal); `last, _ := s.LastSessionRow(sid)`; `if skip(last, r, now) { return nil }`; `return s.AppendHistory(toHistoryRow(r))`. Drop `flock`, `marshalRow`, the 64KB tail scan.
- [ ] **Step 4: Run to verify pass** -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/history/
git commit -m "feat(history): append via store; skip() throttle preserved, flock/tail-scan retired"
```

### Task 7: Route the weekly/5h envelope through the store

**Files:**
- Modify: `internal/burn/burn.go` (add `WeeklyEnvelope`/`FiveHourEnvelope` helpers that read `store` `EnvRow`s and adapt to `[]Entry` for `Project`/`ProjectRecent`; keep `Project`, `ProjectRecent`, `currentRun` unchanged)
- Modify: `internal/status/check.go` (call the new store-backed envelope instead of `burn.Read`+`burn.Envelope`)
- Modify: `internal/burn/burn_test.go`

**Interfaces:**
- Produces: `func WeeklyEnvelope(s *store.Store, now int64) (entries []Entry, readable bool)` and `FiveHourEnvelope(...)`. `readable=false` maps from `StateCorrupt` (unreadable), `true` from `StateOK` (empty slice = warm-up). `StateTransient` -> `readable=false` at the command layer message "unknown, retry" (see Task 10 for the message wiring).
- Adapter: `EnvRow{T,Value,Reset}` -> `Entry{"t":T, field:Value, resetField:Reset.Int64 if Valid}`.

- [ ] **Step 1: Write a golden-shaped test** - seed a temp DB with the `conformance/check_fixtures.json` history rows in arrival order, call `WeeklyEnvelope`, assert the resulting `Project` output equals the committed golden value (proves parity with the old `Read`+`Envelope`).
- [ ] **Step 2: Run to verify fail** -> FAIL (`WeeklyEnvelope` undefined).
- [ ] **Step 3: Implement the adapter + helpers**; wire `check.go` to use them. `check` maps `!readable` to the existing "history unreadable -- projection unavailable" line; empty-but-readable to the normal empty path.
- [ ] **Step 4: Run to verify pass** + `unset GOROOT && go test ./internal/burn/ ./internal/status/ -v` -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/burn/ internal/status/
git commit -m "feat(burn): weekly/5h envelope via store window query; Project unchanged"
```

### Task 8: Route history prune through the store

**Files:**
- Modify: `internal/initcmd/prune.go` (`PruneHistory` -> `store.PruneHistory(cutoff)`)
- Modify: `internal/initcmd/prune_test.go`

- [ ] **Step 1: Update prune tests** to seed a temp DB, prune with `keepDays`, assert only rows with `t >= cutoff` remain and the returned count matches.
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Reimplement `PruneHistory`** as `cutoff := now - int64(keepDays*86400); n, err := s.PruneHistory(cutoff)`. Drop the write-then-truncate + flock file logic.
- [ ] **Step 4: Run to verify pass** -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/initcmd/prune.go internal/initcmd/prune_test.go
git commit -m "feat(prune): history prune via store DELETE; file rewrite retired"
```

### Task 9: One-off importer (NOT shipped)

**Files:**
- Create: `scratch/import-history.go` (`//go:build ignore`; gitignored via `scratch/`)

- [ ] **Step 1: Write the importer** - open the DB via the same DSN; read `~/.ccpool/rate-limit-history.jsonl` (post-Phase-1 location) line by line in file order; skip rows where `wk_reset == 9999999999` or `session == "bench"`; `INSERT INTO history (...)` preserving order (so `id` ascends with arrival). Print counts.
- [ ] **Step 2: Run it** `unset GOROOT && go run scratch/import-history.go`; verify `sqlite3 ~/.ccpool/ccpool.db "SELECT count(*) FROM history"` matches the JSONL line count minus sentinels.
- [ ] **Step 3: Verify parity** - `unset GOROOT && go run . check` output equals the pre-migration `check` output (weekly burn/runway present, same numbers). This is the real proof the importer + envelope agree with the retired path.
- [ ] **Step 4: No commit of the script content** (it lives in gitignored `scratch/`); note completion in the commit message of the next task.

### Task 10: Silent-data-loss cutover guard + transient-state messaging

**Files:**
- Modify: `internal/status/check.go`, `internal/status/status.go` (loud one-time warning if a legacy JSONL exists and `history` is empty; map `StateTransient` to "unknown, retry", not "corrupt")

**Interfaces:**
- Consumes: `store` read states; `paths.History()` (legacy JSONL location).

- [ ] **Step 1: Write the test** - a temp DB with empty history + a non-empty legacy `rate-limit-history.jsonl` present -> `check` prints the "history not imported, run the importer" warning; with the JSONL absent -> normal empty/warm-up path (no false alarm).
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Implement the guard** in the command layer (not the facade). Also thread `StateTransient` to the "unknown, retry" copy distinct from `StateCorrupt`'s "unreadable".
- [ ] **Step 4: Run to verify pass** -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/status/
git commit -m "feat(status): cutover guard for un-imported history; distinguish transient from corrupt"
```

---

## Phase 4 - Snapshots + kv cutover

### Task 11: Snapshot capture via store (single txn with history append)

**Files:**
- Modify: `internal/statusline/command.go` (`capture` -> `store.CaptureAndAppend`; remove tmp+rename snapshot write)
- Modify: `internal/statusline/command_test.go`

**Interfaces:**
- Consumes: `store.CaptureAndAppend(session, capturedAt, payload, HistoryRow)`.
- Note: capture currently splices `captured_at` into the payload (`spliceCapturedAt`); keep that splice so `Snapshots()` re-parse is byte-identical, and pass `captured_at` as the column too.

- [ ] **Step 1: Update capture tests** to assert a snapshot row + paired history row exist after a render (temp DB), and that a reader never sees a snapshot without its history row (atomicity).
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Rewrite `capture`** to build the payload (with spliced `captured_at`) + the `HistoryRow`, then one `store.CaptureAndAppend`. Fail-open: any non-OK Open or error -> silent return (hot path). Keep the top-level `recover`.
- [ ] **Step 4: Run to verify pass** -> PASS. Then `make build && ./ccpool statusline` renders without error.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check && make build
git add internal/statusline/
git commit -m "feat(statusline): capture snapshot+history in one store txn; tmp-rename retired"
```

### Task 12: Snapshot reconcile + data-age read from the store

**Files:**
- Modify: `internal/pool/pool.go` (`LoadSnapshots` -> `store.Snapshots`; `DataAge` -> `store.DataAge` or compute over the returned rows). `GetWindow` logic UNCHANGED.
- Modify: `internal/pool/pool_test.go`

**Interfaces:**
- Consumes: `store.Snapshots() ([]map[string]any, ReadState)`, `store.DataAge`.
- Preserves: `GetWindow`, `Weekly`, `FiveHour`, the 300s jitter bucket, the used% clamp + leak guard - all byte-identical.

- [ ] **Step 1: Update pool tests** to seed snapshot rows in a temp DB; assert `GetWindow`/`Weekly`/`FiveHour`/`DataAge` return the same values as the file-based fixtures did.
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Reimplement `LoadSnapshots`** to return `store.Snapshots()` parsed maps; keep `GetWindow` reading those maps unchanged. `absentOrCorrupt` (in `status`) maps: 0 rows -> warm-up, rows-present-but-none-parse -> corruption (as today).
- [ ] **Step 4: Run to verify pass** + `unset GOROOT && go test ./internal/pool/ ./internal/status/ -v` -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/pool/ internal/status/
git commit -m "feat(pool): snapshots + data-age from store; GetWindow reconcile unchanged"
```

### Task 13: Calibration + blocks + warming state into `kv`

**Files:**
- Modify: `internal/calib/calib.go`, `internal/calib/compute.go` (`ReadCache`/`WriteCache` -> `store.GetKV('calibration')`/`PutKV`; blocks cache -> `kv 'blocks'`), `internal/statusline/command.go` (warming marker -> `kv 'warming'`)
- Modify: `internal/calib/*_test.go`

**Interfaces:**
- Consumes: `store.GetKV`, `store.PutKV`. Value blobs are the SAME JSON shapes as today (`{dpp,at}`, `{raw,at}`, warming epoch).

- [ ] **Step 1: Update calib cache tests** to round-trip `{dpp,at}` through `kv` (temp DB); assert the same read-back + staleness behaviour.
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Reimplement the cache read/write** over `kv`; keep the JSON payload shapes identical so no downstream parsing changes. Fail-open on the hot path (a missing kv row = cold cache, recompute).
- [ ] **Step 4: Run to verify pass** -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/calib/ internal/statusline/
git commit -m "feat(calib): calibration/blocks/warming state in kv table"
```

### Task 14: Snapshot prune via store; retire the file sweep

**Files:**
- Modify: `internal/statusline/command.go` (`PruneCaches` -> `store.PruneSnapshots(now - keepSecs)`; drop the snapshot-file + `.tmp` glob sweep), `internal/status/status.go` (`staleCaches` lists from the store or is removed)
- Modify: relevant tests

- [ ] **Step 1: Update prune tests** - snapshot rows older than `CCPOOL_CACHE_KEEP_SECS` deleted, newer kept; count returned.
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Reimplement `PruneCaches`** as a snapshots DELETE; remove the orphan `.tmp` sweep (no tmp files exist now). Ensure the uninstall/prune path also removes `ccpool.db-wal`/`-shm` if it removes the DB.
- [ ] **Step 4: Run to verify pass** -> PASS.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/statusline/ internal/status/
git commit -m "feat(statusline): snapshot prune via store DELETE; file/.tmp sweep + WAL-sidecar cleanup"
```

---

## Phase 5 - Conformance seeder, verification, cleanup

### Task 15: Conformance fixture -> temp DB seeder (tested)

**Files:**
- Create: `internal/store/seed_testing.go` (a `Seed(t, fixtures)` helper that builds a temp DB and INSERTs history/snapshot fixtures in arrival order, matching the importer byte-for-byte)
- Modify: `internal/*/conformance_test.go` suites that stage history/snapshot fixtures
- Create: `internal/store/seed_testing_test.go` (tests the seeder itself)

**Interfaces:**
- Produces: `func SeedHistory(t testing.TB, rows []HistoryRow) *Store`, `func SeedSnapshots(t testing.TB, payloads map[string][]byte) *Store`. Insert order == slice order == arrival order (load-bearing for the rowid tie-break).

- [ ] **Step 1: Write the seeder's own test** - seed 3 tied-timestamp rows in a known order, assert `EnvelopeWeekly` running-max matches the arrival order (proves the seeder preserves the tie-break).
- [ ] **Step 2: Run to verify fail** -> FAIL.
- [ ] **Step 3: Implement the seeder**; point the conformance suites at it instead of writing JSON/JSONL fixture files.
- [ ] **Step 4: Run to verify pass** `unset GOROOT && go test ./... -run Conformance -v` -> PASS with NO golden change.
- [ ] **Step 5: Gate + commit**

```bash
unset GOROOT && make check
git add internal/store/seed_testing.go internal/store/seed_testing_test.go internal/*/conformance_test.go
git commit -m "test(conformance): seed fixtures into a temp DB in arrival order; seeder unit-tested"
```

### Task 16: Full golden verification (expect NO change)

- [ ] **Step 1: Run the whole suite** `unset GOROOT && TZ=UTC go test ./...`. Expected: all green, zero golden diffs.
- [ ] **Step 2: If any golden differs**, STOP and investigate as a regression (do NOT `CCPOOL_UPDATE_GOLDEN` reflexively). Only re-baseline if the diff is a proven-intentional change and record why in `docs/DECISIONS.md`.
- [ ] **Step 3: Drive the real commands** `unset GOROOT && go run . status`, `go run . check`, `./ccpool statusline` - confirm output matches the pre-migration behaviour.
- [ ] **Step 4: No code change; no commit** unless an intentional golden update was made.

### Task 17: Remove now-dead file-storage code

**Files:**
- Modify/Delete: dead `flock`, tmp+rename snapshot write, glob reconcile, `marshalRow`, 64KB tail scan, snapshot-file path resolvers in `internal/paths` (`SnapshotFor`/`SnapshotGlob`/`SnapshotCache` if no longer referenced), the write-then-truncate prune helper.

- [ ] **Step 1: Find dead code** `unset GOROOT && staticcheck ./...` + grep for the retired symbols; confirm zero references.
- [ ] **Step 2: Delete** the unreferenced functions/paths; run `unset GOROOT && go build ./...`.
- [ ] **Step 3: Gate** `unset GOROOT && make check` -> green (staticcheck must be clean; unused code fails the gate).
- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: retire flock/tmp-rename/glob file-storage code superseded by the store"
```

### Task 18: Contended-write regression bench + fail-open fuzz

**Files:**
- Create: `internal/store/store_bench_test.go` (a scaled-down version of the spike: M concurrent writers via goroutines against a shared temp DB, assert zero dropped writes and a p99 ceiling)
- Create/Modify: `internal/store/store_fuzz_test.go` (fuzz `Snapshots` payload parse + `Open` on a truncated/garbage DB file - never panics)

- [ ] **Step 1: Write the bench + fuzz** - bench asserts 0 drops at M in {2,4,8}; fuzz feeds random bytes to the payload parser and to `Open` (garbage/truncated file) asserting no panic and a valid `ReadState`.
- [ ] **Step 2: Run** `unset GOROOT && go test ./internal/store/ -run Fuzz -v` and `go test ./internal/store/ -bench . -benchtime=1x` -> pass, 0 drops.
- [ ] **Step 3: Confirm fail-open** - a garbage DB file yields `StateCorrupt`/self-heal, never a panic; `warn`/`statusline` with a nil/locked DB render empty.
- [ ] **Step 4: Gate + commit**

```bash
unset GOROOT && make check
git add internal/store/store_bench_test.go internal/store/store_fuzz_test.go
git commit -m "test(store): contended-write regression bench (0 drops) + fail-open fuzz"
```

### Task 19: Update the docs/roadmap

**Files:**
- Modify: `docs/ROADMAP.md` (mark Sprint B done), `docs/DECISIONS.md` (record the actual outcome: envelope parity, contention bench numbers, sqlc tweaks, the live sentinel strip, config-stays-a-file)
- Modify: `AGENTS.md` if any storage invariant wording needs updating (e.g. the flock note)

- [ ] **Step 1: Update the docs** with what shipped and why.
- [ ] **Step 2: Commit** (docs-only; commit-force marker eligible)

```bash
git add docs/ AGENTS.md
git commit -m "docs: record Sprint B (SQLite storage) shipped; outcomes + decisions"
```

---

## Self-Review notes (author check against the spec)

- **Spec coverage:** every design section maps to a task - schema (T3), envelope SQL + tweaks (T3/T7), typed 3-way facade (T4/T5), corruption self-heal (T4), single-txn capture (T5/T11), `~/.ccpool/` move as a separate pre-B commit (T1/T2), snapshot reconcile stays Go (T12), kv (T13), prune rewrite + WAL sweep (T8/T14), importer one-off (T9), cutover guard (T10), hermetic seeder + tested (T15), golden-unchanged proof (T16), dead-code removal (T17), contention regression + fuzz (T18), docs (T19).
- **Known non-SQL-analogue branches** (non-numeric reset, `hasLatest==false`): retained as Go unit tests in `internal/burn` - keep those tests when refactoring T7; do not delete them.
- **Type consistency:** `HistoryRow`, `EnvRow`, `ReadState` names are used identically across T5/T6/T7/T11/T15.
- **Order dependency:** T1-T2 (home move) must land before T3+ so `paths.DB()` resolves under `~/.ccpool`. T9 (importer) runs after T6-T8 exist but before T16's parity check. T17 (dead-code) only after T11-T14 remove the last callers.
