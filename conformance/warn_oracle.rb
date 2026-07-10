# frozen_string_literal: true

# Conformance oracle for `warn`: run the REAL Ruby Warn.run against staged snapshots + markers and
# emit the exact output bytes so the Go port can diff the emitted warning text / hook JSON.
#
# Snapshots (USAGE_CACHE glob), markers (TMPDIR), and CCPOOL_WARN_* thresholds are staged by the
# caller via env before load. stdin = {"now": <int>, "payload": {hook payload}}.
# stdout = Warn.run's output ("" when nothing fires).

require "json"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
$stdout.binmode
$stdout.write(Warn.run(input.fetch("payload"), input.fetch("now")).to_s)
