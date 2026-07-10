# frozen_string_literal: true

# Core pool read: reconcile the account-global rate_limits % across the per-session
# statusline snapshots, and compute pace vs the fraction of the 7-day window elapsed.
# This is the one thing ccusage can't see (it never gets rate_limits) -- our floor.
require "json"

module Pool
  CACHE = File.expand_path(ENV["USAGE_CACHE"] || "~/.claude/usage-cache.json")
  GLOB  = CACHE.sub(/\.json\z/, "-*.json")
  WEEK  = 7 * 86_400
  # How old a snapshot may be before we distrust the raw % and fall to the estimate tier.
  # During active use the statusline re-renders the % multiple times/minute (event-driven,
  # ~300ms debounce), so a trustworthy snapshot is SECONDS old. 2min covers active use
  # comfortably; past it we extrapolate (via accrued ccusage cost) to catch a headless
  # fan-out burning unseen (it renders no statusline). Tight is safer here, not looser.
  STALE = (ENV["CCPOOL_STALE_SECS"] || "120").to_i

  module_function

  def load_snapshots
    files = Dir.glob(GLOB)
    files = [CACHE] if files.empty? && File.exist?(CACHE)
    files.filter_map { |f| (JSON.parse(File.read(f)) rescue nil) }.select { |d| d.is_a?(Hash) }
  end

  # Reconcile one account-global window across snapshots frozen at differing staleness:
  # newest still-plausible reset, MAX used% on it (monotonic within a window -> freshest).
  # Guards the leak bug (#52326: used% carrying a resets_at epoch) and clamps garbage.
  def window(snaps, key, now, max_ahead)
    live = snaps.filter_map do |d|
      w = d.dig("rate_limits", key)
      next unless w.is_a?(Hash) && (u = w["used_percentage"]).is_a?(Numeric) && u >= 0 && u < 10_000
      r = w["resets_at"]
      next unless r.is_a?(Numeric) && r > now && r <= now + max_ahead

      { used: [u.to_f, 100.0].min, reset: r }
    end
    return if live.empty?

    reset = live.map { _1[:reset] }.max
    { used: live.select { _1[:reset] == reset }.map { _1[:used] }.max, reset: reset }
  end

  # Seconds since the freshest snapshot across all sessions (nil if none).
  def data_age(snaps = load_snapshots, now = Time.now.to_i)
    cap = snaps.filter_map { |d| d["captured_at"] if d["captured_at"].is_a?(Numeric) }.max
    cap && (now - cap)
  end

  def fresh?(snaps, now)
    (age = data_age(snaps, now)) && age <= STALE
  end

  # Raw reconciled window + its age. nil only if NO plausible window exists at all;
  # staleness is the CALLER's decision (it drives the fresh/estimated/stale tiers).
  def weekly(now = Time.now.to_i)
    s = load_snapshots
    w = window(s, "seven_day", now, WEEK + 86_400)
    w&.merge(age: data_age(s, now))
  end

  def five_hour(now = Time.now.to_i)
    s = load_snapshots
    w = window(s, "five_hour", now, 6 * 3_600)
    w&.merge(age: data_age(s, now))
  end

  # LINEAR pace: allowance = the fraction of the 7-day window elapsed (not ceil'd days --
  # that front-loads and reads too rosy). delta>0 = ahead of pace (burning fast).
  def pace(used, reset, now = Time.now.to_i)
    elapsed = (now - (reset - WEEK)).clamp(0, WEEK)
    frac = elapsed.to_f / WEEK
    { elapsed_pct: frac * 100, delta: used - frac * 100, to_reset: reset - now }
  end
end
