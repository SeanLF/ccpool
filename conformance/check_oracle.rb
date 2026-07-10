# frozen_string_literal: true

# Conformance oracle for `ccpool check`: run the REAL Check.report against staged snapshots +
# history + calibration/ccusage, and emit the exact [lines, code] so the Go port can diff both.
#
# Snapshots (USAGE_CACHE glob), history (CCPOOL_HISTORY), calibration cache (CCPOOL_CALIB_CACHE),
# and the fake ccusage (CCPOOL_CCUSAGE_CMD -> fake-ccusage.sh, CCUSAGE_FIXTURE) are staged by the
# caller via env before load. stdin = {"now": <int>}.
# stdout = "<code>\n" followed by the lines joined by "\n".

require "json"
require_relative "../ccpool"

now = JSON.parse($stdin.read).fetch("now")
lines, code = Check.report(now)
$stdout.binmode
$stdout.write(code.to_s)
$stdout.write("\n")
$stdout.write(lines.join("\n"))
