# frozen_string_literal: true

# Conformance oracle for `review`: stage transcript .jsonl files under CCPOOL_PROJECTS, run the REAL
# Ruby CCPool.review, and emit its exact stdout bytes so the Go port can diff the rendered readout.
#
# CCPOOL_PROJECTS (the scan root) and CCPOOL_LOW_OUTPUT (the triviality threshold) are frozen into
# constants at require time, so the caller must set them in the process env BEFORE this loads.
# stdin = {"now": <int>, "args": [<days>?], "files": {"<relpath>": {"lines": [obj...], "mtime": int?}}}.
# stdout = the bytes CCPool.review printed.

require "json"
require "stringio"
require "fileutils"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
now = input.fetch("now")
args = input.fetch("args", [])
base = ENV.fetch("CCPOOL_PROJECTS")

input.fetch("files", {}).each do |rel, spec|
  path = File.join(base, rel)
  FileUtils.mkdir_p(File.dirname(path))
  File.write(path, spec.fetch("lines").map { |o| JSON.generate(o) }.join("\n") + "\n")
  t = spec["mtime"] || now
  File.utime(t, t, path)
end

buf = StringIO.new
orig = $stdout
$stdout = buf
begin
  CCPool.review(args, now)
ensure
  $stdout = orig
end

$stdout.binmode
$stdout.write(buf.string)
