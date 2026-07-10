# frozen_string_literal: true

# Activity-weight pace profile. The weekly pool is a ROLLING 7-day window whose start is
# whenever you first prompted -- you can't align it to "my week starts Monday", so pacing used%
# against the wall-clock FRACTION of that window (plain linear) assumes you burn evenly 24/7.
# Almost nobody does: a Mon-Fri 9-5 user reads "ahead of pace" every Friday and "under" every
# Monday purely from the schedule, and that false signal drives downshift/warn/verdict wrong.
#
# Instead we weight time by a wall-clock activity function and measure pace as the fraction of
# the window's WEIGHT elapsed, not its seconds:
#     expected_elapsed = ∫w over [window_start, now]  /  ∫w over [window_start, reset]
# w is anchored to local day-of-week + hour, so it's correct wherever the arbitrary reset lands.
#
# Two orthogonal knobs, both 24/7 by DEFAULT (so the default reproduces plain linear pace and
# fits a continuous autonomous-loop operator -- the honest "no info" prior):
#   CCPOOL_WORK_DAYS   which days you're active   (wday 0=Sun..6=Sat; default all 7)
#   CCPOOL_WAKE_HOURS  your waking window on them  (e.g. 9-17; default 0-24 = no sleep)
# Off a work day OR outside waking hours you get the FLOOR residual (not zero -- one late night
# must not read as infinitely ahead of pace).
#
# CCPOOL_PACE_PROFILE is optional sugar that just picks defaults for those two knobs:
#   even (default)  all days, 24h   -> uniform (identical to the old linear behaviour)
#   weekdays        Mon-Fri, 24h
#   workhours       Mon-Fri, 9-17
#   custom          graded DAY_WEIGHTS[wday] * HOUR_WEIGHTS[hour] (power users; FLOOR not applied)
# An explicit knob always overrides the preset's default.
require "time"

module Profile
  NAME  = (ENV["CCPOOL_PACE_PROFILE"] || "even").downcase
  FLOOR = (ENV["CCPOOL_PACE_FLOOR"] || "0.15").to_f

  module_function

  # Parse "1-5" or "1,2,4" (or a mix) into an Array of ints; nil/blank OR all-garbage (which
  # would silently floor every day) -> default.
  def int_set(str, default)
    return default if str.nil? || str.strip.empty?

    parsed = str.split(",").flat_map do |part|
      if part =~ /\A\s*(\d+)\s*-\s*(\d+)\s*\z/ then ($1.to_i..$2.to_i).to_a
      else Integer(part) rescue nil
      end
    end.compact
    parsed.empty? ? default : parsed
  rescue StandardError
    default
  end

  # Parse "9-17" into [start_hour, end_hour]; missing dash / garbled -> default. Fail-open like
  # int_set: a bad env must never crash a render (Profile loads everywhere).
  def hours(str, default)
    h0, h1 = (str || "").split("-").map { Integer(_1.strip, 10) } # base-10 so "09" isn't read as octal
    h0 && h1 ? [h0, h1] : default
  rescue StandardError
    default
  end

  # Comma list of floats -> fixed-size lookup (index -> weight), 1.0 for unspecified; garbled -> all 1.0.
  def weights(str, size)
    table = Array.new(size, 1.0)
    return table if str.nil? || str.strip.empty?

    str.split(",").each_with_index { |v, i| table[i] = Float(v) if i < size }
    table
  rescue StandardError
    Array.new(size, 1.0)
  end

  # A preset only sets DEFAULTS for the two knobs; an explicit env var below overrides them.
  day_default  = %w[weekdays workhours].include?(NAME) ? (1..5).to_a : (0..6).to_a
  hour_default = NAME == "workhours" ? [9, 17] : [0, 24]

  WORK_DAYS = int_set(ENV["CCPOOL_WORK_DAYS"], day_default).freeze
  WAKE_H0, WAKE_H1 = hours(ENV["CCPOOL_WAKE_HOURS"], hour_default)
  # Graded weight vectors -- custom profile only (nil otherwise).
  CUSTOM = NAME == "custom" ? [weights(ENV["CCPOOL_PACE_WEIGHTS"], 7), weights(ENV["CCPOOL_PACE_HOUR_WEIGHTS"], 24)].freeze : nil

  # A resolved profile: active days, waking window [h0, h1), and optional [day_wts, hour_wts].
  Config  = Struct.new(:days, :h0, :h1, :custom)
  DEFAULT = Config.new(WORK_DAYS, WAKE_H0, WAKE_H1, CUSTOM)

  # True when the weight is uniformly 1.0 everywhere -> pace is just the plain time fraction and
  # we skip the integral. Detected from the CONFIG, so it fires however you reached 24/7 (named
  # `even`, or the knobs simply left at their defaults), not only when NAME == "even".
  def uniform?(cfg = DEFAULT)
    cfg.custom.nil? && cfg.h0 <= 0 && cfg.h1 >= 24 && (0..6).all? { |d| cfg.days.include?(d) }
  end
  def scheduled? = !uniform?

  # Weight for a local (wday, hour). >= 0. Off a work day OR outside waking hours -> FLOOR.
  def weight(wday, hour, cfg = DEFAULT)
    return cfg.custom[0][wday] * cfg.custom[1][hour] if cfg.custom

    (cfg.days.include?(wday) && hour >= cfg.h0 && hour < cfg.h1) ? 1.0 : FLOOR
  end

  def weight_at(epoch, cfg = DEFAULT)
    lt = Time.at(epoch).localtime
    weight(lt.wday, lt.hour, cfg)
  end

  # ∫ weight dt over [a, b], stepping on hour boundaries (weight is constant within an hour).
  # Boundaries are UTC-aligned (t % 3600); for whole-hour-offset zones (EDT et al.) that == local,
  # which is all that matters here. ~168 steps for a full window -- microseconds; no caching.
  def integral(a, b, cfg = DEFAULT)
    return 0.0 if b <= a

    total = 0.0
    t = a
    while t < b
      step   = [3600 - (t % 3600), b - t].min # align to the next hour boundary (or the end)
      total += weight_at(t, cfg) * step
      t     += step
    end
    total
  end

  # Fraction of the window's activity WEIGHT elapsed by `now`. Uniform (24/7) or a degenerate
  # all-zero weight -> plain time fraction, so a broken profile can never divide by zero.
  def elapsed_fraction(window_start, now, reset, cfg = DEFAULT)
    span = reset - window_start
    return 0.0 if span <= 0

    linear = ((now - window_start).to_f / span).clamp(0.0, 1.0)
    return linear if uniform?(cfg)

    denom = integral(window_start, reset, cfg)
    return linear if denom <= 0

    (integral(window_start, now, cfg) / denom).clamp(0.0, 1.0)
  end
end
