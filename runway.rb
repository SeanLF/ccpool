# frozen_string_literal: true

# "How many WORKING hours of pool do I have left before the weekly reset?" -- the actionable
# reframe of "% left". Same shape as SRE error-budget burn-rate alerting / cloud budget
# forecasting: remaining budget / burn rate = time-to-exhaustion. Two refinements the raw rate
# misses:
#   * WORKING hours, not wall-clock: burn is re-measured per ACTIVE hour (∫ Profile weight over
#     the run), so sleep/idle doesn't dilute the rate and the answer is in hours you'd work.
#   * budget vs calendar: min(hours the budget affords, working-hours the week has left). Which
#     one BINDS is the verdict -- budget-limited => you'll throttle early; calendar-limited =>
#     the week resets before you run dry, so burn freely.
# This is a WEEKLY quantity on purpose: the weekly pool is the only budget that doesn't recover
# while you sleep (the 5h window resets in hours). Burn is bursty/fat-tailed, so we report a
# RANGE, never a false-precise point. nil = no usable burn signal.
require_relative "profile"

module Runway
  # Asymmetric fat-tail band on the burn rate: burning FASTER (shorter runway) is the risk worth
  # weighting, so the fast side is wider. Override to tune.
  FAST = (ENV["CCPOOL_RUNWAY_FAST"] || "1.5").to_f # rate could be this much higher
  SLOW = (ENV["CCPOOL_RUNWAY_SLOW"] || "0.7").to_f # ...or this much lower
  # A single burn run may not be schedule-representative (e.g. a workhours user's overnight loop
  # burst lands entirely in FLOOR hours). Measuring its rate per active-hour would then divide by
  # ~FLOOR*wall and inflate the rate up to 1/FLOOR (~6.7x) -> a false "throttle imminent". Floor
  # the run's active-hours at this fraction of its wall span so one off-schedule burst can't imply
  # an unbounded per-active-hour rate. No-op for `even` (active == wall always).
  MIN_DENSITY = (ENV["CCPOOL_RUNWAY_MIN_DENSITY"] || "0.5").to_f

  module_function

  # Active-hours over [a, b], but never fewer than MIN_DENSITY of the wall span -- see MIN_DENSITY.
  def active_hours(a, b, cfg = Profile::DEFAULT)
    [Profile.integral(a, b, cfg) / 3600.0, (b - a) / 3600.0 * MIN_DENSITY].max
  end

  # proj = Burn.project(...) (needs :dpct/:first_t/:last_t). => Hash{hours, low, high, budget_h,
  # cal_h, bind} or nil when there's no usable run.
  def estimate(used, reset, proj, now)
    return nil unless proj && proj[:dpct].to_f.positive? && proj[:first_t] && proj[:last_t]

    active_h = active_hours(proj[:first_t], proj[:last_t])
    cal_h    = Profile.integral(now, reset) / 3600.0
    return nil if active_h <= 0 || cal_h <= 0

    rate      = proj[:dpct] / active_h # % of pool per WORKING hour
    remaining = [100.0 - used, 0.0].max
    budget_h  = remaining / rate
    { hours: [budget_h, cal_h].min, budget_h: budget_h, cal_h: cal_h,
      low: [remaining / (rate * FAST), cal_h].min, high: [remaining / (rate * SLOW), cal_h].min,
      bind: budget_h < cal_h ? :budget : :calendar }
  end

  def dur(secs)
    h, = secs.to_i.clamp(0, 1 << 62).divmod(3600)
    d, h = h.divmod(24)
    d.positive? ? "#{d}d #{h}h" : "#{h}h"
  end

  # One-line human phrasing. Budget-limited surfaces the working-hours range (the scary number);
  # calendar-limited just says the week wins, so burn freely.
  def phrase(r, to_reset)
    if r[:bind] == :budget
      lo = r[:low].round
      hi = r[:high].round
      span = lo == hi ? "~#{lo}" : "~#{lo}-#{hi}"
      "#{span} working-hours of pool left -> at your active-hour burn you'd throttle before reset (#{dur(to_reset)} out)"
    else
      "budget outlasts the week -> reset (#{dur(to_reset)}) comes first with headroom, burn freely"
    end
  end
end
