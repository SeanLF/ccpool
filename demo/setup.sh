#!/usr/bin/env bash
# Stage deterministic fixture data for the demo GIFs, so they render FIXED numbers and never touch
# (or leak) your real ~/.ccpool. Source it -- it exports the hermetic CCPOOL_* env and puts a
# freshly-built ccpool on PATH. Regenerate the GIFs with `make demo`. (Needs sqlite3 to seed the store.)
set -eu
here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
data="$here/.data"
rm -rf "$data"; mkdir -p "$data"

now=$(date +%s)
reset=$((now + 4 * 86400)) # ~4 days to the weekly reset

# Isolate ALL ccpool state in a fresh SQLite store under .data, and run a freshly-built binary so the
# demo matches the current code. ccpool-owned state is the DB (history + snapshots + kv caches).
export CCPOOL_HOME="$data"
export CCPOOL_DB="$data/ccpool.db"
export CCPOOL_SETTINGS="$data/settings.json" # empty -> `init` shows a full fresh-wiring diff
( cd "$repo" && go build -o "$data/ccpool" . )
export PATH="$data:$PATH"

# the demo payload the statusline reads on stdin
cat > "$data/payload.json" <<JSON
{"session_id":"demo","context_window":{"used_percentage":38,"context_window_size":200000},"rate_limits":{"five_hour":{"used_percentage":22,"resets_at":$((now + 7200))},"seven_day":{"used_percentage":47,"resets_at":$reset}}}
JSON

# Prime the store: one render captures the snapshot, creates the schema, and writes the first history
# row (wk 47 @ now). Fail-open, so a hiccup here doesn't abort the demo.
ccpool statusline < "$data/payload.json" > /dev/null 2>&1 || true

# Seed the rest directly into the store's tables (sqlite3, a demo-only dev dependency): two earlier
# weekly points so the run is monotonic (30 -> 40 -> 47) and burn/runway project, plus a warm $/1%
# calibration so the $ shows immediately (ccusage isn't available in the sandbox to compute it).
sqlite3 "$CCPOOL_DB" <<SQL
INSERT INTO history (t, wk, wk_reset) VALUES ($((now - 36000)), 30, $reset), ($((now - 18000)), 40, $reset);
INSERT OR REPLACE INTO kv (key, value, updated_at) VALUES ('calibration', '{"dpp":26.0,"at":$now}', $now);
SQL
