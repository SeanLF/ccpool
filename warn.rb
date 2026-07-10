# frozen_string_literal: true

# Situational-awareness hook (`ccpool warn`): warns the agent mid-turn about things it
# otherwise can't see, so long/autonomous work doesn't blow past a limit unaware:
#
#   1. WEEKLY pace over the linear share of the 7-day window (the ceiling nobody advertises).
#   2. 5h SESSION window nearing full (an imminent auto-throttle to land work before).
#   3. CONTEXT window nearing auto-compaction (so it can /compact at a clean point).
#
# Wired to two hook events (see settings.json):
#   UserPromptSubmit -> once per turn / loop iteration; emits plain stdout (added to context).
#   PostToolUse      -> after every tool, so a single long turn still gets warned; emits
#                       hookSpecificOutput.additionalContext, and is THROTTLED per signal so
#                       it doesn't warn after every tool.
#
# Data source is the per-session snapshots the statusline writes (Pool.load_snapshots).
# rate_limits is ACCOUNT-GLOBAL but each snapshot is frozen at a session's last API turn, so
# we reconcile across sessions (Pool.window). context_window is SESSION-LOCAL, so the
# compaction check reads THIS session's own snapshot (matched by session_id).
#
# Pace uses ccpool's LINEAR elapsed-fraction convention (Pool.pace + MARGIN), NOT the old
# front-loaded day/7 allowance -- so `warn`, `status`, `check` and `downshift` never disagree.
#
# Fails open and SILENT throughout: missing/stale/garbled data or any error emits nothing,
# never a false alarm.
require "json"
require_relative "pool"
require_relative "statusline" # fmt_size

module Warn
  STALE        = (ENV["CCPOOL_WARN_STALE_SECS"]     || "3600").to_i # ignore data older than this
  THROTTLE     = (ENV["CCPOOL_WARN_THROTTLE_SECS"]  || "1800").to_i # min gap between weekly/5h warnings
  CTX_WARN     = (ENV["CCPOOL_WARN_CTX_PCT"]        || "85").to_f   # context % that trips a compaction warning
  CTX_THROTTLE = (ENV["CCPOOL_WARN_CTX_THROTTLE_SECS"] || "600").to_i # min gap between compaction warnings (more urgent)
  SES_WARN     = (ENV["CCPOOL_WARN_5H_PCT"]         || "85").to_f   # 5h % that trips an auto-throttle heads-up
  MARGIN       = (ENV["CCPOOL_PACE_MARGIN"]         || "3").to_f    # pace overshoot (pts) before we nag
  COAST        = (ENV["CCPOOL_COAST_SECS"]          || "43200").to_i # reset this close -> use-it-or-lose-it, don't nag
  TMP          = ENV["TMPDIR"] || "/tmp"

  Sig = Struct.new(:key, :throttle, :text)

  module_function

  # Pure decision: the warnings that apply right now. => Array<Sig> (possibly empty).
  # Kept side-effect-free (no markers, no stdin) so it's trivially testable.
  def signals(snaps, session_id, now)
    fresh = (age = Pool.data_age(snaps, now)) && age <= STALE
    out = []

    if fresh
      if (wk = Pool.window(snaps, "seven_day", now, Pool::WEEK + 86_400))
        p = Pool.pace(wk[:used], wk[:reset], now)
        out << Sig.new("pace", THROTTLE, pace_text(wk[:used], p)) if p[:delta] > MARGIN && p[:to_reset] > COAST
      end
      if (fh = Pool.window(snaps, "five_hour", now, 6 * 3_600)) && fh[:used] >= SES_WARN
        out << Sig.new("session", THROTTLE, session_text(fh, now))
      end
    end

    # CONTEXT is session-local: read THIS session's own snapshot, with its own freshness gate.
    own = session_id && snaps.find { |d| d["session_id"] == session_id }
    cap = own && own["captured_at"]
    cw  = own && own["context_window"]
    if cap.is_a?(Numeric) && now - cap <= STALE && cw.is_a?(Hash) &&
       (ctx = cw["used_percentage"]).is_a?(Numeric) && ctx.between?(0, 100) && ctx >= CTX_WARN
      out << Sig.new("ctx", CTX_THROTTLE, ctx_text(ctx, cw))
    end
    out
  end

  def pace_text(used, p)
    against = Profile::NAME == "even" ? "of the week elapsed" : "of your #{Profile::NAME} pace"
    format(
      "[usage-pace] WEEKLY pace: %d%% used vs ~%d%% #{against} (~%dpts ahead; resets in %s). " \
      "A PACE signal, NOT a stop order. If finishing the current task is cheaper than a clean handover, " \
      "push through -- a cold restart pays to rebuild context. If you stop, checkpoint properly: update " \
      "the relevant docs and leave a handover note so the next session resumes cheaply, don't just drop " \
      "it mid-task. Running unattended, aim for a comfortable checkpoint as you near the limit, not the " \
      "moment you cross pace. Run `ccpool check` before a big new push.",
      used.round, p[:elapsed_pct].round, p[:delta].round, CCPool.dur(p[:to_reset])
    )
  end

  def session_text(fh, now)
    format(
      "[usage-session] 5h SESSION window at %d%% (resets in %s) -- you will auto-throttle soon. Land or " \
      "checkpoint in-flight work before the pause. This is a short wait, not done for the week, if the " \
      "weekly pool still has room.",
      fh[:used].round, CCPool.dur(fh[:reset] - now)
    )
  end

  def ctx_text(ctx, cw)
    size  = Statusline.fmt_size(cw["context_window_size"])
    where = size ? " of the #{size} context window" : ""
    format(
      "[context] this session is at %d%%%s -- auto-compaction is near. Land or checkpoint important state " \
      "now, and consider /compact at a clean point so it doesn't cut mid-task.",
      ctx.round, where
    )
  end

  # Apply per-signal throttling (only on PostToolUse -- UserPromptSubmit is once/turn and always
  # emits) and format for the firing event. => String to print, or nil when nothing fires.
  def emit(sigs, event, session_id, now)
    return nil if sigs.empty?

    key = (session_id || "global").gsub(/[^\w.-]/, "")
    fire = sigs.reject do |w|
      event == "PostToolUse" && now - (File.read(marker(w, key)).to_i rescue 0) < w.throttle
    end
    fire.each { |w| File.write(marker(w, key), now.to_s) rescue nil }
    return nil if fire.empty?

    text = fire.map(&:text).join("\n")
    if event == "PostToolUse"
      JSON.generate(hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: text })
    else
      text
    end
  end

  def marker(sig, key) = File.join(TMP, "claude-#{sig.key}-#{key}")

  # Entry point: hook payload (parsed) + now -> the string to print, or nil. Never raises.
  def run(payload, now = Time.now.to_i)
    snaps = Pool.load_snapshots
    return nil if snaps.empty?

    event = payload["hook_event_name"].is_a?(String) ? payload["hook_event_name"] : "UserPromptSubmit"
    sid   = payload["session_id"].is_a?(String) ? payload["session_id"] : nil
    emit(signals(snaps, sid, now), event, sid, now)
  rescue StandardError
    nil
  end
end
