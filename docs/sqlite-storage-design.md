# SQLite storage (Sprint B) design

Status: design, approved decisions + spike evidence, pending final user review before the
implementation plan. Supersedes the Sprint B sketch in `docs/ROADMAP.md:127`. Read
`docs/DECISIONS.md` (the SQLite envelope->SQL spike, 2026-07-10) for the prior art this builds on.

## Goal

Replace ccpool's bespoke on-disk file storage (a JSONL history + globbed per-session snapshot JSON +
small state JSON files) with one embedded SQLite database. The payoff is not a bugfix (the current
`flock` path passes 8-way concurrency) but dissolving the subtlest hand-rolled code in the tool:
tail-dedup, glob reconcile, prune, and the `Burn.envelope` running-max. It is release-prep for outside
users, and the real merit is clean concurrency across *processes* (multiple Claude Code windows), which
one user already has.

Hard constraint: **byte-identical output**. The golden conformance suite (`status`/`check`/`burn`/
statusline output) is the proof of correctness. The migration changes storage, not behaviour, with one
deliberate exception (the sentinel bugfix, below).

## Scope

In (move into SQLite):
- **history** (`rate-limit-history.jsonl`): the append-only rate-limit log. Its tail-dedup, prune, and
  `Burn.envelope` all become SQL. This is the core win.
- **snapshots** (`usage-cache-<session>.json`, globbed): per-session raw statusline payloads.
- **kv** (`ccpool-calibration.json`, `ccpool-blocks-cache.json`, the `.warming` marker): small state.

Out (stay as-is):
- **config** (`ccpool.json`): user-facing, just shipped. Moves *directory* (see Location) but stays a
  JSON file, not a DB row. Deliberate: config is user *source-of-truth* (hand-edited, dotfiles-able,
  `env > file > default`), not regenerable state. The DB self-heal path recreates an empty DB, so
  config in the DB would be silently wiped on corruption; a file loses only recomputable data. The
  guiding line is exactly XDG's config-vs-data/cache split, honoured within the one dir: **DB = append +
  regenerable state** (history, snapshots, kv cache), **file = user intent that must survive and stay
  editable** (config).
- **statusline.log**: append-only text log, fine as a file (moves directory).
- **`projects/**`** transcripts: external, Claude Code owns them; ccpool only reads. Stays in `~/.claude`.
- **`/tmp` warn throttle markers**: ephemeral per-process throttle; stay `/tmp` files (no reason to
  contend on the DB write lock for a throttle).

## Decisions (all spike-backed)

| Decision | Outcome | Evidence |
|---|---|---|
| Move snapshots into the DB? | Yes | Contention bench: 0 dropped writes across 50 configs; p99 < 20ms through M=8 windows; M=16 storm tail (~37ms) also hits the file baseline, so it is OS fork scheduling, not a SQLite regression. |
| Query layer | sqlc for all queries | sqlc spike: v1.31.1 + modernc v1.53.0 generate + compile + run end-to-end on the full set incl. the window query. Two required tweaks below. |
| Envelope -> SQL faithful? | Yes | Envelope PoC on real 56,978 rows: numerically identical to Go on weekly + 5h, sentinel-present and sentinel-stripped. |
| Location | Single `~/.ccpool/` dir, `CCPOOL_HOME` override; NOT XDG | XDG splits state across up to 4 base dirs and degrades to a Linux-ism on macOS; a single dotdir is more findable/nukeable and matches "resist over-structuring". |
| Cutover | Hard cutover, no JSONL read-fallback | No outside users; the one existing user migrates via a one-off importer. Guarded (below). |
| Concurrency | WAL, `busy_timeout=5000`, `synchronous=NORMAL`, single txn per write | Bench: single-txn latency-neutral, keep it for atomicity; 5000ms costs nothing uncontended and backstops a checkpoint stall. |
| Snapshot reconcile | Stays in Go (`pool.GetWindow` untouched) | Tiny dataset; subtle jitter/clamp/leak-guard logic with no clean SQL analogue. |

The RANGE-vs-ROWS window-frame concern from the spike is real *insurance* but was **not** empirically
triggered on the real data (coarse integer `wk`/`ses` never set a fresh high mid-tie-burst; all four
formulations produced identical output). We keep `ROWS ... ORDER BY t, id` because it is provably equal
to Go's arrival-order running max under hostile ordering, not because current data needs it.

## Architecture

### New package `internal/store`

Owns the `*sql.DB`, the embedded `schema.sql`, the sqlc-generated queries (`internal/store/db`), and a
thin facade the rest of the codebase calls. sqlc-generated types never leak past the facade.

Facade methods (roughly): `Open`, `AppendHistory`, `EnvelopeWeekly`, `EnvelopeFiveHour`, `PutSnapshot`,
`Snapshots`, `GetKV`, `PutKV`, `PruneHistory`, `PruneSnapshots`, `DataAge`, `LastSessionRow`.

### Fail-open is NOT uniform: a typed 3-way result

This is the correction to the naive "return zero value on any error" facade. ccpool's invariant is that
the hot path (`warn`, `statusline`) fails OPEN, but the on-demand commands (`status`, `check`) fail LOUD
and **distinguish states** the user sees: `burn.Read` returns `(nil,false)` for *unreadable* vs
`([]Entry{},true)` for *absent/empty*, and `check` prints "history unreadable -- projection
unavailable" only on the unreadable case (`internal/status/check.go:171`); `absentOrCorrupt`
(`check.go:59`) distinguishes zero-snapshots (warm-up) from present-but-none-readable (corruption).

A uniform fail-open facade would collapse *empty*, *busy*, *disk-full*, and *corrupt* into one signal,
so a healthy-but-momentarily-busy DB during a `check` would raise a **false corruption alarm**. Today
that is impossible because reads take no lock. So the facade read methods return a typed state:

```go
type ReadState int
const (
    StateOK        ReadState = iota // query ran; rows (possibly empty) are valid
    StateCorrupt                    // SQLITE_CORRUPT / SQLITE_NOTADB -> genuinely unreadable
    StateTransient                 // SQLITE_BUSY after timeout, I/O error -> unknown, retry
)
```

Three states, not four: `StateOK` with an *empty* result set IS the warm-up / no-data-yet case (the
`([]Entry{},true)` Go semantics; `emit_empty_slices: true` guarantees `[]`, not nil). The finer
"zero rows (warm-up)" vs "rows present but none parse (corruption)" distinction that `absentOrCorrupt`
draws today is made **in Go over the returned rows** (a snapshot row whose payload JSON does not parse
is skipped exactly as today), not at the facade. `StateCorrupt`/`StateTransient` are DB-file-level
states SQLite introduces that the file world never had.

- Hot path (`warn`, `statusline`): treats every non-OK state as empty and degrades silently. Never
  panics (top-level `recover` stays).
- `check`/`status`: `StateOK` + empty -> warm-up/empty; `StateOK` + rows-but-none-parse -> "unreadable"
  (Go-side, as today); `StateCorrupt` -> "unreadable"; `StateTransient` -> "unknown, retry" (NOT
  "corrupt"). This preserves the truthful distinction the tool makes today rather than raising a false
  corruption alarm on a merely-busy DB.

### Corruption handling (real self-heal, not a claim)

"Self-heals on next write" is false for a corrupt SQLite file: writes also return `SQLITE_CORRUPT`, and
one DB is now one blast radius for all three subsystems. `store.Open` therefore probes integrity
cheaply (open + a trivial `PRAGMA quick_check` or a guarded first query) and, on `SQLITE_CORRUPT` /
`SQLITE_NOTADB`:
1. renames the DB aside (`ccpool.db.corrupt-<t>`), plus its `-wal`/`-shm`,
2. recreates an empty DB from `schema.sql`,
3. returns `StateOK` with an empty result (warm-up) to callers, not `StateCorrupt`.

This is a real, tested code path, not a comment. Fresh install and post-corruption both land on the
same empty-DB self-population path.

## File location

Introduce `~/.ccpool/` as the home for all ccpool-*owned* state: `ccpool.db` (+ `-wal`/`-shm`),
`ccpool.json`, `statusline.log`. Overridable via a single `CCPOOL_HOME`. Individual per-file env
overrides (`CCPOOL_DB`, `CCPOOL_CONFIG`, ...) still resolve first where they exist. Only *reads* stay in
`~/.claude` (`projects/**` transcripts + the stdin payload).

The DB path resolves: `CCPOOL_DB` else `$CCPOOL_HOME/ccpool.db` else `~/.ccpool/ccpool.db`.

**Sequencing: this move ships as its own isolated commit BEFORE the SQLite work**, not bundled into it.
It rewrites `internal/paths/paths.go` defaults and touches shipped surface (`README.md:146`,
`docs/config-file-*.md`, `docs/CONFIG-AUDIT.md` all document `~/.claude/ccpool.json`), so it is
reviewable on its own. The Claude Code hooks wire the *binary* path, not the data dir, so they are
unaffected. The one existing user's files migrate (config/calib/blocks/log copied; history via the
importer). Doing it first is efficient because `paths.go` is rewritten by B anyway.

## Schema

```sql
CREATE TABLE history (
  id        INTEGER PRIMARY KEY,   -- rowid = arrival-order tie-break (see envelope)
  t         INTEGER NOT NULL,      -- epoch secs, write time
  wk        REAL    NOT NULL,      -- seven_day used_percentage
  wk_reset  INTEGER,               -- seven_day resets_at (nullable)
  ses       REAL,                  -- five_hour used_percentage (nullable)
  ses_reset INTEGER,               -- five_hour resets_at (nullable)
  tier      TEXT    NOT NULL,
  cost      REAL,                  -- nullable
  session   TEXT                   -- nullable
);
CREATE INDEX history_t ON history(t);          -- cutoff filters + prune

CREATE TABLE snapshots (
  session     TEXT    PRIMARY KEY, -- one row per session, UPSERT (latest wins)
  captured_at INTEGER NOT NULL,
  payload     TEXT    NOT NULL     -- raw CC statusline JSON blob, re-parsed in Go unchanged
);
CREATE INDEX snapshots_captured ON snapshots(captured_at);   -- prune + DataAge

CREATE TABLE kv (
  key        TEXT PRIMARY KEY,     -- 'calibration', 'blocks', 'warming'
  value      TEXT NOT NULL,        -- same JSON blob as today's small state files
  updated_at INTEGER NOT NULL
);
```

Notes:
- **history** is decomposed into typed columns because `Burn.envelope` becomes a window query. Storing
  epoch *ints* for `t`/resets (not SQLite date types) sidesteps the `TZ=UTC` golden hazard entirely.
- **snapshots** keeps the *whole* raw payload as TEXT so Go re-parses it exactly as today
  (`rb.ParseObject`), `GetWindow` reconcile stays byte-identical, and no context field `warn` needs is
  lost. UPSERT on `session` mirrors today's per-session-file latest-wins.
- **rowid reuse landmine:** `INTEGER PRIMARY KEY` reuses a rowid only if the max row is deleted. Prune
  deletes *oldest* rows (`t < cutoff`), never the newest, so arrival order is safe. This invariant is
  load-bearing for the envelope tie-break; a `WHY` comment on the table guards it. If any future code
  ever deletes the newest row, switch to `AUTOINCREMENT`.

## Query layer (sqlc)

sqlc config (v2, sqlite engine, `database/sql`):

```yaml
version: "2"
sql:
  - engine: "sqlite"
    schema: "schema.sql"
    queries: "query.sql"
    gen:
      go:
        package: "db"
        out: "internal/store/db"
        sql_package: "database/sql"
        emit_empty_slices: true
```

Two **required** tweaks the spike surfaced (without them codegen fails or emits `interface{}`):
1. **Qualify every column in the `latest` CTE** (`history.t`, `history.wk_reset`, ...). Bare `t` errors
   `column reference "t" is ambiguous` under sqlc's scope-flattening analyzer.
2. **`CAST` the computed result columns** (`running`, `reset`, `DataAge`'s `max`) to `REAL`/`INTEGER`,
   or sqlc types them `interface{}`. CAST yields proper `float64`/`int64`.

### The envelope query (final, weekly; 5h is the same on `ses`/`ses_reset`)

```sql
-- name: EnvelopeWeekly :many
WITH latest AS (
  SELECT max(history.wk_reset) AS r
  FROM history
  WHERE history.wk IS NOT NULL AND history.wk_reset IS NOT NULL
    AND history.t >= @cutoff                 -- mirror Go's Read() 14d trim (finding: was missing)
),
kept AS (   -- FILTER FIRST, then window over the filtered set (matches Go's two-pass)
  SELECT h.t, h.wk AS f, h.id
  FROM history h, latest
  WHERE h.t >= @cutoff AND h.wk IS NOT NULL
    AND CASE WHEN latest.r IS NOT NULL
             THEN h.wk_reset IS NOT NULL AND latest.r - h.wk_reset <= 300   -- 300s jitter bucket
             ELSE h.wk_reset IS NULL END
)
SELECT kept.t,
       CAST(max(kept.f) OVER (ORDER BY kept.t, kept.id
                              ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS REAL) AS running,
       (SELECT r FROM latest) AS reset          -- keep nullable (sql.NullInt64) for the !hasLatest path
FROM kept
ORDER BY kept.t, kept.id;
```

`@cutoff = now - 14*86400`. The window runs over the *filtered* `kept` set, `ROWS` not default `RANGE`,
`ORDER BY t, id`. `reset` stays `sql.NullInt64` (no CAST) so the Go-only `!hasLatest` branch (kept rows
with a NULL latest) scans cleanly rather than erroring on NULL->int64.

Reference: `internal/burn/burn.go:139` `Envelope(entries, field, resetField)` two-pass logic. Pass 1:
`latest` = max(resetField) over rows where both field and resetField numeric; `hasLatest` = any found.
Pass 2 (chronological): keep iff (hasLatest && reset within 300s of latest) or (!hasLatest && reset
nil); running max over kept rows.

### What is SQL vs what stays Go

- **SQL:** history append, envelope (weekly + 5h), prune (history + snapshots), `DataAge`
  (`SELECT max(captured_at)`), tail-dedup lookup (`LastSessionRow`).
- **Go (unchanged):** snapshot reconcile `pool.GetWindow` (reads parsed maps from `store.Snapshots()`),
  the `skip()` throttle policy (dedup identical wk/wk_reset; 60s throttle when only `ses` moved) applied
  after `LastSessionRow`. `flock` disappears; WAL + `busy_timeout` handle cross-process writes.

### hasLatest / non-numeric-reset carve-out

The SQL `CASE ... ELSE h.wk_reset IS NULL` *does* implement the `!hasLatest` nil-reset branch. The only
genuinely non-SQL-analogue case is a *non-numeric* stored reset (a malformed value the typed writer
cannot produce; SQL has only INTEGER-or-NULL). That, plus the `hasLatest==false` shape, stay as **Go
unit tests**, not forced into SQL.

## Concurrency, durability, atomicity

- `PRAGMA journal_mode=WAL; busy_timeout=5000; synchronous=NORMAL`.
- Each short-lived process: open -> work -> close. WAL keeps *reads* lock-free (so `check`/`warn`/`pool`
  reads never block a concurrent writer). Writers serialize; the bench proves the contention manifests
  as sub-20ms p99 latency through M=8 with zero drops.
- The statusline write is **one transaction**: snapshot UPSERT + history INSERT commit together, so a
  reader never sees a snapshot without its paired history row. Bench-confirmed latency-neutral.
- WAL auto-checkpoint occasionally makes one writer pay a checkpoint (part of the storm-tail maxima).
  Acceptable; `wal_autocheckpoint` is a tuning knob if ever needed. `synchronous=NORMAL` under WAL
  fsyncs at checkpoint, not per commit, which is why sustained writes stay fast.

## Migration and versioning

- `PRAGMA user_version` gates schema evolution. v1 is the initial schema, applied idempotently
  (`CREATE TABLE IF NOT EXISTS`). Forward path is sketched now so Sprint C+ does not rediscover it:
  `store.Open` reads `user_version` and runs a `switch` of ordered migration steps, bumping the pragma
  after each. No migration *framework*, just an ordered `[]func(*sql.Tx) error`.
- **Cutover guard (silent-data-loss protection).** Hard cutover means if the one-off importer never
  runs, the user upgrades into an empty history DB and weekly burn/runway silently vanish (empty table
  reads as `StateOK`-empty warm-up, not an error). To prevent that: on `Open`, if a legacy
  `~/.claude/rate-limit-history.jsonl` (or `$CCPOOL_HOME` sibling) exists *and* the `history` table is
  empty, `status`/`check` print a loud one-time "history not imported, run the importer" warning rather
  than presenting empty history as truth. This is the one place the cutover fails loud.

## Importer (one-off, NOT shipped)

A throwaway script (a `//go:build ignore` tool or a `sqlite3`/python one-liner), never in the binary.
It migrates *the existing user's* `rate-limit-history.jsonl` once:
- read the JSONL in **file/arrival order** (preserves the `id` rowid tie-break),
- **skip the synthetic sentinel rows** (`session="bench"` and any `wk_reset=9999999999`), then
- `INSERT INTO history` preserving order.

Outside users install fresh (empty DB self-populates). The sentinel-skip is belt-and-suspenders: the
live file's 4 sentinel rows were already stripped out-of-band, and the fixtures never had any, so the
skip guards only against future reappearance (it is not expected to change any golden).

## Testing and verification

- **Golden suite is the proof.** Re-baseline with `CCPOOL_UPDATE_GOLDEN=1 TZ=UTC go test ./...` and
  review the diff. The conformance suites must be reworked to **build a temp DB and INSERT the JSON/JSONL
  fixtures in arrival order** (matching the importer byte-for-byte), reproducing the rowid tie-break for
  the 76%-tied-timestamp data. The fixture->DB seeder is itself unit-tested (it is now load-bearing).
- **Hermeticity.** The suite must set `CCPOOL_HOME` (or `CCPOOL_DB`) so tests never touch a real
  `~/.ccpool`. Same hermetic contract as today's `CCPOOL_*`/`USAGE_*` fixtures.
- **Goldens should pass UNCHANGED.** The conformance fixtures (`conformance/*_fixtures.json`) contain no
  sentinel rows (verified), and the envelope PoC proved the SQL numerically identical to Go on clean
  data. So a correct migration re-runs the existing goldens green with **no re-baseline**; that is the
  proof. If a golden shifts, treat it as a regression to investigate, not an expected update. (The
  sentinel collapse was a *live-data* bug, fixed out-of-band by stripping the 4 sentinel rows from
  `rate-limit-history.jsonl`; it never touched the fixtures. The importer keeps its sentinel-skip as
  belt-and-suspenders in case sentinels reappear in live data.)
- **Contended write bench** kept as a regression guard (revives the spike): asserts zero dropped writes
  and a p99 ceiling at realistic M.
- **Fail-open gate.** Fuzz the new parse/scan paths. Confirm every hot-path entry (`warn.Hook`,
  `statusline.Command`) returns empty (never panics) on a nil DB, a locked DB, and a corrupt DB file.
- **Prune rewrite.** `statusline.PruneCaches` (which today sweeps snapshot files + orphan `.tmp`)
  becomes a snapshots `DELETE WHERE captured_at < cutoff`; the `.tmp` sweep is orphaned and removed.
  Uninstall/prune must also sweep the `-wal`/`-shm` sidecars.
- **Go unit tests** retained for the non-numeric-reset and `hasLatest==false` branches (no SQL analogue).
- `make check` green before any commit.

## Sequencing (commit plan)

1. **`~/.ccpool/` home-dir move** (isolated): `paths.go` defaults + `CCPOOL_HOME`, migrate the user's
   config/calib/blocks/log, update README + config docs/tests. Golden-neutral.
2. **`internal/store` + schema + sqlc + facade** (typed 3-way states, corruption self-heal), behind the
   facade, no callers switched yet.
3. **Switch history** to the store (append, envelope, prune, tail-dedup); one-off importer; re-baseline
   goldens (with the sentinel divergence noted).
4. **Switch snapshots + kv** to the store; rewrite `PruneCaches`; single-txn capture+append.
5. **Cleanup**: remove `flock`/tmp-rename/glob machinery now dead; contended-bench regression test.

## Risks / open items

- sqlc's weak inference on computed columns is handled by CAST; watch for it on any *new* aggregate
  query.
- WAL sidecars must never be copied out-of-band (Dropbox/rsync of a live `-wal`) or the DB corrupts.
  `~/.ccpool/` is not a synced dir by default; document "do not sync a live DB".
- The M=16 simultaneous fork-storm tail (~37ms) is imperceptible for a statusline redraw and matches the
  file baseline; not a blocker, noted for honesty.

## Prior art referenced

- `docs/DECISIONS.md` SQLite spike (envelope->SQL, RANGE-vs-ROWS, size + latency spikes, sentinel purge).
- `docs/ROADMAP.md:127` Sprint B sketch (superseded by this doc).
- Envelope PoC, sqlc spike, contention bench (this design's de-risking; results summarised above).
