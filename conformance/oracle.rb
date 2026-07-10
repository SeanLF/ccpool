# frozen_string_literal: true

# Conformance oracle: render one fixture through the REAL Ruby statusline and emit the exact bytes,
# so the Go port can diff against it (docs/GO-MIGRATION.md: "diff Go output against Ruby output").
#
# Env (NO_COLOR, COLUMNS, CCPOOL_*, TZ, CCPOOL_CALIB_CACHE) is supplied by the caller's process
# environment BEFORE this file loads, because statusline.rb/profile.rb/calibration.rb freeze their
# config into constants at require time. stdin = {"now": <int>, "payload": {...}}.
# stdout = <render>\0<render_compact>  (NUL separates; neither render contains a NUL).

require "json"
require_relative "../statusline"

input = JSON.parse($stdin.read)
now = input.fetch("now")
payload = input.fetch("payload")

$stdout.binmode
$stdout.write(Statusline.render(payload, now))
$stdout.write("\0")
$stdout.write(Statusline.render_compact(payload, now))
