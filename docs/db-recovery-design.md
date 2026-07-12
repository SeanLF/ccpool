# DB corruption recovery — design (2026-07-12)

Evidence-backed design for handling SQLite corruption without silently losing the user's history.
Supersedes the T4 "self-heal to empty" behaviour. Grounded in a corruption PoC (this repo) + prior-art
research (`docs/` commit; sources cited inline).

## The problem (measured, not assumed)

A corruption PoC on a realistic 5,000-row ccpool DB (`internal/store/schema.sql`, 78 pages) found:

| corruption | integrity_check | ccpool auto-wipes? | `.recover` salvages |
|---|---|---|---|
| header zeroed | unopenable | **yes** | ~0 (needs the JSONL) |
| truncated 50% | unopenable | **yes** | **2,306 / 5,000** |
| page bit-flip | detects | no | 4,998 / 5,000 |
| page zeroed | detects | no | 4,904 / 5,000 |
| trailing garbage | ok (harmless) | no | 5,000 |

Two findings:
1. **The auto-wipe fires only when the schema probe at `Open` fails** (header corruption / truncation /
   NOTADB). Page-level corruption leaves the file intact and degrades gracefully (read-layer
   `StateCorrupt`, fail-open). So the destructive path is narrow — but it hits exactly the cases where
   `.recover` could still salvage thousands of rows (2,306 from a truncated file, proven).
2. **After a wipe, `ccpool check` prints the FRESH-INSTALL message** ("No usage snapshots yet… open a
   window…"). A user whose DB was just corrupted and quarantined is told it looks brand-new — the event
   is disguised. The quarantine *preserves* the data (`.corrupt-<ts>`), but nothing recovers it and the
   user is never told it exists.

History is **not** cheaply regenerable: the wk% time-series takes weeks to rebuild enough for accurate
`$/1%` calibration and burn projection. So auto-emptying it is a real loss.

## Constraints (from the research)

- **`modernc.org/sqlite` cannot run `.recover`** — the recovery extension (`sqlite3recover.c`) is outside
  the amalgamation modernc ports, and the backup C API isn't exposed through `database/sql`. Recovery on
  our stack is either (a) a **pure-Go table-walk salvage** (`INSERT INTO new SELECT * FROM
  quarantined.history`, catching per-page `SQLITE_CORRUPT`), or (b) shelling to the external `sqlite3`
  CLI's `.recover` (present on macOS 3.54, NOT guaranteed on a user's box — probe at runtime).
- **Top corruption vectors** are backup/sync tools copying the DB mid-write (Time Machine/iCloud/Dropbox
  over `~/.ccpool`) and bit-rot — NOT power loss (WAL+`synchronous=NORMAL` survives a crash mid-COMMIT;
  only a checkpoint-during-crash-on-a-lying-drive corrupts, which is rare). So keep `NORMAL`; the fix is
  a backup + recovery, not durability tuning.
- **Prior art is unanimous** (Firefox `places.sqlite`, Chromium): detect lazily on access, quarantine
  (never delete), **restore the valuable table from a rolling backup**, let the cheap data regenerate.
  ccpool has the quarantine half but no backup to restore from.
- Detection stays **off the hot path**: trigger recovery on a real `SQLITE_CORRUPT` from a query (free);
  `integrity_check` (O(N log N)) runs only in `doctor` and post-recovery.

## Design — automatic + best-effort, `doctor` as escalation

The map onto ccpool's data tiers: **history = Firefox's bookmarks (back it up + restore); snapshots +
kv = Firefox's history (let them regenerate).**

1. **Rolling last-known-good backup** (`store`): `VACUUM INTO $CCPOOL_DB.bak`, pure-Go through
   `database/sql`, self-validating (it fails on a corrupt source, so a successful run is provably clean
   → only ever overwrite `.bak` on success). Gated to run **occasionally** (>= ~24h since the last, via a
   `kv` timestamp), from the statusline capture path — **never per hook**. This is the piece that makes
   recovery reliable instead of scavenging a broken file.

2. **Automatic heal-with-restore** (`store.Open`): when the schema probe fails → quarantine the corrupt
   file aside (as today) → recreate empty → **best-effort restore `history` from `.bak`** into the fresh
   DB (snapshots/kv regenerate). Leave a breadcrumb so commands + `doctor` know a recovery happened. This
   is AUTOMATIC: the user's statusline keeps rendering and history is restored transparently, no command
   needed for the common case. One-time cost on the rare corrupt Open; fail-open on any error (empty DB +
   breadcrumb).

3. **`ccpool doctor`** — the manual escalation when auto-restore wasn't enough (no `.bak` yet, or the
   backup is stale and the quarantine holds newer rows). Ladder, never deleting the original:
   (a) `integrity_check` + report; (b) pure-Go salvage from the newest quarantine into the live DB;
   (c) if the file won't open and `sqlite3` is on PATH and capable, shell out to `.recover` (runtime
   probe, never assume); (d) re-import from `rate-limit-history.jsonl` if present; (e) else keep the
   quarantine and print its path. `--reset` = the destructive recreate-empty, opt-in only.

4. **Honest messaging** (breadcrumb): post-heal, `status`/`check`/statusline say "recovered from
   corruption — run `ccpool doctor` for deeper recovery" (or, if auto-restore fully succeeded, a quiet
   note), NOT the fresh-install copy. Clears when `doctor` finishes or the DB re-populates.

5. **Quarantine audit**: never rename/unlink the DB while another ccpool process may hold it open
   (undefined behaviour per SQLite §2.5) — the current `quarantine` runs inside `Open` before the fresh
   handle exists, which is safe; keep it that way.

Keep `synchronous=NORMAL` + WAL + `busy_timeout=5000`. No `synchronous=FULL` (marginal for our pattern).

## Invariant change (was T4)

T4 shipped "corrupt DB → self-heal to EMPTY (fresh install and post-corruption land on the same path)."
That was too optimistic about history's regenerability and disguised the loss. New invariant:
**corrupt DB → quarantine + recreate + best-effort restore history from the rolling backup + surface it;
`doctor` does the deeper salvage.** Fail-open on the hot path is preserved (empty-and-rendering beats
blocking); the change is that we no longer *silently* accept empty when a backup or salvage exists.
Recorded in `docs/DECISIONS.md`.

## Build order (each tested + committed)

1. Rolling `VACUUM INTO` backup + the gate (store method + statusline wiring). Test: backup created,
   gated, self-validates.
2. Auto heal-with-restore in `Open` + breadcrumb. Test: seed history, corrupt-header, Open → restored
   from `.bak`; no `.bak` → empty + breadcrumb.
3. `ccpool doctor` recovery ladder + `--reset`. Test: salvage from a quarantined truncated DB (the PoC's
   2,306-row case) via the pure-Go path; JSONL re-import; sqlite3 `.recover` when present.
4. Honest post-heal messaging (breadcrumb → status/check/statusline).
5. Per-command `--help` registry (the one real Sprint-C win, hand-rolled, no cobra).
