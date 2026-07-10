# frozen_string_literal: true

# Conformance oracle for `downshift_env`: run the REAL Ruby CCPool.downshift_env against staged
# snapshots (+ optional calibration cache / fake ccusage for the estimated tier) and emit the two
# byte-checkable outputs so the Go port can diff them: the message line, then the env as sorted JSON.
#
# Snapshots (USAGE_CACHE glob), calibration cache (CCPOOL_CALIB_CACHE), the fake ccusage
# (CCPOOL_CCUSAGE_CMD -> fake-ccusage.sh, CCUSAGE_FIXTURE -> blocks JSON), and the CCPOOL_* knobs are
# staged by the caller via env before load. stdin = {"now": <int>}.
# stdout = "<msg>\n<env-as-sorted-json>" (the run itself exec's, so only this decision is diffable).

require "json"
require_relative "../ccpool"

now = JSON.parse($stdin.read).fetch("now")
env, msg = CCPool.downshift_env(now)

$stdout.binmode
$stdout.write(msg.to_s)
$stdout.write("\n")
$stdout.write(JSON.generate(env.sort.to_h))
