# frozen_string_literal: true

# Weekly burn-rate projection from the rate-limit history the status line records
# (~/.claude/rate-limit-history.jsonl, one JSON sample per change).
#
# Reset-robust by design. Anthropic resets the weekly window WITHOUT warning --
# model launches, rate-limit-counting fixes, incidents -- so resets_at is not
# trustworthy as the only reset signal. Instead we treat any DROP in weekly %
# (or a change of reset window) as a reset boundary and measure burn only over
# the current monotonic run since the last boundary. A surprise reset can never
# produce a phantom "negative burn" or a doom projection: it just restarts the
# clock. Flat or just-reset history yields nil (no burn to project), not a guess.
#
# Fail-soft but not fail-silent: read() never raises, and it distinguishes an
# UNREADABLE/garbled history (returns nil) from a merely absent/empty one
# (returns []). The caller surfaces the former instead of quietly acting as if
# there's simply no burn -- a dead projection must not masquerade as "all clear".
require "json"

module Burn
  module_function

  # Noise guards. Weekly % is a coarse integer, so it wiggles +-1 between renders;
  # without these a 1-point blip over two minutes extrapolates to an absurd rate.
  DROP_RESET = (ENV["USAGE_BURN_DROP_RESET"] || "5").to_f   # a wk fall bigger than this = a real reset, not noise
  MIN_SPAN_H = (ENV["USAGE_BURN_MIN_SPAN_H"] || "2").to_f   # need this many hours of run before a slope is trustworthy
  MIN_DELTA  = (ENV["USAGE_BURN_MIN_DELTA"]  || "3").to_f   # and this much net climb (else it's within rounding noise)

  # Short-horizon (5h SESSION) rate. The weekly two-point slope is too coarse for a
  # window that moves in minutes, so we fit a line by least-squares over a trailing
  # window: every sample pins the fit, so a lone +-1 integer wiggle can't swing it.
  RECENT_SECS       = (ENV["USAGE_SES_WINDOW_SECS"] || "1800").to_f # trailing window to fit (30 min)
  RECENT_MIN_SPAN_H = (ENV["USAGE_SES_MIN_SPAN_H"]  || "0.08").to_f # ~5 min of run before a slope is trustworthy
  RECENT_MIN_DELTA  = (ENV["USAGE_SES_MIN_DELTA"]   || "2").to_f    # and this much climb (past integer noise)

  # => Array of recent, well-formed samples (chronological); [] when there's no
  # history yet; nil when the file exists with content but nothing parses (schema
  # drift / corruption) or can't be read at all. Never raises.
  def read(path, now, keep_secs: 14 * 86_400)
    return [] unless File.exist?(path)

    raw = File.readlines(path)
    parsed = raw.filter_map { |l| parse(l) }
    # Content present but not a single usable line -> drift/corruption, not warmup.
    return nil if parsed.empty? && raw.any? { |l| !l.strip.empty? }

    parsed.select { |e| e["t"] >= now - keep_secs }.sort_by { |e| e["t"] }
  rescue StandardError
    nil  # I/O error (EACCES, EISDIR, bad encoding...) -> unreadable, signal it
  end

  def parse(line)
    e = JSON.parse(line)
    e if e.is_a?(Hash) && e["t"].is_a?(Numeric) && e["wk"].is_a?(Numeric)
  rescue StandardError
    nil  # a bad line is dropped, not fatal; read() flags an all-bad file as nil
  end

  # Collapse the raw multi-session log into ONE account-global monotonic series for a
  # single field over its CURRENT window. The status line records every concurrent
  # session's samples into one file, each frozen at a different staleness, so a naive
  # read interleaves them (wk 42, 35, 42, 32...) and current_run/trailing_run see
  # phantom resets at every downward step. Both windows are account-global and
  # monotonic within a window, so the truth is the running MAX over the current window
  # (the latest reset_field value); samples from older windows are prior weeks/sessions
  # and are dropped. => chronological Array<{t, field[, reset_field]}> (possibly empty),
  # or nil when entries is nil -- an unreadable history must stay distinguishable from
  # an empty one, not be masked as "no burn".
  def envelope(entries, field, reset_field)
    return nil if entries.nil?

    # reset_field must be Numeric to be ordered: parse() only validates t and the
    # field itself, so a schema-drifted line can carry a non-numeric reset. Ordering
    # a String against an Integer would raise -- fatal here, since the caller invokes
    # envelope outside any rescue. Non-numeric resets are simply excluded (the loop's
    # `== latest` then drops them too), preserving the nil==nil legacy-fallback path.
    latest  = entries.filter_map { |e| e[reset_field] if e[field].is_a?(Numeric) && e[reset_field].is_a?(Numeric) }.max
    running = nil
    entries.filter_map do |e|
      next unless e[field].is_a?(Numeric)
      next unless e[reset_field] == latest  # current window only (nil == nil when no reset recorded)

      running = running ? [running, e[field]].max : e[field]
      row = { "t" => e["t"], field => running }
      row[reset_field] = latest if latest
      row
    end
  end

  # The longest trailing run since the most recent reset boundary. A reset is a
  # BIG drop (> DROP_RESET) or a change of reset window -- small -1 dips are
  # rounding noise and stay inside the run (netted out by first->last), so a blip
  # can't fragment the history into a tiny, over-steep run.
  def current_run(entries)
    return entries if entries.size < 2

    start = entries.size - 1
    (entries.size - 1).downto(1) do |i|
      cur = entries[i]
      prev = entries[i - 1]
      break if cur["wk"] < prev["wk"] - DROP_RESET   # weekly fell hard -> a reset landed here
      break if cur["wk_reset"] != prev["wk_reset"]   # window changed -> new week

      start = i - 1
    end
    entries[start..]
  end

  # Average %/hour over the current run and the resulting time-to-cap.
  # `entries` comes from read(). => { burn_per_h:, hours_to_cap:, wk: } or nil
  # when there isn't enough signal (no entries, flat, or just reset).
  def project(entries)
    return nil unless entries && entries.size >= 2

    run = current_run(entries)
    return nil if run.size < 2

    first = run.first
    last = run.last
    dt_h = (last["t"] - first["t"]) / 3600.0
    dpct = last["wk"] - first["wk"]
    # Need a real climb over a real span: too little of either and the slope is
    # noise, not a trend. Better to project nothing than to cry wolf.
    return nil if dt_h < MIN_SPAN_H || dpct < MIN_DELTA

    burn = dpct / dt_h
    # first_t/last_t/dpct let Runway re-measure the same run in WORKING hours (∫ Profile weight)
    # rather than the wall-clock hours this slope is over.
    { burn_per_h: burn, hours_to_cap: (100.0 - last["wk"]) / burn, wk: last["wk"],
      first_t: first["t"], last_t: last["t"], dpct: dpct }
  end

  # The trailing run of a field since its last reset (a drop > DROP_RESET = the
  # window rolled over). Parameterized by field so it works for "ses" as well as
  # "wk"; unlike current_run it has no reset-window column to lean on (history only
  # records wk_reset), so it detects a session reset purely from a drop in value.
  def trailing_run(entries, field)
    return entries if entries.size < 2

    start = entries.size - 1
    (entries.size - 1).downto(1) do |i|
      break if entries[i][field].to_f < entries[i - 1][field].to_f - DROP_RESET
      start = i - 1
    end
    entries[start..]
  end

  # Ordinary least-squares slope (units of field-% per hour) over a run. Robust to
  # the integer wiggle that makes a two-point slope unreliable on a short span.
  def lsq_slope(run, field)
    n  = run.size.to_f
    t0 = run.first["t"]
    xs = run.map { |e| (e["t"] - t0) / 3600.0 } # hours since run start
    ys = run.map { |e| e[field].to_f }
    mx = xs.sum / n
    my = ys.sum / n
    den = xs.sum { |x| (x - mx)**2 }
    return nil if den.zero?

    xs.zip(ys).sum { |x, y| (x - mx) * (y - my) } / den
  end

  # Short-horizon projection for a fast window. Fits the trailing `window_secs` of
  # samples carrying `field` and extrapolates to 100%.
  # => { rate_per_h:, hours_to_cap:, val: } or nil when there's no real, positive
  # climb over a real span (idle, flat, just-reset, or too few points to trust).
  def project_recent(entries, now, field:, window_secs: RECENT_SECS)
    return nil unless entries && !entries.empty?

    recent = entries.select { |e| e["t"] >= now - window_secs && e[field].is_a?(Numeric) }
    return nil if recent.size < 3

    run = trailing_run(recent, field)
    return nil if run.size < 3

    span_h = (run.last["t"] - run.first["t"]) / 3600.0
    delta  = run.last[field].to_f - run.first[field].to_f
    return nil if span_h < RECENT_MIN_SPAN_H || delta < RECENT_MIN_DELTA

    slope = lsq_slope(run, field)
    return nil unless slope&.positive?

    { rate_per_h: slope, hours_to_cap: (100.0 - run.last[field].to_f) / slope, val: run.last[field].to_f }
  end
end
