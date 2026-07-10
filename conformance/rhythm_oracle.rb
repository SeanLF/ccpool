# frozen_string_literal: true

# Conformance oracle for `rhythm`: run the REAL Ruby CCPool.rhythm against a staged transcript corpus
# and emit the exact output bytes so the Go port can diff its rendered `ccpool rhythm` output.
#
# The caller stages the transcript jsonl under CCPOOL_PROJECTS and pins TZ (+ any CCPOOL_RHYTHM_*
# knobs) via env before load. stdin = {"now": <int>}. stdout = the `puts`-joined report lines.

require "json"
require_relative "../ccpool"

input = JSON.parse($stdin.read)
$stdout.binmode
CCPool.rhythm(input.fetch("now"))
