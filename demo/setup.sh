#!/usr/bin/env bash
# Stage deterministic fixture data for the demo GIFs, so they render FIXED numbers and never touch
# (or leak) your real ~/.claude usage. Source it -- it exports the hermetic CCPOOL_* env and puts
# ccpool on PATH. Regenerate the GIFs with `make demo`.
set -eu
here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
data="$here/.data"
rm -rf "$data"; mkdir -p "$data"

now=$(date +%s)
reset=$((now + 4 * 86400)) # ~4 days to the weekly reset

export USAGE_CACHE="$data/usage-cache.json"
export CCPOOL_HISTORY="$data/history.jsonl"
export CCPOOL_CALIB_CACHE="$data/calib.json"
export CCPOOL_STATUSLINE_LOG="$data/sl.log"
export CCPOOL_SETTINGS="$data/settings.json" # empty -> `init` shows a full fresh-wiring diff
export PATH="$repo/bin:$PATH"

# a session snapshot: rate_limits (weekly 47%, 5h 22%) + context window
cat > "$data/usage-cache-demo.json" <<JSON
{"captured_at":$now,"session_id":"demo","context_window":{"used_percentage":38,"context_window_size":200000},"rate_limits":{"five_hour":{"used_percentage":22,"resets_at":$((now + 7200))},"seven_day":{"used_percentage":47,"resets_at":$reset}}}
JSON

# a monotonic weekly run so burn/runway project and the $/1% calibrates
printf '{"t":%d,"wk":30,"wk_reset":%d}\n{"t":%d,"wk":47,"wk_reset":%d}\n' \
  $((now - 36000)) "$reset" "$now" "$reset" > "$CCPOOL_HISTORY"

# a warm calibration so the $ shows immediately (dpp = $/1% of the weekly pool)
echo "{\"dpp\":26.0,\"at\":$now}" > "$CCPOOL_CALIB_CACHE"

# the demo payload the statusline reads on stdin
cp "$data/usage-cache-demo.json" "$data/payload.json"
