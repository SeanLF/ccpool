-- name: AppendHistory :exec
INSERT INTO history (t, wk, wk_reset, ses, ses_reset, cost, session)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: LastSessionRow :one
SELECT * FROM history
WHERE (sqlc.narg('session') IS NULL OR session = sqlc.narg('session'))
ORDER BY id DESC LIMIT 1;

-- name: EnvelopeWeekly :many
-- Two-pass running-max mirror of burn.Envelope (see docs/sqlite-storage-design.md):
-- latest = max weekly reset in the window; kept = rows within the 300s jitter bucket of it
-- (or, when there is no latest, rows with a NULL reset); running = arrival-order running max.
-- Every latest column is qualified (history.wk) and the computed running column is CAST, or
-- sqlc's analyzer errors / emits interface{}. reset stays un-CAST: sqlc cannot type an aggregate
-- (max wk_reset) inside a scalar subquery, so it emits interface{} (NOT sql.NullInt64). That is
-- deliberate: interface{} preserves the nullability the !hasLatest path needs (NULL->nil, int->int64),
-- and the store facade normalizes it to sql.NullInt64. CASTing reset would type it non-nullable int64
-- and fail-scan on the NULL (no-latest) case, so we do NOT CAST it.
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
-- CAST(COALESCE(...)) not bare CAST: max() over an empty snapshots table (the fresh-install state)
-- is NULL, and a bare CAST(max AS INTEGER) still yields NULL at runtime -> fail-scans NULL->int64 on
-- first run. COALESCE(..., 0) makes the runtime value never NULL (0 = "no snapshots yet"); the outer
-- CAST keeps sqlc typing newest as a clean non-null int64. The facade reads 0 as no-data (captured_at
-- is always a real epoch, never 0). (bare COALESCE alone makes sqlc emit interface{}.)
SELECT CAST(COALESCE(max(captured_at), 0) AS INTEGER) AS newest FROM snapshots;

-- name: GetKV :one
SELECT value FROM kv WHERE key = ?;

-- name: PutKV :exec
INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: PruneHistory :execrows
DELETE FROM history WHERE t < ?;

-- name: PruneSnapshots :execrows
DELETE FROM snapshots WHERE captured_at < ?;
