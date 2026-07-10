#!/usr/bin/env ruby
# frozen_string_literal: true

# ccpool -- get the most out of your fixed Claude subscription pool.
#
#   ccpool statusline        (wire as your Claude Code statusLine command) -- captures
#                            rate_limits from CC's payload, seeds history, renders a line.
#                            This populates ccpool's data on a fresh install.
#   ccpool status            % used, ~$ API-equiv left, pace vs the week elapsed.
#   ccpool check             time + budget + a keep-going/stop VERDICT (for long/auto loops).
#   ccpool warn              hook: warn the agent mid-turn about pace / 5h / context (stdin=payload).
#   ccpool run -- <cmd...>   run <cmd>, downshifting subagent model/effort when ahead of pace.
#   ccpool review [days]     retrospective: did you use the right model for the work?
#   ccpool rhythm            read-only: your circadian work rhythm + a suggested pace profile.
#
# Delegates cost to ccusage; reads the % ccusage can't see. Fails OPEN on missing data.
require "json"
require "time"
require_relative "pool"
require_relative "calibration"
require_relative "analyzer"
require_relative "burn"
require_relative "statusline"
require_relative "warn"
require_relative "check"
require_relative "runway"
require_relative "rhythm"
require_relative "clock"

module CCPool
  MARGIN  = (ENV["CCPOOL_PACE_MARGIN"] || "3").to_f
  MODE    = (ENV["CCPOOL_DOWNSHIFT"] || "auto").downcase # auto (enforce) | advise (print, don't apply) | off
  DMODEL  = ENV["CCPOOL_DOWNSHIFT_MODEL"] || "haiku"
  DEFFORT = ENV["CCPOOL_DOWNSHIFT_EFFORT"] || "low"
  COAST   = (ENV["CCPOOL_COAST_SECS"] || "43200").to_i # <12h to reset -> use-it-or-lose-it
  FIVE_H_CAP = (ENV["CCPOOL_5H_CAP"] || "85").to_f      # 5h window this full -> downshift too
  HIST    = File.expand_path(ENV["CCPOOL_HISTORY"] || "~/.claude/rate-limit-history.jsonl")
  HIST_KEEP_DAYS = (ENV["CCPOOL_HISTORY_KEEP_DAYS"] || "30").to_f    # `prune --history` cutoff; 0 = keep raw forever
  HIST_MIN_INT   = (ENV["CCPOOL_HISTORY_MIN_INTERVAL"] || "60").to_i # min secs between 5h-only writes (curbs growth)

  module_function

  def usd(n) = "$#{n.round.to_s.reverse.gsub(/(\d{3})(?=\d)/, '\\1,').reverse}"
  def usdk(n) = n >= 1000 ? "$#{(n / 1000.0).round(1)}k" : "$#{n.round}"

  def dur(secs)
    secs = secs.to_i
    return "now" if secs <= 0

    d, r = secs.divmod(86_400)
    d.positive? ? "#{d}d #{r / 3_600}h" : "#{r / 3_600}h #{(r % 3_600) / 60}m"
  end

  def at(epoch)
    t = Time.at(epoch).localtime
    "#{t.strftime('%a %m-%d')} #{Clock.time(t)}"
  end

  # 3-tier read: fresh official snapshot > stale-but-extrapolated-from-accrued-cost >
  # stale-shown-with-warning. (No OAuth tier -- deliberately.) => window + :confidence.
  def resolve_weekly(now = Time.now.to_i)
    wk = Pool.weekly(now) or return nil
    age = wk[:age] || 0
    return wk.merge(confidence: :fresh) if age <= Pool::STALE

    dpp = Calibration.dollar_per_pct(now)
    accrued = dpp && Calibration.cost_since(now - age, now)
    if dpp && accrued && accrued >= 0
      wk.merge(used: (wk[:used] + accrued / dpp).clamp(0, 100), confidence: :estimated)
    else
      wk.merge(confidence: :stale)
    end
  end

  def stamp(wk, now)
    case wk[:confidence]
    when :estimated then "  ·  ~estimated (snapshot #{dur(wk[:age])} old + accrued cost)"
    when :stale then "  ·  ⚠ stale: snapshot #{dur(wk[:age])} old, may be behind (open Claude Code)"
    else wk[:age] && wk[:age] > 300 ? "  ·  data #{wk[:age] / 60}m old" : ""
    end
  end

  def pace_phrase(p)
    return "reset in #{dur(p[:to_reset])} -- unspent budget is use-it-or-lose-it, burn freely" if p[:to_reset] < COAST

    d = p[:delta]
    el = p[:elapsed_pct].round
    frame = Profile.scheduled? ? "of your work-rhythm pace" : "of the week elapsed" # agree with check/warn
    if d > MARGIN then "#{d.round} pts AHEAD of pace (#{el}% #{frame}) -- burning fast"
    elsif d < -MARGIN then "#{(-d).round} pts under pace (#{el}% #{frame}) -- banked headroom"
    else "on pace (~#{el}% #{frame})"
    end
  end

  # -- full readout --------------------------------------------------------------------
  def status(now = Time.now.to_i)
    wk = resolve_weekly(now)
    unless wk
      puts "weekly pool: no data yet. Wire `ccpool statusline` as your Claude Code"
      puts "statusLine command (settings.json) so it can capture rate_limits, then use CC once."
      return
    end

    used = wk[:used]
    dpp = Calibration.dollar_per_pct(now)
    dollars = dpp ? "  ·  ~#{usd((100 - used) * dpp)} left of ~#{usd(100 * dpp)} (API-equiv)" : "  ·  ($ value calibrating -- needs ccusage + a few days of history)"
    puts "Weekly pool  ·  #{used.round}% used#{dollars}  ·  resets #{at(wk[:reset])} (#{dur(wk[:reset] - now)})#{stamp(wk, now)}"
    puts "Pace         ·  #{pace_phrase(Pool.pace(used, wk[:reset], now))}"

    # Burn projection (reset-robust, from the history log) -- will you throttle before reset?
    # envelope() first: the raw log interleaves concurrent sessions, so project() needs the
    # collapsed monotonic current-window series or it sees phantom resets.
    entries = Burn.read(HIST, now)
    env = entries && Burn.envelope(entries, "wk", "wk_reset")
    pr = env && Burn.project(env)
    if pr
      # cap from the FRESH `used` (same basis Runway uses), not the last log sample -- else the
      # Burn verdict and the Runway line can disagree when the cache is stale (:estimated tier).
      cap_h = (100.0 - used) / pr[:burn_per_h]
      dcap = cap_h / 24.0
      dreset = (wk[:reset] - now) / 86_400.0
      verdict = cap_h * 3_600 < (wk[:reset] - now) ? "⚠ ~#{(dreset - dcap).round(1)}d BEFORE reset -- you'll throttle early" : "resets first (in #{dreset.round(1)}d) -- you're clear"
      puts "Burn         ·  ~#{pr[:burn_per_h].round(1)}%/h -> hits cap in ~#{dcap.round(1)}d; #{verdict}"
    end
    if pr && (r = Runway.estimate(used, wk[:reset], pr, now))
      puts "Runway       ·  #{Runway.phrase(r, wk[:reset] - now)}"
    end

    fh = Pool.five_hour(now)
    if fh && (fh[:age] || 0) <= Pool::STALE && fh[:used] >= 70
      puts "5h window    ·  #{fh[:used].round}% used (resets #{dur(fh[:reset] - now)}) -- session throttle near"
    end
    if (n = stale_caches(now).size) >= 20 # surface, don't auto-delete
      puts "cleanup      ·  #{n} stale session snapshots accumulating -- run `ccpool prune` to clean"
    end
    warn_mb = (ENV["CCPOOL_HISTORY_WARN_MB"] || "20").to_f
    if HIST_KEEP_DAYS.positive? && (mb = (File.size(HIST) rescue 0) / 1_048_576.0) > warn_mb
      puts "cleanup      ·  usage history is #{mb.round}MB -- `ccpool prune --history` compacts it to the last #{HIST_KEEP_DAYS.round}d"
    end
  end

  # -- statusLine command: capture + seed + render (cached $ only; must stay fast) ------
  # Direct in a terminal there's no CC payload on stdin (reading it would just hang), so PREVIEW
  # from the newest snapshot instead. Piped/redirected stdin (incl. Claude Code) -> the real path.
  def statusline(now = Time.now.to_i)
    return preview_statusline(now) if $stdin.tty?

    payload = (JSON.parse($stdin.read) rescue {})
    return unless payload.is_a?(Hash)

    if (sid = payload["session_id"]).is_a?(String)
      path = Pool::CACHE.sub(/\.json\z/, "-#{sid.gsub(/[^\w.-]/, '')}.json")
      tmp = "#{path}.#{Process.pid}.tmp"
      (File.write(tmp, JSON.generate(payload.merge("captured_at" => now))); File.rename(tmp, path)) rescue nil # atomic
    end
    seed_history(payload, now)
    prune_caches(now) if ENV["CCPOOL_PRUNE"] == "1" # opt-in only: deleting files is never silent-by-default

    line = Statusline.render(payload, now) # rich: ctx · cache · 5h · weekly meter (coloured) + $
    print line unless line.to_s.empty?
  rescue StandardError => e
    Statusline.log("error", "#{e.class}: #{e.message} @ #{e.backtrace&.first}")
    # a statusline must NEVER break Claude Code
  end

  # `ccpool statusline` in a terminal: render from the freshest per-session snapshot the real
  # statusLine has captured, so you can see what the line looks like without Claude Code. The
  # rate_limits % is account-global, so an old snapshot's % is still current; only the ctx/cache
  # segments (session-local) may be stale -- hence the "preview" caveat on stderr.
  def preview_statusline(now = Time.now.to_i)
    newest = Dir.glob(Pool::GLOB).max_by { |f| File.mtime(f) rescue 0 }
    unless newest
      warn "ccpool: no statusline snapshot yet. Wire `ccpool statusline` as your Claude Code statusLine first (see README), then it self-populates."
      return
    end
    data = JSON.parse(File.read(newest))
    age  = now - (data["captured_at"] || File.mtime(newest).to_i)
    line = Statusline.render(data, now)
    warn "[preview from a #{dur(age)}-old snapshot -- ctx/cache may be stale; live values come from Claude Code]"
    puts line unless line.to_s.empty?
  rescue StandardError
    warn "ccpool: couldn't render a statusline preview (no readable snapshot)."
  end

  # Stale per-session snapshots older than KEEP (dead sessions). Non-destructive by default:
  # returns the paths so status can SURFACE them; `ccpool prune` (or CCPOOL_PRUNE=1) deletes.
  def stale_caches(now)
    keep = (ENV["CCPOOL_CACHE_KEEP_SECS"] || "3600").to_i
    (Dir.glob(Pool::GLOB) + Dir.glob("#{Pool::GLOB}.*.tmp")).select { |f| now - File.mtime(f).to_i > keep rescue false }
  rescue StandardError
    []
  end

  def prune_caches(now)
    stale_caches(now).count { |f| File.delete(f) rescue next }
  end

  # Compact the raw history to the last `keep` days (Burn ignores >14d; the $/1% summary lives in
  # the calibration cache, so old raw is redundant). Opt-in only. keep<=0 -> keep raw forever.
  # flock so a concurrent statusline append can't interleave with the rewrite. => rows removed.
  def prune_history(now, keep = HIST_KEEP_DAYS)
    return 0 if keep <= 0 || !File.exist?(HIST)

    cutoff = now - keep * 86_400
    removed = 0
    File.open(HIST, File::RDWR) do |f|
      f.flock(File::LOCK_EX)
      lines = f.read.lines
      kept = lines.select { |l| (JSON.parse(l)["t"] rescue 0) >= cutoff }
      removed = lines.size - kept.size
      if removed.positive?
        # write-THEN-truncate (not truncate-first): a crash mid-op then leaves the kept rows plus a
        # stale tail (readers skip torn lines), never an empty file. fsync so the kept rows are
        # durable before we drop the tail. tmp+rename is wrong here -- it'd orphan a blocked
        # appender's inode; both writers flock the same path, so in-place is the safe choice.
        f.rewind
        f.write(kept.join)
        f.fsync
        f.truncate(f.pos)
      end
    end
    removed
  rescue StandardError
    0
  end

  def seed_history(payload, now)
    sd = payload.dig("rate_limits", "seven_day")
    fh = payload.dig("rate_limits", "five_hour")
    return unless sd.is_a?(Hash) && sd["used_percentage"].is_a?(Numeric)

    sid = payload["session_id"]
    row = { "t" => now, "wk" => sd["used_percentage"], "wk_reset" => sd["resets_at"],
            "ses" => fh&.dig("used_percentage"), "ses_reset" => fh&.dig("resets_at"),
            "tier" => (ENV["USAGE_TIER"] || "max_20x"), "cost" => payload.dig("cost", "total_cost_usd"),
            "session" => sid }
    # flock the read-check-append so concurrent sessions' statuslines can't interleave lines.
    # Dedup PER SESSION (not the global last line: other sessions interleave, and wk is
    # account-global so they all carry the same wk), and key on ses too -- else a 5h-only move
    # (wk flat) is dropped and the session burn series starves.
    File.open(HIST, File::RDWR | File::CREAT, 0o644) do |f|
      f.flock(File::LOCK_EX)
      # Only need THIS session's most recent line, which is near the tail (sessions render
      # often), so scan the last 64KB, not the whole (unbounded) file -- this runs on every
      # render under the lock. A missed older line just appends a harmless duplicate row.
      f.seek([f.size - 65_536, 0].max)
      last = nil
      f.read.lines.reverse_each do |l|
        e = (JSON.parse(l) rescue nil)
        next unless e.is_a?(Hash) && (sid.nil? || e["session"] == sid)

        last = e
        break
      end
      if last && last["wk"] == row["wk"] && last["wk_reset"] == row["wk_reset"]
        next if last["ses"] == row["ses"]                  # nothing moved
        next if now - last["t"].to_i < HIST_MIN_INT        # only the 5h % moved -> throttle (curbs growth)
      end

      f.seek(0, IO::SEEK_END)
      f.puts(JSON.generate(row))
    end
  rescue StandardError => e
    Statusline.log("warn", "history append failed: #{e.class}: #{e.message}")
  end

  # -- situational-awareness hook (stdin = hook payload) --------------------------------
  def warn_hook(now = Time.now.to_i)
    payload = (JSON.parse($stdin.read) rescue {})
    payload = {} unless payload.is_a?(Hash)
    out = Warn.run(payload, now)
    puts out unless out.to_s.empty?
  rescue StandardError
    # a hook must NEVER break Claude Code
  end

  # -- keep-going/stop verdict ---------------------------------------------------------
  def check(now = Time.now.to_i)
    lines, code = Check.report(now)
    (code.zero? ? $stdout : $stderr).puts(lines)
    exit code
  end

  # -- pace-aware downshift launcher ---------------------------------------------------
  def downshift_env(now = Time.now.to_i)
    wk = resolve_weekly(now)
    return [{}, "no usable usage data -> no downshift (fail open)"] if wk.nil? || wk[:confidence] == :stale

    p = Pool.pace(wk[:used], wk[:reset], now)
    fh = Pool.five_hour(now)
    fh_hot = fh && (fh[:age] || 0) <= Pool::STALE && fh[:used] >= FIVE_H_CAP
    tag = wk[:confidence] == :estimated ? " est" : ""
    down = { "CLAUDE_CODE_SUBAGENT_MODEL" => DMODEL, "CLAUDE_CODE_EFFORT_LEVEL" => DEFFORT }

    # 5h saturation throttles within minutes -> downshift first, regardless of weekly/coast.
    if fh_hot && p[:delta] <= MARGIN
      [down, "5h at #{fh[:used].round}% (#{wk[:used].round}% wk#{tag}) -> downshifting subagents to #{DMODEL}/#{DEFFORT}"]
    elsif p[:to_reset] < COAST # near weekly reset: unspent is lost anyway, let it burn.
      [{}, "#{wk[:used].round}% used#{tag}, reset in #{dur(p[:to_reset])} -> no downshift (burn it)"]
    elsif p[:delta] > MARGIN
      [down, "pace +#{p[:delta].round}pts (#{wk[:used].round}% used#{tag}) -> downshifting subagents to #{DMODEL}/#{DEFFORT}"]
    else
      [{}, "#{wk[:used].round}% used#{tag}, #{pace_phrase(p)} -> no downshift"]
    end
  end

  def run(argv, now = Time.now.to_i)
    sep = argv.index("--")
    cmd = sep ? argv[(sep + 1)..] : argv
    if cmd.nil? || cmd.empty?
      warn "usage: ccpool run -- <command...>"
      exit 2
    end
    exec(*cmd) if MODE == "off" # pure passthrough -- never downshift
    # respect an explicit user choice -- don't override a model the user set themselves.
    if ENV["CLAUDE_CODE_SUBAGENT_MODEL"]
      warn "[ccpool] CLAUDE_CODE_SUBAGENT_MODEL already set -> leaving it"
      exec(*cmd)
    end
    env, msg = downshift_env(now)
    if MODE == "advise" # print the recommendation, like the native tab -- but don't apply it
      warn "[ccpool] #{msg}#{env.empty? ? '' : ' (advise mode -> not applied; CCPOOL_DOWNSHIFT=auto to enforce)'}"
      exec(*cmd)
    end
    warn "[ccpool] #{msg}"
    exec(env, *cmd)
  end

  # -- retrospective provisioning review -----------------------------------------------
  def review(argv, now = Time.now.to_i)
    days = (argv.first.to_i.positive? ? argv.first.to_i : 7)
    r = Analyzer.review(days: days, now: now)
    puts "Model provisioning review -- last #{days}d"
    if r[:by_model].empty?
      puts "  no Claude turns found in the window."
      return
    end
    r[:by_model].first(6).each { |m, v| puts "  #{v[:turns].to_s.rjust(6)} turns  #{(v[:out] / 1000).to_s.rjust(6)}k out  #{m}" }
    if r[:exp_turns].positive?
      puts
      puts "  Expensive-model turns (opus/fable): #{r[:exp_turns]}"
      puts "  ...low-complexity (little output, no tools): #{r[:exp_trivial]} (#{r[:trivial_pct].round}%) -- candidates to downshift to sonnet/haiku"
      puts "  ~#{r[:trivial_out_pct].round}% of your expensive-model output tokens went to that trivial work."
    end
    puts
    puts "  caveat: effort isn't logged per-turn -- this proxies complexity from output volume +"
    puts "  tool-calls; `ultrathink`/thinking inflate output invisibly, so treat as a hint, not a verdict."
  end

  # -- read-only work-rhythm diagnostic ------------------------------------------------
  def rhythm(now = Time.now.to_i)
    puts Rhythm.report(now)
  end

  # -- help ----------------------------------------------------------------------------
  def help
    puts <<~TXT
      ccpool -- get the most out of your fixed Claude subscription pool.

      Usage: ccpool <command> [args]        (no command -> status)

      Commands:
        status             % used, ~$ API-equiv left, and pace vs how far through the week you are.
        check              time + budget + a keep-going/stop VERDICT, for long or autonomous loops.
        rhythm             your circadian work rhythm + a suggested pace profile (read-only).
        run -- <cmd...>    run <cmd>, downshifting subagent model/effort when you're ahead of pace.
        review [days]      retrospective: did you use the right model for the work? (default 7d)
        statusline         render the Claude Code statusLine; bare in a terminal shows a preview.
        warn               Claude Code hook: warn mid-turn on pace / 5h / context (stdin = payload).
        prune [--history]  delete stale snapshots (add --history to also compact the history file).
        help               this message (also -h, --help).

      Pace knobs:  CCPOOL_PACE_PROFILE=even|weekdays|workhours|custom · CCPOOL_WORK_DAYS=0-6 ·
                   CCPOOL_WAKE_HOURS=9-17 · CCPOOL_CLOCK=24|12|auto
      Full reference + env vars: see the README.
    TXT
  end
end

if $PROGRAM_NAME == __FILE__
  case cmd = ARGV.shift
  when "statusline" then CCPool.statusline
  when "status", nil then CCPool.status
  when "check" then CCPool.check
  when "warn" then CCPool.warn_hook
  when "run" then CCPool.run(ARGV)
  when "review" then CCPool.review(ARGV)
  when "rhythm" then CCPool.rhythm
  when "help", "-h", "--help" then CCPool.help
  when "prune"
    now = Time.now.to_i
    msg = "pruned #{CCPool.prune_caches(now)} stale snapshot(s)"
    msg += "; compacted #{CCPool.prune_history(now)} old history row(s)" if ARGV.include?("--history")
    puts "ccpool: #{msg}"
  else
    warn "ccpool: unknown command #{cmd.inspect}. Run `ccpool help` for usage."
    exit 2
  end
end
