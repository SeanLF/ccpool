# frozen_string_literal: true

# Conformance oracle for the $/1% calibration compute. History + a fake ccusage (CCPOOL_CCUSAGE_CMD
# pointing at fake-ccusage.sh, CCUSAGE_FIXTURE at the blocks JSON) are staged by the caller via env;
# this forces a recompute and prints the resulting dpp so the Go port can diff it.
#
# stdin = {"now": <int>}. stdout = the dpp rounded to 4 decimals, or "nil".

require "json"
require_relative "../calibration"

now = JSON.parse($stdin.read).fetch("now")
dpp = Calibration.dollar_per_pct(now, force: true)
$stdout.write(dpp.nil? ? "nil" : format("%.4f", dpp))
