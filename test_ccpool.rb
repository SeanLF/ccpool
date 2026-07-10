#!/usr/bin/env ruby
# frozen_string_literal: true

# Hermetic tests for ccpool. Env is redirected to a temp dir BEFORE requiring the modules
# (their path constants bind at load), so nothing touches real ~/.claude data. No deps.
require "tmpdir"
require "json"
require "fileutils"
require "stringio"
require "time"

TMP = Dir.mktmpdir
ENV["USAGE_CACHE"]        = "#{TMP}/usage-cache.json"
ENV["CCPOOL_HISTORY"]     = "#{TMP}/hist.jsonl"
ENV["CCPOOL_CALIB_CACHE"] = "#{TMP}/calib.json"
ENV["CCPOOL_PROJECTS"]    = "#{TMP}/projects"
require_relative "ccpool"

NOW = Time.now.to_i
$fail = 0
def ok(name, cond)
  puts "#{cond ? 'PASS' : 'FAIL'}  #{name}"
  $fail += 1 unless cond
end

def snap(sid, week: nil, five: nil, captured: NOW, reset_in: 300_000)
  rl = {}
  rl["seven_day"] = { "used_percentage" => week, "resets_at" => NOW + reset_in } if week
  rl["five_hour"] = { "used_percentage" => five, "resets_at" => NOW + 7_200 } if five
  File.write(ENV["USAGE_CACHE"].sub(/\.json\z/, "-#{sid}.json"),
             JSON.generate("captured_at" => captured, "session_id" => sid, "rate_limits" => rl))
end
def clear_snaps = Dir.glob("#{TMP}/usage-cache*.json").each { File.delete(_1) }

def capture(stdin = nil)
  o = $stdout
  i = $stdin
  $stdout = StringIO.new
  $stdin = StringIO.new(stdin) if stdin
  yield
  $stdout.string
ensure
  $stdout = o
  $stdin = i
end

# ---- Pool -------------------------------------------------------------------------
clear_snaps
snap("a", week: 20)
snap("b", week: 35)
ok("Pool.weekly reconciles MAX used on the window", Pool.weekly(NOW)[:used] == 35.0)

clear_snaps
snap("a", week: 90, reset_in: -100)
ok("Pool.weekly drops an already-expired window", Pool.weekly(NOW).nil?)

clear_snaps
snap("a", week: 12)
File.write("#{TMP}/usage-cache-epoch.json",
           JSON.generate("captured_at" => NOW, "rate_limits" => { "seven_day" => { "used_percentage" => 1_783_600_000, "resets_at" => NOW + 300_000 } }))
ok("Pool.weekly ignores leak-bug epoch-in-used% (#52326)", Pool.weekly(NOW)[:used] == 12.0)

clear_snaps
snap("a", week: 105)
ok("Pool.weekly clamps >100 to 100", Pool.weekly(NOW)[:used] == 100.0)

clear_snaps
snap("a", week: 12, captured: NOW - 99_999)
w = Pool.weekly(NOW)
ok("Pool.weekly returns the window + age even when stale (caller tiers it)", w && w[:used] == 12.0 && w[:age] > Pool::STALE)

pc = Pool.pace(60, NOW + 4 * 86_400, NOW) # ~3/7 = 43% of the week elapsed, used 60 => ahead
ok("Pool.pace linear: ahead-of-pace (delta>0) at true elapsed fraction", pc[:delta] > 0 && (pc[:elapsed_pct] - 100.0 * 3 / 7).abs < 1)
ok("Pool.pace linear: under-pace (delta<0)", Pool.pace(5, NOW + 4 * 86_400, NOW)[:delta] < 0)
ok("Pool.pace near-reset exposes to_reset", Pool.pace(50, NOW + 3_600, NOW)[:to_reset] == 3_600)

# ---- Calibration ------------------------------------------------------------------
BND = NOW + 500_000
File.open(ENV["CCPOOL_HISTORY"], "w") { |f| [[NOW - 7_200, 5], [NOW - 3_600, 12], [NOW, 20]].each { |t, w| f.puts JSON.generate("t" => t, "wk" => w, "wk_reset" => BND) } }
runs = Calibration.wk_runs
ok("Calibration.wk_runs detects a monotonic run", runs.size == 1 && runs[0][:dw] == 15)

File.write(ENV["CCPOOL_HISTORY"], JSON.generate("t" => NOW, "wk" => 5, "wk_reset" => BND) + "\n")
ok("Calibration.wk_runs: insufficient history -> []", Calibration.wk_runs.empty?)

b = [{ s: 0, e: 100, cost: 100.0 }]
ok("Calibration.cost_between full overlap", Calibration.cost_between(b, 0, 100) == 100.0)
ok("Calibration.cost_between half overlap prorates", Calibration.cost_between(b, 0, 50) == 50.0)

File.write(ENV["CCPOOL_HISTORY"], "")
Calibration.define_singleton_method(:ccusage_blocks) { raise "ccusage must not be spawned on empty history" }
ok("Calibration.compute skips ccusage when no runs (fresh install)", Calibration.compute.nil?)

File.open(ENV["CCPOOL_HISTORY"], "w") { |f| [[NOW - 7_200, 5], [NOW, 20]].each { |t, w| f.puts JSON.generate("t" => t, "wk" => w, "wk_reset" => BND) } }
Calibration.define_singleton_method(:ccusage_blocks) { [{ s: NOW - 7_200, e: NOW, cost: 150.0 }] } # $150 over dw=15 => $10/1%
ok("Calibration.compute pooled $/1%", (Calibration.compute - 10.0).abs < 0.5)

# ---- Analyzer ---------------------------------------------------------------------
FileUtils.mkdir_p("#{ENV['CCPOOL_PROJECTS']}/p")
def asst(model, out, tools: 0, ts: NOW)
  content = [{ "type" => "text", "text" => "x" }] + Array.new(tools) { { "type" => "tool_use", "name" => "Bash" } }
  JSON.generate("type" => "assistant", "timestamp" => Time.at(ts).utc.iso8601,
                "message" => { "model" => model, "usage" => { "output_tokens" => out }, "content" => content })
end
File.open("#{ENV['CCPOOL_PROJECTS']}/p/s.jsonl", "w") do |f|
  f.puts asst("claude-opus-4-8", 100)            # trivial: little output, no tools -> flagged
  f.puts asst("claude-opus-4-8", 5_000, tools: 3) # substantial -> not flagged
  f.puts asst("claude-sonnet-5", 200)             # cheap model -> not "expensive"
  f.puts asst("<synthetic>", 100)                 # excluded
  f.puts asst("z-ai/glm-5.2", 100)                # router -> excluded
end
r = Analyzer.review(days: 7, now: NOW)
ok("Analyzer counts only expensive Claude turns", r[:exp_turns] == 2)
ok("Analyzer flags the trivial expensive turn", r[:exp_trivial] == 1)

# ---- CCPool decision + IO ---------------------------------------------------------
clear_snaps
snap("a", week: 60, reset_in: 4 * 86_400)
env, = CCPool.downshift_env(NOW)
ok("downshift_env: ahead of pace -> sets subagent model env", env["CLAUDE_CODE_SUBAGENT_MODEL"] == "haiku")

clear_snaps
snap("a", week: 5, reset_in: 4 * 86_400)
env, = CCPool.downshift_env(NOW)
ok("downshift_env: under pace -> no env", env.empty?)

clear_snaps
snap("a", week: 5, five: 90, reset_in: 4 * 86_400) # weekly under pace, but 5h at 90%
env, msg = CCPool.downshift_env(NOW)
ok("downshift_env: 5h saturated -> downshift even if weekly under pace", env["CLAUDE_CODE_SUBAGENT_MODEL"] == "haiku" && msg.include?("5h"))

clear_snaps
env, msg = CCPool.downshift_env(NOW)
ok("downshift_env: no data -> fail open", env.empty? && msg.include?("fail open"))

# 3-tier resolution: fresh / estimated (stale + accrued cost) / stale
clear_snaps
snap("a", week: 30)
ok("resolve_weekly: fresh snapshot -> :fresh", CCPool.resolve_weekly(NOW)[:confidence] == :fresh)

clear_snaps
snap("a", week: 20, captured: NOW - 99_999)
Calibration.define_singleton_method(:dollar_per_pct) { |*| 10.0 }
Calibration.define_singleton_method(:cost_since) { |_a, _b| 50.0 } # $50 accrued since capture
r2 = CCPool.resolve_weekly(NOW)
ok("resolve_weekly: stale + accrued cost -> :estimated, % extrapolated up (20 + 50/10)", r2[:confidence] == :estimated && r2[:used] == 25.0)

Calibration.define_singleton_method(:dollar_per_pct) { |*| nil } # can't calibrate -> can't estimate
ok("resolve_weekly: stale + no calibration -> :stale", CCPool.resolve_weekly(NOW)[:confidence] == :stale)

# near-reset coast: unspent budget is use-it-or-lose-it -> don't downshift, burn it
clear_snaps
Calibration.define_singleton_method(:dollar_per_pct) { |*| 10.0 }
snap("a", week: 90, reset_in: 6 * 3_600) # 6h to reset (< 12h COAST), fresh
env, msg = CCPool.downshift_env(NOW)
ok("downshift_env: near reset -> no downshift (coast/burn it)", env.empty? && msg.include?("burn"))

clear_snaps
File.write(ENV["CCPOOL_HISTORY"], "")
out = capture(JSON.generate("session_id" => "t", "rate_limits" => { "seven_day" => { "used_percentage" => 10, "resets_at" => NOW + 300_000 } })) { CCPool.statusline(NOW) }
ok("statusline renders the rich weekly line", out.include?("wk") && out.include?("10%"))
ok("statusline wrote a snapshot", File.exist?(ENV["USAGE_CACHE"].sub(/\.json\z/, "-t.json")))
ok("statusline seeded history", File.read(ENV["CCPOOL_HISTORY"]).include?('"wk":10'))

clear_snaps
out = capture { CCPool.status(NOW) }
ok("status with no data guides to wire the statusline", out.include?("no data yet"))

# Burn projection integration: a clean monotonic run in history -> a Burn line in status.
File.open(ENV["CCPOOL_HISTORY"], "w") do |f|
  bnd = NOW + 400_000
  [[NOW - 18_000, 5], [NOW - 9_000, 12], [NOW, 20]].each { |t, wv| f.puts JSON.generate("t" => t, "wk" => wv, "wk_reset" => bnd) }
end
clear_snaps
snap("a", week: 20, reset_in: 400_000)
Calibration.define_singleton_method(:dollar_per_pct) { |*| 10.0 }
ok("status shows a burn projection from a clean run", capture { CCPool.status(NOW) }.include?("Burn"))

puts($fail.zero? ? "\nAll green." : "\n#{$fail} FAILED.")
exit($fail.zero? ? 0 : 1)
