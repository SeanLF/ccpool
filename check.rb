# frozen_string_literal: true

# `ccpool check` -- time + remaining budget + a keep-going/stop VERDICT, for deciding
# whether to push on in a long or autonomous loop. Reads the per-session snapshots the
# statusline writes (Pool) and the burn history (Burn); there is no other on-demand
# source for rate_limits on the machine, so this is only as fresh as the last render.
#
# The point is the DECISION, not the numbers. The trap (learned the hard way) is treating
# a near-full 5h SESSION window as "out of budget" and stopping -- it resets in hours;
# only the WEEKLY pool genuinely low is a real "stop for the week". The verdict makes
# that distinction explicit. Pace uses ccpool's LINEAR convention (Pool.pace + MARGIN),
# so `check`, `warn`, `status` and `downshift` never disagree.
#
# Unlike `warn`, `check` is on-demand, so it always reports (with a staleness caveat)
# rather than staying silent on old data. => [lines, exit_code]: 0 = report, 2 = no data.
require "json"
require_relative "pool"
require_relative "burn"

module Check
  SESSION_FULL  = (ENV["CCPOOL_CHECK_SES_FULL"]      || "92").to_f   # 5h window effectively spent
  SES_SOON_SECS = (ENV["CCPOOL_CHECK_SES_SOON_SECS"] || "900").to_i  # projected 5h cap this close -> SESSION-LIMITED early
  WEEKLY_LOW    = (ENV["CCPOOL_CHECK_WEEKLY_LOW"]     || "90").to_f   # weekly budget genuinely low
  STALE_SECS    = (ENV["CCPOOL_CHECK_STALE_SECS"]     || "900").to_i  # 15 min -> warn the number is old
  MARGIN        = (ENV["CCPOOL_PACE_MARGIN"]          || "3").to_f    # linear pace overshoot (pts) that earns a PACE DOWN
  COAST_SECS    = (ENV["CCPOOL_COAST_SECS"]           || "43200").to_i # reset this close -> COAST, don't WIND DOWN
  IDLE_WARN_SECS = (ENV["CCPOOL_CHECK_IDLE_WARN_H"]   || "24").to_f * 3600 # idle-before-reset (if 24/7) worth a caveat
  BURNDOWN_FORFEIT = (ENV["CCPOOL_CHECK_BURNDOWN_FORFEIT"] || "15").to_f   # unspent-at-reset worth a BURN DOWN nudge
  HIST = File.expand_path(ENV["CCPOOL_HISTORY"] || "~/.claude/rate-limit-history.jsonl")
  WEEK = 7 * 86_400

  module_function

  def fmt_dur(secs) = CCPool.dur(secs)

  # => [lines_array, exit_code]. Never raises out.
  def report(now = Time.now.to_i)
    files = Dir.glob(Pool::GLOB)
    files = [Pool::CACHE] if files.empty? && File.exist?(Pool::CACHE)
    snaps = Pool.load_snapshots
    return [[absent_or_corrupt(files)], 2] if snaps.empty?

    age = Pool.data_age(snaps, now)
    ses = Pool.window(snaps, "five_hour", now, 6 * 3_600)
    wk  = Pool.window(snaps, "seven_day", now, Pool::WEEK + 86_400)

    history  = Burn.read(HIST, now)
    wk_hist  = Burn.envelope(history, "wk", "wk_reset")
    ses_hist = Burn.envelope(history, "ses", "ses_reset")

    lines = []
    lines << format("time     %s", Time.at(now).strftime("%Y-%m-%d %H:%M %Z (%a)"))
    lines << "data     #{freshness(age)}"
    lines << ""

    ses_soon = session_lines(lines, ses, ses_hist, now)
    pace_warn = weekly_lines(lines, wk, wk_hist, history, now)
    lines << ""
    lines << "VERDICT  #{verdict(ses, wk, ses_soon, pace_warn, now)}"
    [lines, 0]
  rescue StandardError => e
    [["ccpool check: #{e.class}: #{e.message}"], 2]
  end

  def absent_or_corrupt(files)
    files.empty? ? <<~ABSENT.strip : <<~CORRUPT.strip
      No usage snapshots yet. The statusline writes one per session on every render, so this is
      empty only if no interactive Claude Code window has drawn on this machine recently (e.g. a
      pure background job with no TUI). Open/refresh an interactive window, then re-run. Don't guess.
    ABSENT
      Usage snapshots exist (#{files.size}) but none is readable -- corrupt or partially written.
      Treat budget as unknown; don't guess. A fresh statusline render rewrites them; re-run once an
      interactive window has redrawn.
    CORRUPT
  end

  def freshness(age)
    return "unknown age" if age.nil?
    return "fresh (#{age}s ago)" if age <= 90
    return "#{fmt_dur(age)} old" if age <= STALE_SECS

    "STALE -- #{fmt_dur(age)} old; statusline not rendering. The real budget may have moved."
  end

  # 5h SESSION lines. => ses_soon (projected 5h cap imminent even if not yet >= SESSION_FULL).
  def session_lines(lines, ses, ses_hist, now)
    unless ses
      lines << "SESSION  (no live 5h window across sessions -- all snapshots predate the last reset)"
      return false
    end

    lines << format("SESSION  %d%% used  ·  resets in %s  (5h window)", ses[:used].round, fmt_dur(ses[:reset] - now))
    sp = Burn.project_recent(ses_hist, now, field: "ses")
    return false unless sp

    cap_in = sp[:hours_to_cap] * 3600
    rate   = sp[:rate_per_h] >= 60 ? format("%.1f%%/min", sp[:rate_per_h] / 60.0) : format("%.1f%%/h", sp[:rate_per_h])
    if (ses[:reset] - now) <= cap_in
      lines << format("         burn: ~%s -> window resets (in %s) before you'd cap, fine", rate, fmt_dur(ses[:reset] - now))
      false
    else
      lines << format("         burn: ~%s -> 5h cap in ~%s at this rate; land work before the pause", rate, fmt_dur(cap_in))
      cap_in <= SES_SOON_SECS
    end
  end

  # WEEKLY lines. => pace_warn (past the linear share and not near reset).
  def weekly_lines(lines, wk, wk_hist, history, now)
    unless wk
      lines << "WEEKLY   (no live 7d window across sessions -- snapshots missing or stale)"
      return false
    end

    used  = wk[:used]
    reset = wk[:reset]
    lines << format("WEEKLY   %d%% used  ·  resets in %s  (7d window)", used.round, fmt_dur(reset - now))

    p         = Pool.pace(used, reset, now)
    pace_warn = p[:delta] > MARGIN && p[:to_reset] > COAST_SECS
    days_left = [(reset - now).to_f / 86_400, 0.0001].max
    remaining = [100 - used, 0.0].max
    today_cap = [remaining, remaining / days_left].min.clamp(0, 100) # even-burn daily share
    even = Profile::NAME == "even"
    word = even ? "even-burn pace" : "your #{Profile::NAME} pace"
    note =
      if p[:delta].abs < 2 then "on #{word}"
      elsif p[:delta].positive? then format("%dpts AHEAD of %s (burning fast)", p[:delta].round, word)
      else
        # the 24/7 caveat only applies to `even`; a schedule profile already accounts for idle.
        tail = even ? " -- expected unless you run 24/7 (idle/sleep counts as elapsed)" : ""
        format("%dpts UNDER %s%s", (-p[:delta]).round, word, tail)
      end
    label = even ? "of week elapsed" : "of #{word}"
    lines << format("         %d%% %s -> %s", p[:elapsed_pct].round, label, note)
    lines << format("         pace guide: ~%d%%/day spends the rest evenly to reset (not a hard cap)", today_cap.round)

    if (proj = Burn.project(wk_hist))
      secs_to_cap = proj[:hours_to_cap] * 3600
      if secs_to_cap >= (reset - now)
        lines << format("         burn: ~%.1f%%/h -> even non-stop, resets before you'd reach the cap, fine", proj[:burn_per_h])
      else
        idle = (reset - now) - secs_to_cap
        tail = idle > IDLE_WARN_SECS ? "~#{fmt_dur(idle)} idle before reset -- ease off IF you'll sustain this unattended" : "just shy of reset -> burn it down freely"
        lines << format("         burn: ~%.1f%%/h; IF sustained 24/7, cap in ~%s -- %s (idle/sleep stretches this out)", proj[:burn_per_h], fmt_dur(secs_to_cap), tail)
      end
    elsif history.nil?
      lines << "         burn: history unreadable -- projection unavailable (not a clear signal)"
    end
    pace_warn
  end

  def verdict(ses, wk, ses_soon, pace_warn, now)
    ses_used = ses && ses[:used]
    wk_used  = wk && wk[:used]
    wk_left  = wk_used ? [100 - wk_used, 0].max.round : nil

    if ses_used && (ses_used >= SESSION_FULL || ses_soon) && (wk_used.nil? || wk_used < WEEKLY_LOW)
      left  = wk_left ? "#{wk_left}%" : "budget"
      rst   = " (resets in #{fmt_dur(ses[:reset] - now)})"
      state = ses_used >= SESSION_FULL ? "5h window almost full" : "on pace to hit the 5h cap soon at your current burn"
      "SESSION-LIMITED -- #{state}#{rst}. TEMPORARY: land in-flight work, then pause and resume after " \
        "the session resets. Do NOT call the work done while #{left} of the weekly pool remains."
    elsif wk_used && wk_used >= WEEKLY_LOW && (wk[:reset] - now) <= COAST_SECS
      "COAST -- weekly is nearly spent (#{wk_left}% left) but it resets in #{fmt_dur(wk[:reset] - now)}. " \
        "Unspent budget is use-it-or-lose-it, so spend the rest freely."
    elsif wk_used && wk_used >= WEEKLY_LOW
      "WIND DOWN -- weekly pool is nearly spent (#{wk_left}% left). Land what's in flight and stop for the " \
        "week. Finish the task if that's cheaper than a handover; otherwise stop at a natural boundary and " \
        "checkpoint properly -- update docs and leave a handover note so the next session resumes cheaply."
    elsif wk_used && wk_used < WEEKLY_LOW && wk_left && burn_down?(wk, wk_left, now)
      lost = forfeit(wk, wk_left, now).round
      "BURN DOWN -- #{wk_left}% unspent, reset in #{fmt_dur(wk[:reset] - now)}; at your ~1/7/day pace ~#{lost}% " \
        "would go UNSPENT and reset to zero (you can't bank it). If you have valuable but deferrable work -- deep " \
        "passes, parallel fan-outs, research, an overnight loop -- spend it now: go bigger/parallel. Not busywork, " \
        "but don't waste the headroom."
    elsif wk_used.nil? && ses_used.nil?
      "UNKNOWN -- no live budget data across snapshots. Don't guess; re-render the statusline."
    elsif pace_warn
      head = wk_left ? "#{wk_left}% weekly headroom" : "weekly headroom"
      "PACE DOWN -- #{head}, but you're past the linear share of the week. Front-loaded, not doomed -- it " \
        "self-corrects for interactive work. Before a big new thread, spread the spend; finish or checkpoint " \
        "what's in flight first."
    else
      # Only ONE window may be nil here (both-nil is UNKNOWN above). Don't read an ABSENT
      # window as "healthy" -- report it as unknown, matching check's no-false-all-clear rule.
      wkp  = wk_left ? "#{wk_left}% weekly headroom" : "weekly unknown (no live window -- re-render)"
      sesp = ses_used ? "session has room" : "session unknown (no live window)"
      "KEEP GOING -- #{wkp}, #{sesp}. Spend the budget you were asked to spend."
    end
  end

  # Unspent-at-reset headroom: at the even ~1/7-per-day pace, how much of wk_left would still
  # be on the table when the week resets (resets to zero, can't bank).
  def forfeit(wk, wk_left, now)
    days_to_reset = [(wk[:reset] - now).to_f / 86_400, 0].max
    wk_left - days_to_reset * (100.0 / 7)
  end

  # Near-reset surplus: would a meaningful chunk go UNSPENT => nudge to spend it. Self-gating:
  # only fires when there's genuine headroom.
  def burn_down?(wk, wk_left, now) = forfeit(wk, wk_left, now) >= BURNDOWN_FORFEIT
end
