# frozen_string_literal: true

# Conformance oracle for `ccpool init` (the Init module): run the REAL Ruby Init.run against a staged
# settings.json (or symlink) and emit everything the Go port must reproduce byte-for-byte -- the
# captured stdout, the exit code, whether the path is still a symlink, and the resulting file bytes.
#
# The filesystem is staged by the caller via CCPOOL_SETTINGS (+ USAGE_CACHE so the preview finds no
# snapshot, + CCPOOL_CCUSAGE_CMD so the ccusage-on-PATH branch is deterministic). stdin = {argv, now}.
# stdout envelope (NUL-separated, raw bytes so the settings JSON isn't re-escaped):
#   <captured_stdout>\0<exit_code>\0<is_symlink 0|1>\0<exists 0|1>\0<settings_bytes>

require "json"
require "stringio"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
argv = input.fetch("argv")
now  = input.fetch("now")

settings = ENV.fetch("CCPOOL_SETTINGS")

captured = StringIO.new
real_stdout = $stdout
$stdout = captured
code = 0
begin
  Init.run(argv, now)
rescue SystemExit => e
  code = e.status
ensure
  $stdout = real_stdout
end

is_symlink = File.symlink?(settings) ? "1" : "0"
exists     = File.exist?(settings) ? "1" : "0"
body       = File.exist?(settings) ? File.binread(settings) : ""

$stdout.binmode
$stdout.write(captured.string)
$stdout.write("\0")
$stdout.write(code.to_s)
$stdout.write("\0")
$stdout.write(is_symlink)
$stdout.write("\0")
$stdout.write(exists)
$stdout.write("\0")
$stdout.write(body)
