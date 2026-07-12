CREATE TABLE IF NOT EXISTS history (
  id        INTEGER PRIMARY KEY,   -- rowid = arrival-order tie-break; prune deletes oldest only, never max, so rowid is never reused (envelope depends on this)
  t         INTEGER NOT NULL,      -- epoch secs, write time
  wk        REAL    NOT NULL,      -- seven_day used_percentage
  wk_reset  INTEGER,               -- seven_day resets_at (nullable)
  ses       REAL,                  -- five_hour used_percentage (nullable)
  ses_reset INTEGER,               -- five_hour resets_at (nullable)
  tier      TEXT    NOT NULL,
  cost      REAL,                  -- nullable
  session   TEXT                   -- nullable
);
CREATE INDEX IF NOT EXISTS history_t ON history(t);

CREATE TABLE IF NOT EXISTS snapshots (
  session     TEXT    PRIMARY KEY, -- one row per session, UPSERT (latest wins)
  captured_at INTEGER NOT NULL,
  payload     TEXT    NOT NULL     -- raw CC statusline JSON blob, re-parsed in Go unchanged
);
CREATE INDEX IF NOT EXISTS snapshots_captured ON snapshots(captured_at);

CREATE TABLE IF NOT EXISTS kv (
  key        TEXT PRIMARY KEY,     -- 'calibration', 'blocks', 'warming'
  value      TEXT NOT NULL,        -- same JSON blob as today's small state files
  updated_at INTEGER NOT NULL
);
