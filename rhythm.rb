# frozen_string_literal: true

# `ccpool rhythm` -- read-only diagnostic: measure your OWN circadian work rhythm from the
# transcript corpus and SUGGEST a pace profile. It never auto-applies -- you run it, read it,
# and optionally set the env knobs it prints. That opt-in stance is deliberate: auto-detecting
# a schedule and feeding it into pace would be confidently wrong exactly when it matters least.
#
# The killer insight (why this is a suggester, not a detector wired into pace): detection is
# SELF-OBVIATING. Both the value of a schedule profile AND its detectability scale with R, the
# circular resultant length (rhythm strength). When R is high the rhythm is sharp and easy to
# read AND a schedule genuinely helps; when R is low (continuous 24/7 loops, R~0.3) a schedule
# can't help and `even` is already the right default. So the honest output is: high R -> here's
# your window; low R -> stick with `even`. Either branch is truthful.
#
# Timezone travel would poison a naive all-time hour-of-day histogram (Ottawa-9am smears into
# Tokyo-9am). We sidestep the whole change-point-detection rabbit hole with a RECENCY WINDOW
# (default 30d = your current TZ regime) rendered in the CURRENT machine's local time. No BOCPD,
# no phase-shift tracking -- the window IS the regime.
#
# Streams via a regex timestamp extract (no full JSON parse) for speed over ~thousands of files.
# Fails OPEN: any unreadable file/line is skipped; no activity -> an honest "nothing to read".
require "date"
require "time"
require_relative "clock"

module Rhythm
  PROJECTS  = File.expand_path(ENV["CCPOOL_PROJECTS"] || "~/.claude/projects")
  TS        = /"timestamp":"(\d{4})-(\d\d)-(\d\d)T(\d\d):/ # ISO8601 UTC date + hour
  WINDOW    = (ENV["CCPOOL_RHYTHM_WINDOW"] || "30").to_i   # recency window (days) = current TZ regime
  R_STRONG  = (ENV["CCPOOL_RHYTHM_R"] || "0.5").to_f       # R >= this -> rhythm sharp enough to schedule to
  PEAK_FRAC = (ENV["CCPOOL_RHYTHM_PEAK"] || "0.25").to_f   # a bucket is "active" at >= this fraction of the peak
  BLOCKS    = " ▁▂▃▄▅▆▇█"

  module_function

  # Scan the corpus -> {hours: [24], wdays: [7], n:} in the CURRENT machine's LOCAL frame,
  # restricted to the last WINDOW days. Never raises.
  def scan(now = Time.now.to_i)
    off_h = Time.at(now).utc_offset / 3600 # whole-hour offset (matches Profile's integral convention)
    today = Time.at(now).to_date
    hours = Array.new(24, 0)
    wdays = Array.new(7, 0)
    n = 0

    Dir.glob("#{PROJECTS}/**/*.jsonl").each do |f|
      File.foreach(f) do |line|
        next unless (m = TS.match(line))

        h = m[4].to_i
        roll = (h + off_h).div(24) # offset may push local past midnight (+/-1 day)
        date = Date.new(m[1].to_i, m[2].to_i, m[3].to_i) + roll # UTC date -> local date
        next unless (today - date).to_i.between?(0, WINDOW)

        hours[(h + off_h) % 24] += 1 # local hour-of-day
        wdays[date.wday] += 1        # local weekday
        n += 1
      rescue StandardError
        next
      end
    rescue StandardError
      next # a file that vanished/errored on open must not discard the whole scan (fail open per-file)
    end
    { hours: hours, wdays: wdays, n: n }
  rescue StandardError
    { hours: Array.new(24, 0), wdays: Array.new(7, 0), n: 0 }
  end

  # Circular stats over the 24h clock. => [mean_hour|nil, R, n]. R~1 = one sharp time-of-day;
  # R~0 = spread evenly (loops filling the clock).
  def circ(counts)
    n = counts.sum
    return [nil, 0.0, 0] if n.zero?

    s = c = 0.0
    counts.each_with_index do |cnt, h|
      a = 2 * Math::PI * h / 24
      s += cnt * Math.sin(a)
      c += cnt * Math.cos(a)
    end
    [(Math.atan2(s, c) / (2 * Math::PI) * 24) % 24, Math.sqrt((s * s) + (c * c)) / n, n]
  end

  # Indices of buckets at >= PEAK_FRAC of the peak bucket.
  def active(counts)
    mx = counts.max
    return [] if mx.zero?

    counts.each_index.select { |i| counts[i] >= mx * PEAK_FRAC }
  end

  # Smallest CIRCULAR arc covering all active hours -> [h0, h1) for CCPOOL_WAKE_HOURS. Found by
  # locating the largest empty gap on the clock; the wake window is everything else. h1 may be 24
  # (== end of day). Returns a wrapping window (h1 <= h0) when activity straddles midnight -- the
  # caller must reject that, since Profile's [h0, h1) can't represent it.
  def wake_window(active_hours)
    return [0, 24] if active_hours.empty? || active_hours.size >= 24

    sorted = active_hours.sort
    best_gap = -1
    gap_start = sorted.first
    sorted.each_with_index do |a, i|
      b = sorted[(i + 1) % sorted.size]
      gap = (b - a - 1) % 24 # empty hours strictly between a and the next active hour, going forward
      if gap > best_gap
        best_gap = gap
        gap_start = a
      end
    end
    h0 = (gap_start + best_gap + 1) % 24 # first active hour after the largest gap
    h1 = h0 + (24 - best_gap)            # window length = clock minus the gap
    h1 -= 24 if h1 > 24                  # keep h1 in (h0, 24]; a wrap yields h1 <= h0
    [h0, h1]
  end

  # Weekdays (0=Sun..6=Sat) at >= PEAK_FRAC of the busiest day; all-7 means no day pattern.
  def active_days(wdays) = active(wdays)

  # Compact int set: contiguous run -> "a-b", single -> "a", else comma list.
  def fmt_set(xs)
    xs = xs.sort
    return xs.first.to_s if xs.size == 1
    return "#{xs.first}-#{xs.last}" if xs == (xs.first..xs.last).to_a

    xs.join(",")
  end

  def compact(n) = n >= 1000 ? "#{(n / 1000.0).round}k" : n.to_s

  def spark(counts)
    mx = counts.max.to_f
    counts.map { |c| BLOCKS[mx.zero? ? 0 : (c / mx * 8).round] }.join
  end

  # The R-gated recommendation line. Low R -> `even`; high R -> a concrete window (+ work-days if
  # there's a day pattern), unless the window straddles midnight (unrepresentable -> honest `even`).
  def suggestion(r, hours, wdays)
    return "CCPOOL_PACE_PROFILE=even   (R too low for a schedule to help)" if r < R_STRONG

    h0, h1 = wake_window(active(hours))
    return "CCPOOL_PACE_PROFILE=even   (strong, but the rhythm straddles midnight -- no clean day-window)" if h1 <= h0

    parts = ["CCPOOL_WAKE_HOURS=#{h0}-#{h1}"]
    days = active_days(wdays)
    parts << "CCPOOL_WORK_DAYS=#{fmt_set(days)}" unless days.size == 7
    "#{parts.join(' ')}   (strong rhythm -- pace to it)"
  end

  # => array of output lines. Never raises.
  def report(now = Time.now.to_i)
    d = scan(now)
    return ["rhythm: no transcript activity in the last #{WINDOW}d -- nothing to read."] if d[:n].zero?

    _, r, n = circ(d[:hours])
    busiest = d[:hours].index(d[:hours].max)
    quietest = d[:hours].index(d[:hours].min)
    off = format("UTC%+d", Time.at(now).utc_offset / 3600)
    # Lead with the plain read; keep R (circular resultant, 0=flat..1=one sharp peak) as a detail.
    headline = r >= R_STRONG ? "strong day/night rhythm" : "weak/continuous rhythm -- your loops fill the clock"

    ["rhythm (last #{WINDOW}d · #{compact(n)} messages · local #{off})",
     format("  %s  (R=%.2f)", headline, r),
     "  activity  #{spark(d[:hours])}  midnight -> midnight",
     "  busiest #{Clock.hour(busiest)} · quietest #{Clock.hour(quietest)} · active ~#{active(d[:hours]).size}h/day",
     "  suggested: #{suggestion(r, d[:hours], d[:wdays])}"]
  end
end
