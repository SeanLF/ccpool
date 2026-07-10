# frozen_string_literal: true

# Conformance oracle for seed_history: initialize the history file, run the REAL Ruby append, and
# emit the resulting file bytes so the Go port can diff the on-disk history log byte-for-byte.
#
# Env (CCPOOL_HISTORY, USAGE_TIER, CCPOOL_HISTORY_MIN_INTERVAL) comes from the caller's process
# environment before load (Ruby freezes them into constants). stdin = {"now", "payload", "hist"}.
# stdout = the exact bytes of the history file after the append.

require "json"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
now = input.fetch("now")
payload = input.fetch("payload")

File.write(ENV.fetch("CCPOOL_HISTORY"), input["hist"].to_s)
CCPool.seed_history(payload, now)

$stdout.binmode
$stdout.write(File.read(ENV.fetch("CCPOOL_HISTORY")))
