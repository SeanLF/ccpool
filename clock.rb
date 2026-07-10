# frozen_string_literal: true

# One knob so every WALL-CLOCK time in ccpool agrees. Durations ("in 5d 10h") are not clock
# times and are unaffected -- this governs only times-of-day (rhythm's busiest hour, status's
# reset time, check's clock line).
#   CCPOOL_CLOCK = 24 (default) | 12 | auto
# `auto` best-efforts the OS preference (macOS `AppleICUForce24HourTime`) and falls back to 24.
# It's opt-in, never the default: detection is macOS-only and the key is often unset, so a wrong
# guess would be worse than a predictable default.
require "time"

module Clock
  module_function

  # Raw env string -> 12 | 24. Unknown/blank -> 24. `auto` -> best-effort OS detect.
  def resolve(v)
    case (v || "24").strip.downcase
    when "12" then 12
    when "auto" then detect
    else 24 # "24", blank, or garbage: predictable default
    end
  end

  # macOS: AppleICUForce24HourTime "1" => 24h, "0" => 12h. Absent / other / non-mac => 24.
  def detect
    `defaults read -g AppleICUForce24HourTime 2>/dev/null`.strip == "0" ? 12 : 24
  rescue StandardError
    24
  end

  MODE = resolve(ENV.fetch("CCPOOL_CLOCK", nil))

  def h12? = MODE == 12

  # Hour-of-day int (0..23) -> "18:00" (24h) / "6pm" (12h).
  def hour(h, h12: h12?)
    return format("%02d:00", h) unless h12
    return "12am" if h.zero?
    return "12pm" if h == 12

    h < 12 ? "#{h}am" : "#{h - 12}pm"
  end

  # A Time -> clock time only, no date: "22:30" (24h) / "10:30pm" (12h).
  def time(t, h12: h12?) = t.strftime(h12 ? "%-I:%M%P" : "%H:%M")
end
