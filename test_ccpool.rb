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
ENV["TMPDIR"]             = TMP # keep `warn`'s throttle markers hermetic
ENV["CCPOOL_STATUSLINE_LOG"] = "#{TMP}/statusline.log"
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

# ---- Profile (activity-weight pace, two orthogonal knobs) --------------------------
ok("Profile.int_set parses a range", Profile.int_set("1-5", []) == [1, 2, 3, 4, 5])
ok("Profile.int_set parses a list", Profile.int_set("1,3,5", []) == [1, 3, 5])
ok("Profile.int_set all-garbage -> default (not empty)", Profile.int_set("abc", [1, 2, 3]) == [1, 2, 3])
ok("Profile.weights parses + pads to size", Profile.weights("1,0.5", 4) == [1.0, 0.5, 1.0, 1.0])
ok("Profile.weights garbled -> all 1.0", Profile.weights("x,y", 3) == [1.0, 1.0, 1.0])
ok("Profile.hours parses a range", Profile.hours("8-16", [9, 17]) == [8, 16])
ok("Profile.hours no-dash -> default (must not crash)", Profile.hours("9", [9, 17]) == [9, 17])
ok("Profile.hours blank/garbled -> default", Profile.hours("", [9, 17]) == [9, 17] && Profile.hours("x-y", [9, 17]) == [9, 17])

# default (no env in test) is 24/7 uniform -> plain linear pace
ok("Profile default is uniform 24/7 (== plain linear)", Profile.uniform? && !Profile.scheduled?)
ok("Profile even == plain time fraction (3/7 of the window)",
   (Profile.elapsed_fraction(NOW - 3 * 86_400, NOW, NOW + 4 * 86_400) - 3.0 / 7).abs < 0.001)

# the two knobs, as resolved configs: weekdays (days only), workhours (days + wake window)
weekdays  = Profile::Config.new((1..5).to_a, 0, 24, nil)
workhours = Profile::Config.new((1..5).to_a, 9, 17, nil)
ok("Profile.weight: work day + waking hour -> full", Profile.weight(2, 12, workhours) == 1.0)
ok("Profile.weight: work day but sleeping hour -> FLOOR", Profile.weight(2, 3, workhours) == Profile::FLOOR)
ok("Profile.weight: off day -> FLOOR", Profile.weight(6, 12, weekdays) == Profile::FLOOR)
ok("Profile.uniform? false for a scheduled config", !Profile.uniform?(weekdays))

# Anchor a window to a real local Monday 00:00 so day-of-week weighting is deterministic.
base = Time.local(2026, 3, 2, 0, 0, 0)
mon  = base + ((1 - base.wday) % 7) * 86_400
mon  = Time.local(mon.year, mon.month, mon.day, 0, 0, 0)
wstart = mon.to_i
wreset = wstart + 7 * 86_400
fri_end = wstart + 5 * 86_400 # ~Fri 24:00 local: all weekday mass elapsed, weekend still ahead
ev = Profile.elapsed_fraction(wstart, fri_end, wreset)            # DEFAULT (uniform) ~0.714
wd = Profile.elapsed_fraction(wstart, fri_end, wreset, weekdays)  # ~0.94 (weekend deferred)
ok("Profile weekdays: end-of-Friday reads further along than even", wd > ev + 0.1)
ok("Profile: 0 at window start, 1 at reset (weekdays)",
   Profile.elapsed_fraction(wstart, wstart, wreset, weekdays) == 0.0 &&
   (Profile.elapsed_fraction(wstart, wreset, wreset, weekdays) - 1.0).abs < 1e-9)

# ---- Runway (working-hours of pool left before weekly reset) -----------------------
# even config (test default) -> active-hours == wall-hours. Run: 10% over 10h -> 1%/working-h.
rproj = { dpct: 10.0, first_t: NOW - 36_000, last_t: NOW }
rb = Runway.estimate(90, NOW + 40 * 3_600, rproj, NOW) # 10% left, ~40 working-h to reset -> budget binds
ok("Runway budget-limited when budget < calendar", rb[:bind] == :budget && (rb[:budget_h] - 10).abs < 0.5)
ok("Runway range brackets the estimate (fat-tail band)", rb[:low] < rb[:hours] && rb[:hours] <= rb[:high])
rc = Runway.estimate(20, NOW + 40 * 3_600, rproj, NOW) # 80% left over ~40 working-h -> week wins
ok("Runway calendar-limited when budget outlasts the week", rc[:bind] == :calendar)
ok("Runway nil without a burn signal", Runway.estimate(50, NOW + 40 * 3_600, nil, NOW).nil?)
ok("Runway.phrase budget-limited surfaces working-hours", Runway.phrase(rb, 40 * 3_600).include?("working-hours"))
ok("Runway.phrase calendar-limited says burn freely", Runway.phrase(rc, 40 * 3_600).include?("burn freely"))
# FLOOR-inflation guard: a 4h burst entirely in a workhours user's off-hours (1-5am) would
# integrate to ~4h*FLOOR=0.6h; the density floor keeps active-hours >= 0.5*wall (2h), so the
# per-active-hour rate can't be inflated ~6.7x into a false "throttle imminent".
wh = Profile::Config.new((1..5).to_a, 9, 17, nil)
night0 = Time.local(2026, 3, 3, 1, 0, 0).to_i # Tue 1am local, off-hours for 9-17
ok("Runway.active_hours floors an off-schedule burst at 0.5*wall (not ~FLOOR)",
   (Runway.active_hours(night0, night0 + 4 * 3_600, wh) - 2.0).abs < 0.05)
ok("Runway.active_hours is a no-op for even (active == wall)",
   (Runway.active_hours(night0, night0 + 4 * 3_600) - 4.0).abs < 0.05)

# ---- Calibration ------------------------------------------------------------------
BND = NOW + 500_000
File.open(ENV["CCPOOL_HISTORY"], "w") { |f| [[NOW - 7_200, 5], [NOW - 3_600, 12], [NOW, 20]].each { |t, w| f.puts JSON.generate("t" => t, "wk" => w, "wk_reset" => BND) } }
runs = Calibration.wk_runs
ok("Calibration.wk_runs detects a monotonic run", runs.size == 1 && runs[0][:dw] == 15)

File.write(ENV["CCPOOL_HISTORY"], JSON.generate("t" => NOW, "wk" => 5, "wk_reset" => BND) + "\n")
ok("Calibration.wk_runs: insufficient history -> []", Calibration.wk_runs.empty?)

# ses-keyed dedup writes many flat-wk rows; wk_runs must ignore them (record wk CHANGES only),
# else a run's t1 stretches to the flat tail and cost_between inflates the $/1% calibration.
File.open(ENV["CCPOOL_HISTORY"], "w") do |f|
  f.puts JSON.generate("t" => NOW - 14_400, "wk" => 5, "wk_reset" => BND, "ses" => 10)
  f.puts JSON.generate("t" => NOW - 7_200, "wk" => 20, "wk_reset" => BND, "ses" => 50) # wk hits 20 here
  f.puts JSON.generate("t" => NOW - 3_600, "wk" => 20, "wk_reset" => BND, "ses" => 80) # flat wk, ses moved
  f.puts JSON.generate("t" => NOW, "wk" => 20, "wk_reset" => BND, "ses" => 95)          # flat wk, ses moved
end
fr = Calibration.wk_runs
ok("Calibration.wk_runs ends a run at the last wk CHANGE, not the flat ses-padded tail",
   fr.size == 1 && fr[0][:dw] == 15 && fr[0][:t1] < NOW - 3_600)

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

# ---- Rhythm -----------------------------------------------------------------------
# Pure circular stats + window math (TZ-independent).
ok("circ: single spike -> R~1", (Rhythm.circ(Array.new(24, 0).tap { _1[14] = 100 })[1] - 1.0).abs < 0.01)
ok("circ: uniform -> R~0", Rhythm.circ(Array.new(24, 5))[1] < 0.01)
ok("wake_window: daytime block 9..16 -> [9,17]", Rhythm.wake_window((9..16).to_a) == [9, 17])
ok("wake_window: straddles midnight -> wraps (h1<=h0)", Rhythm.wake_window([22, 23, 0, 1, 2]).then { |a| a[1] <= a[0] })
ok("fmt_set: contiguous -> range", Rhythm.fmt_set([1, 2, 3, 4, 5]) == "1-5")
ok("fmt_set: gapped -> comma list", Rhythm.fmt_set([6, 0, 1, 5]) == "0,1,5,6")
ok("suggestion: low R -> even", Rhythm.suggestion(0.3, Array.new(24, 5), Array.new(7, 5)).include?("PACE_PROFILE=even"))
ok("suggestion: strong daytime -> WAKE_HOURS",
   Rhythm.suggestion(0.9, Array.new(24, 0).tap { |a| (9..16).each { |h| a[h] = 50 } }, Array.new(7, 5)).include?("CCPOOL_WAKE_HOURS=9-17"))

# Integration: a corpus concentrated in a local-hour block reads as a strong rhythm. Pin TZ=UTC
# so local == the UTC hour we write, making the assertion machine-independent.
ENV["TZ"] = "UTC"
FileUtils.mkdir_p("#{ENV['CCPOOL_PROJECTS']}/rh")
File.open("#{ENV['CCPOOL_PROJECTS']}/rh/r.jsonl", "w") do |f|
  0.upto(6) do |d|                      # last 7 days
    [13, 14, 15, 16].each do |h|        # busy 1pm-4pm UTC
      20.times { f.puts JSON.generate("timestamp" => Time.at(NOW - d * 86_400).utc.strftime("%Y-%m-%dT#{format('%02d', h)}:30:00Z")) }
    end
  end
end
lines = Rhythm.report(NOW)
ok("Rhythm.report: header names the window + local TZ", lines.first.match?(/\Arhythm \(last 30d · .+ messages · local UTC\+0\)/))
ok("Rhythm.report: strong block -> strong headline + a schedule suggestion",
   lines.any? { _1.include?("strong day/night") } && lines.any? { _1.include?("CCPOOL_WAKE_HOURS=") })
ok("Rhythm.report: R demoted to a trailing detail, not the lead", lines.any? { _1.match?(/\A  strong day\/night rhythm  \(R=/) })
ok("Rhythm.report: busiest hour rendered 24h, in the concentrated block", lines.any? { _1.match?(/busiest 1[3-6]:00/) })

# Recency filter must use the LOCAL date, not the raw UTC date. In a negative-offset zone a late
# local-evening event has already rolled to the next UTC calendar day; filtering on the UTC date
# would drop it from "the last 30d". TZ=UTC-4, "now" = 10pm local (= 2am UTC next day).
ENV["TZ"] = "Etc/GMT+4" # POSIX sign-inverted -> UTC-4, no DST
evening = Time.new(2026, 7, 10, 22, 0, 0, "-04:00").to_i
File.open("#{ENV['CCPOOL_PROJECTS']}/rh/rollover.jsonl", "w") do |f|
  50.times { f.puts JSON.generate("timestamp" => "2026-07-11T02:30:00Z") } # UTC July 11, but local July 10 22:00
end
rolled = Rhythm.scan(evening)
ok("Rhythm.scan: local-evening event (next UTC day) still counts as today", rolled[:hours][22] >= 50)
ENV["TZ"] = nil

# ---- Clock ------------------------------------------------------------------------
ok("Clock.resolve: default -> 24h", Clock.resolve(nil) == 24 && Clock.resolve("24") == 24)
ok("Clock.resolve: explicit 12h", Clock.resolve("12") == 12)
ok("Clock.resolve: garbage -> 24h (predictable default)", Clock.resolve("banana") == 24)
ok("Clock.resolve: auto -> a concrete 12 or 24", [12, 24].include?(Clock.resolve("auto")))
ok("Clock.hour: 24h formatting", Clock.hour(18, h12: false) == "18:00" && Clock.hour(0, h12: false) == "00:00")
ok("Clock.hour: 12h formatting", Clock.hour(18, h12: true) == "6pm" && Clock.hour(0, h12: true) == "12am" && Clock.hour(12, h12: true) == "12pm")
ok("Clock.time: 24h vs 12h", Clock.time(Time.new(2026, 7, 10, 22, 30), h12: false) == "22:30" &&
                             Clock.time(Time.new(2026, 7, 10, 22, 30), h12: true) == "10:30pm")

# ---- help + statusline preview ----------------------------------------------------
help = capture { CCPool.help }
ok("help lists every subcommand", %w[status check rhythm run review statusline warn prune].all? { help.include?("  #{_1}") })
ok("help names the clock knob", help.include?("CCPOOL_CLOCK"))

clear_snaps
snap("prev", week: 40, reset_in: 3 * 86_400)
preview = capture { CCPool.preview_statusline(NOW) }  # bypasses the tty gate -> renders newest snapshot
ok("statusline preview renders from a snapshot", preview.include?("40%") || preview.match?(/\d+%/))
clear_snaps
nowhere = capture { CCPool.preview_statusline(NOW) }   # no snapshots -> quiet, no crash, no stdout
ok("statusline preview with no snapshot -> no stdout (guidance goes to stderr)", nowhere.strip.empty?)

# NO_COLOR contract. COLOR resolves at load, so exercise it in subprocesses (child inherits the
# hermetic TMP env). Payload on stdin -> the real render path.
sl_payload = JSON.generate("session_id" => "t", "rate_limits" => { "seven_day" => { "used_percentage" => 30, "resets_at" => NOW + 300_000 } })
sl_run = lambda do |env|
  IO.popen(env.merge("PATH" => ENV["PATH"]), ["ruby", "#{Dir.pwd}/ccpool.rb", "statusline"], "r+") do |io|
    io.write(sl_payload); io.close_write; io.read
  end
end
ok("statusline colours by default", sl_run.call("NO_COLOR" => nil, "TERM" => "xterm-256color").include?("\e["))
ok("NO_COLOR strips all ANSI", !sl_run.call("NO_COLOR" => "1", "TERM" => "xterm-256color").include?("\e["))
ok("NO_COLOR='' KEEPS colour (empty is the no-color.org exception)", sl_run.call("NO_COLOR" => "", "TERM" => "xterm-256color").include?("\e["))
ok("TERM=dumb strips all ANSI", !sl_run.call("NO_COLOR" => nil, "TERM" => "dumb").include?("\e["))

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

# history write-throttle: 5h-only moves are rate-limited; weekly moves always land.
clear_snaps
File.write(ENV["CCPOOL_HISTORY"], "")
pa = JSON.generate("session_id" => "tw", "rate_limits" => { "seven_day" => { "used_percentage" => 10, "resets_at" => NOW + 300_000 }, "five_hour" => { "used_percentage" => 20, "resets_at" => NOW + 7_200 } })
pb = JSON.generate("session_id" => "tw", "rate_limits" => { "seven_day" => { "used_percentage" => 10, "resets_at" => NOW + 300_000 }, "five_hour" => { "used_percentage" => 30, "resets_at" => NOW + 7_200 } }) # 5h moved
pc = JSON.generate("session_id" => "tw", "rate_limits" => { "seven_day" => { "used_percentage" => 11, "resets_at" => NOW + 300_000 }, "five_hour" => { "used_percentage" => 30, "resets_at" => NOW + 7_200 } }) # weekly moved
capture(pa) { CCPool.statusline(NOW) }
capture(pb) { CCPool.statusline(NOW + 10) }  # 5h move 10s later (< 60s) -> throttled
ok("history throttles a 5h-only write inside the interval", File.readlines(ENV["CCPOOL_HISTORY"]).size == 1)
capture(pb) { CCPool.statusline(NOW + 120) } # 5h move past the interval -> lands
ok("history records the 5h move once the interval passes", File.readlines(ENV["CCPOOL_HISTORY"]).size == 2)
capture(pc) { CCPool.statusline(NOW + 125) } # weekly moved 5s later -> always lands
ok("history always records a weekly move regardless of throttle", File.readlines(ENV["CCPOOL_HISTORY"]).size == 3)

# opt-in history prune: drop rows older than KEEP_DAYS, keep raw when keep<=0.
File.open(ENV["CCPOOL_HISTORY"], "w") do |f|
  f.puts JSON.generate("t" => NOW - 40 * 86_400, "wk" => 5)  # 40d old -> prunable (>30d default)
  f.puts JSON.generate("t" => NOW - 5 * 86_400, "wk" => 10)  # recent -> kept
  f.puts JSON.generate("t" => NOW, "wk" => 15)
end
ok("prune_history keeps raw forever when keep<=0", CCPool.prune_history(NOW, 0) == 0 && File.readlines(ENV["CCPOOL_HISTORY"]).size == 3)
ok("prune_history drops rows older than keep-days, keeps recent",
   CCPool.prune_history(NOW, 30) == 1 && File.readlines(ENV["CCPOOL_HISTORY"]).size == 2)

clear_snaps
snap("old", week: 5)
snap("new", week: 5)
File.utime(NOW - 7_200, NOW - 7_200, ENV["USAGE_CACHE"].sub(/\.json\z/, "-old.json")) # 2h old > KEEP
CCPool.prune_caches(NOW)
ok("prune removes stale session snapshots, keeps fresh",
   !File.exist?(ENV["USAGE_CACHE"].sub(/\.json\z/, "-old.json")) && File.exist?(ENV["USAGE_CACHE"].sub(/\.json\z/, "-new.json")))

clear_snaps
out = capture { CCPool.status(NOW) }
ok("status with no data guides to wire the statusline", out.include?("no data yet"))

# statusline parity: anomaly log + per-session, ses-keyed history dedup
File.write(ENV["CCPOOL_STATUSLINE_LOG"], "")
Statusline.typed?({ "x" => "str" }, "x", Numeric, "x.field")
ok("typed? logs a present-but-wrong-type field", File.read(ENV["CCPOOL_STATUSLINE_LOG"]).include?("x.field is String"))
ok("typed? is silent on a missing key", Statusline.typed?({}, "y", Numeric, "y") == false && !File.read(ENV["CCPOOL_STATUSLINE_LOG"]).include?("[warn] y "))

clear_snaps
File.write(ENV["CCPOOL_HISTORY"], "")
h1 = JSON.generate("session_id" => "h", "rate_limits" => { "seven_day" => { "used_percentage" => 10, "resets_at" => NOW + 300_000 }, "five_hour" => { "used_percentage" => 20, "resets_at" => NOW + 7_200 } })
h2 = JSON.generate("session_id" => "h", "rate_limits" => { "seven_day" => { "used_percentage" => 10, "resets_at" => NOW + 300_000 }, "five_hour" => { "used_percentage" => 25, "resets_at" => NOW + 7_200 } }) # wk flat, 5h moved
capture(h1) { CCPool.statusline(NOW) }
capture(h1) { CCPool.statusline(NOW) } # identical -> deduped
ok("history dedups an identical wk+ses render", File.readlines(ENV["CCPOOL_HISTORY"]).size == 1)
capture(h2) { CCPool.statusline(NOW + 120) } # 5h-only move past the throttle -> must record despite flat wk
ok("history records a 5h-only move (dedup keyed on ses, not just wk)", File.readlines(ENV["CCPOOL_HISTORY"]).size == 2)

# Burn projection integration: a clean monotonic run in history -> a Burn line in status.
File.open(ENV["CCPOOL_HISTORY"], "w") do |f|
  bnd = NOW + 400_000
  [[NOW - 18_000, 5], [NOW - 9_000, 12], [NOW, 20]].each { |t, wv| f.puts JSON.generate("t" => t, "wk" => wv, "wk_reset" => bnd) }
end
clear_snaps
snap("a", week: 20, reset_in: 400_000)
Calibration.define_singleton_method(:dollar_per_pct) { |*| 10.0 }
status_out = capture { CCPool.status(NOW) }
ok("status shows a burn projection from a clean run", status_out.include?("Burn"))
ok("status shows a working-hours runway from a clean run", status_out.include?("Runway"))

# ---- Warn (situational-awareness hook) --------------------------------------------
clear_snaps
snap("a", week: 60, reset_in: 4 * 86_400) # 60% used, ~43% elapsed -> ahead of pace
ok("warn: ahead of pace -> pace signal", Warn.signals(Pool.load_snapshots, nil, NOW).any? { _1.key == "pace" })

clear_snaps
snap("a", week: 5, reset_in: 4 * 86_400)
ok("warn: under pace -> no pace signal", Warn.signals(Pool.load_snapshots, nil, NOW).none? { _1.key == "pace" })

clear_snaps
snap("a", week: 5, five: 90, reset_in: 4 * 86_400) # weekly fine, but 5h at 90%
ok("warn: 5h near full -> session signal", Warn.signals(Pool.load_snapshots, nil, NOW).any? { _1.key == "session" })

clear_snaps
snap("a", week: 98, reset_in: 40_000) # ahead, but reset < COAST -> use-it-or-lose-it, don't nag
ok("warn: ahead but near reset (coast) -> no pace signal", Warn.signals(Pool.load_snapshots, nil, NOW).none? { _1.key == "pace" })

clear_snaps
snap("a", week: 60, reset_in: 4 * 86_400, captured: NOW - 99_999) # ahead but stale
ok("warn: stale data -> silent (fail open)", Warn.signals(Pool.load_snapshots, nil, NOW).empty?)

clear_snaps
File.write(ENV["USAGE_CACHE"].sub(/\.json\z/, "-me.json"),
           JSON.generate("captured_at" => NOW, "session_id" => "me",
                         "context_window" => { "used_percentage" => 90, "context_window_size" => 200_000 },
                         "rate_limits" => {}))
csig = Warn.signals(Pool.load_snapshots, "me", NOW)
ok("warn: own context near compaction -> ctx signal", csig.any? { _1.key == "ctx" })
ok("warn: context is session-local (ignored for another session)", Warn.signals(Pool.load_snapshots, "other", NOW).none? { _1.key == "ctx" })

# context nag keys off ABSOLUTE headroom, not a flat % -- a 1M window at 89% (~110k free) mustn't nag.
def ctxsnap(sid, pct, size)
  File.write(ENV["USAGE_CACHE"].sub(/\.json\z/, "-#{sid}.json"),
             JSON.generate("captured_at" => NOW, "session_id" => sid,
                           "context_window" => { "used_percentage" => pct, "context_window_size" => size }, "rate_limits" => {}))
end
clear_snaps; ctxsnap("big", 89, 1_000_000)
ok("warn: 1M window at 89% (~110k free) -> no context nag", Warn.signals(Pool.load_snapshots, "big", NOW).none? { _1.key == "ctx" })
clear_snaps; ctxsnap("full", 98, 1_000_000)
ok("warn: 1M window at 98% (~20k free) -> context nag", Warn.signals(Pool.load_snapshots, "full", NOW).any? { _1.key == "ctx" })
clear_snaps; ctxsnap("sm", 85, 200_000)
ok("warn: 200k window at 85% (30k free) -> context nag (unchanged)", Warn.signals(Pool.load_snapshots, "sm", NOW).any? { _1.key == "ctx" })

sig = [Warn::Sig.new("pace", Warn::THROTTLE, "TXT")]
ok("warn.emit: UserPromptSubmit emits plain text", Warn.emit(sig, "UserPromptSubmit", "u", NOW) == "TXT")
first = Warn.emit(sig, "PostToolUse", "s", NOW) # distinct session -> own marker
ok("warn.emit: PostToolUse emits additionalContext JSON", first && JSON.parse(first).dig("hookSpecificOutput", "additionalContext") == "TXT")
ok("warn.emit: PostToolUse throttles a repeat within the window", Warn.emit(sig, "PostToolUse", "s", NOW).nil?)

clear_snaps
ok("warn.run: no data -> nil (fail open)", Warn.run({ "hook_event_name" => "UserPromptSubmit" }, NOW).nil?)

# ---- Check (keep-going/stop verdict) ----------------------------------------------
File.write(ENV["CCPOOL_HISTORY"], "")
def verdict_of(now = NOW)
  lines, = Check.report(now)
  (lines.grep(/^VERDICT/).first || "").sub("VERDICT  ", "")
end

clear_snaps
lines, code = Check.report(NOW)
ok("check: no data -> exit 2 + guidance", code == 2 && lines.join.include?("No usage snapshots"))

clear_snaps
snap("a", week: 45) # ~50% elapsed default, 45% used -> healthy, near pace
ok("check: healthy weekly -> KEEP GOING", verdict_of.start_with?("KEEP GOING"))

clear_snaps
snap("a", week: 95) # far from reset (default ~3.5d)
ok("check: weekly nearly spent, far from reset -> WIND DOWN", verdict_of.start_with?("WIND DOWN"))

clear_snaps
snap("a", week: 95, reset_in: 6 * 3_600) # nearly spent but resets soon
ok("check: weekly nearly spent, near reset -> COAST", verdict_of.start_with?("COAST"))

clear_snaps
snap("a", week: 50, five: 95) # 5h almost full, weekly has room
ok("check: 5h full, weekly has room -> SESSION-LIMITED", verdict_of.start_with?("SESSION-LIMITED"))

clear_snaps
snap("a", week: 60, reset_in: 4 * 86_400) # ahead of linear pace, not near reset, little forfeit
ok("check: ahead of pace -> PACE DOWN", verdict_of.start_with?("PACE DOWN"))

clear_snaps
snap("a", week: 20) # lots unspent with days left -> burn-down nudge
ok("check: large unspent surplus -> BURN DOWN", verdict_of.start_with?("BURN DOWN"))

clear_snaps
snap("a", five: 50) # 5h live + healthy, but NO weekly window -> must not claim weekly "healthy"
v = verdict_of
ok("check: weekly window absent -> KEEP GOING flags it unknown, not 'healthy'", v.start_with?("KEEP GOING") && v.include?("weekly unknown"))

puts($fail.zero? ? "\nAll green." : "\n#{$fail} FAILED.")
exit($fail.zero? ? 0 : 1)
