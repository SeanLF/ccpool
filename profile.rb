# frozen_string_literal: true

# Activity-weight pace profile. The weekly pool is a ROLLING 7-day window whose start is
# whenever you first prompted -- you can't align it to "my week starts Monday", so pacing
# used% against the wall-clock FRACTION of that window (the old linear model) assumes you
# burn evenly 24/7. Almost nobody does: a Mon-Fri 9-5 user is "ahead of pace" every Friday
# and "under pace" every Monday, purely from the schedule, and that false signal drives
# downshift/warn/verdict wrong.
#
# Instead we weight time by a wall-clock activity function w(local_time) and measure pace as
# the fraction of the window's WEIGHT that's elapsed, not its seconds:
#     expected_elapsed = ∫w over [window_start, now]  /  ∫w over [window_start, reset]
# w is anchored to local day-of-week + hour, so it stays correct wherever the arbitrary reset
# boundary falls. `even` (w≡1) reproduces the old linear behaviour and stays the default.
#
# Profiles (CCPOOL_PACE_PROFILE):
#   even       w≡1 everywhere -- uniform 24/7 (default; also the honest choice for "random").
#   weekdays   1.0 Mon-Fri, FLOOR on weekends.
#   workhours  1.0 during WORK_DAYS ∩ WORK_HOURS, FLOOR otherwise.
#   custom     w(wday,hour) = DAY_WEIGHTS[wday] * HOUR_WEIGHTS[hour] (literal; FLOOR not applied
#              -- you own the numbers, including deliberate zeros).
#
# FLOOR is the off-schedule residual: without it, burning one evening on a `workhours` profile
# reads as infinitely ahead of pace and would pin the downshift on. It keeps off-hours work sane.
require "time"

module Profile
  NAME  = (ENV["CCPOOL_PACE_PROFILE"] || "even").downcase
  FLOOR = (ENV["CCPOOL_PACE_FLOOR"] || "0.15").to_f

  module_function

  # Parse "1-5" or "1,2,4" (or a mix) into a Set-ish Array of ints; nil/blank OR all-garbage
  # (which would silently floor every day) -> default.
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

  # Parse "9-17" into [start_hour, end_hour]; missing dash / garbled -> default. Same fail-open
  # contract as int_set/weights: a bad env must never crash a render (Profile loads everywhere).
  def hours(str, default)
    h0, h1 = (str || "").split("-").map { Integer(_1.strip, 10) } # base-10 so "09" isn't read as octal
    h0 && h1 ? [h0, h1] : default
  rescue StandardError
    default
  end

  # Parse a comma list of floats into a fixed-size lookup (index -> weight), 1.0 for any
  # unspecified slot; nil/garbled -> all 1.0. `size` guards against a stray long list.
  def weights(str, size)
    table = Array.new(size, 1.0)
    return table if str.nil? || str.strip.empty?

    str.split(",").each_with_index { |v, i| table[i] = Float(v) if i < size }
    table
  rescue StandardError
    Array.new(size, 1.0)
  end

  WORK_DAYS    = int_set(ENV["CCPOOL_WORK_DAYS"], (1..5).to_a).freeze # wday: 0=Sun..6=Sat
  WORK_H0, WORK_H1 = hours(ENV["CCPOOL_WORK_HOURS"], [9, 17])
  DAY_WEIGHTS  = weights(ENV["CCPOOL_PACE_WEIGHTS"], 7).freeze       # Sun..Sat
  HOUR_WEIGHTS = weights(ENV["CCPOOL_PACE_HOUR_WEIGHTS"], 24).freeze

  # Weight for a local (wday, hour). >= 0. `name` defaults to the configured profile; passing it
  # explicitly is for tests (NAME is a load-time constant). Hot path (~168x per pace calc): cheap.
  def weight(wday, hour, name = NAME)
    case name
    when "weekdays"  then WORK_DAYS.include?(wday) ? 1.0 : FLOOR
    when "workhours" then (WORK_DAYS.include?(wday) && hour >= WORK_H0 && hour < WORK_H1) ? 1.0 : FLOOR
    when "custom"    then DAY_WEIGHTS[wday] * HOUR_WEIGHTS[hour]
    else 1.0 # even / unknown -> uniform
    end
  end

  def weight_at(epoch, name = NAME)
    lt = Time.at(epoch).localtime
    weight(lt.wday, lt.hour, name)
  end

  # ∫ weight dt over [a, b], stepping on hour boundaries (weight is constant within an hour).
  # Boundaries are UTC-aligned (t % 3600); for whole-hour-offset zones (EDT et al.) that == local,
  # which is all that matters here. ~168 steps for a full window -- microseconds; no caching.
  def integral(a, b, name = NAME)
    return 0.0 if b <= a

    total = 0.0
    t = a
    while t < b
      step   = [3600 - (t % 3600), b - t].min # align to the next hour boundary (or the end)
      total += weight_at(t, name) * step
      t     += step
    end
    total
  end

  # Fraction of the window's activity WEIGHT elapsed by `now`. Degenerate all-zero weight (or
  # even) -> plain time fraction, so a broken/empty profile can never divide by zero.
  def elapsed_fraction(window_start, now, reset, name = NAME)
    span = reset - window_start
    return 0.0 if span <= 0

    # even (default) is uniform weight -> plain time fraction (skips the 168-step loop); a
    # degenerate all-zero weight can't divide either, so both fall back to this linear fraction.
    linear = ((now - window_start).to_f / span).clamp(0.0, 1.0)
    return linear if name == "even"

    denom = integral(window_start, reset, name)
    return linear if denom <= 0

    (integral(window_start, now, name) / denom).clamp(0.0, 1.0)
  end
end
