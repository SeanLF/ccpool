# frozen_string_literal: true

# Conformance oracle for prune_history: stage a history file, run the REAL Ruby CCPool.prune_history,
# and emit the rows-removed count plus the resulting file bytes so the Go port can diff both.
#
# Env: CCPOOL_HISTORY (the log path) + CCPOOL_HISTORY_KEEP_DAYS (the cutoff) come from the caller.
# stdin = {now, hist}. stdout envelope (NUL-separated): <removed_count>\0<history_bytes>

require "json"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
now = input.fetch("now")

File.write(ENV.fetch("CCPOOL_HISTORY"), input["hist"].to_s)
removed = CCPool.prune_history(now)

$stdout.binmode
$stdout.write(removed.to_s)
$stdout.write("\0")
$stdout.write(File.binread(ENV.fetch("CCPOOL_HISTORY")))
