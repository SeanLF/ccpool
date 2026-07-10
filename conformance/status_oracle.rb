# frozen_string_literal: true

# Conformance oracle for `ccpool status`: run the REAL CCPool.status against staged snapshots +
# history + calibration/ccusage, and emit the exact stdout bytes so the Go port can diff its readout.
#
# Snapshots (USAGE_CACHE glob), history (CCPOOL_HISTORY), calibration cache (CCPOOL_CALIB_CACHE),
# and the fake ccusage (CCPOOL_CCUSAGE_CMD -> fake-ccusage.sh, CCUSAGE_FIXTURE) are staged by the
# caller via env before load. stdin = {"now": <int>}. stdout = CCPool.status's printed lines.

require "json"
require_relative "../ccpool"

now = JSON.parse($stdin.read).fetch("now")
$stdout.binmode
CCPool.status(now)
