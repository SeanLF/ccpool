# frozen_string_literal: true

# Rich statusLine renderer -- the port of statusline-command.rb into ccpool, with two
# changes: the pace bar is now COLOURED (over-pace cells go red -- the monochrome hatch
# was too subtle to read), and it adds the pool $-value. Groups by timescale:
#   now (context window + cache-TTL) · ses (5h) · wk (weekly meter + $ + day-share)
# ANSI is officially supported in statuslines (code.claude.com/docs/en/statusline).
require "json"
require "time"
require_relative "pool"
require_relative "calibration"

module Statusline
  # Honour the NO_COLOR contract (present + non-empty, per no-color.org) and TERM=dumb. We CAN'T gate on tty here: Claude
  # Code invokes the statusLine with a non-tty stdout yet renders ANSI, so tty-gating would strip
  # colour in the primary use. NO_COLOR is the user's explicit opt-out; colour off -> every escape
  # below collapses to "", so the line degrades to plain text (the bar/meter still read via glyphs).
  COLOR = ENV["NO_COLOR"].to_s.empty? && ENV["TERM"] != "dumb"
  def self.ansi(code) = COLOR ? code : ""

  EIGHTHS = [" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"].freeze
  SOLID = "█"
  TRACK = "░"
  RESET = ansi("\e[0m")
  DIM   = ansi("\e[2m")
  YELLOW = ansi("\e[93m")
  RED   = ansi("\e[91m") # bright, legible on dark backgrounds
  # 24-bit truecolour teal-cyan: theme-INDEPENDENT. A 16-colour code (\e[96m) gets remapped by
  # the terminal palette -- Ghostty rendered cyan as pink -- so we pin the actual RGB. Calm/cool
  # when healthy; the over-pace tail overrides to red. Override via CCPOOL_BAR_COLOR.
  BAR   = ansi(ENV["CCPOOL_BAR_COLOR"] || "\e[38;2;86;182;194m")
  BOLD  = ansi("\e[1m")
  SEP   = " #{DIM}·#{RESET} ".freeze # interpolated -> not auto-frozen by the magic comment
  WEEK  = 7 * 86_400
  CACHE_TTL  = 3600 # statusline staleness display tiers (secs): fresh past this
  CACHE_WARN = 900  # ...dim/warn past this
  CACHE_CRIT = 180  # ...and flag as critically stale past this
  TIER = ENV["USAGE_TIER"] || "max_20x"
  LOG  = File.expand_path(ENV["CCPOOL_STATUSLINE_LOG"] || "~/.claude/statusline.log")

  module_function

  # Best-effort anomaly log, capped to the last 200 lines. The happy path writes NOTHING --
  # only a Claude Code payload-schema change or an unexpected error lands here, so a silent
  # segment drop (below) still leaves a trail: `tail -f ~/.claude/statusline.log`. Never raises.
  def log(level, msg)
    msg  = msg.to_s.gsub(/\s*\n\s*/, " ").slice(0, 500) # one entry is always one line
    prev = File.exist?(LOG) ? File.readlines(LOG) : []
    prev << "#{Time.now.strftime('%F %T')} [#{level}] #{msg}\n"
    File.write(LOG, prev.last(200).join)
  rescue StandardError
    nil
  end

  # True if hash[key] is the expected type. Present-but-wrong-type LOGS a warning (the signal a
  # CC schema change silently dropped a segment); a missing key is silent + expected. Callers
  # && these so a false result skips the segment without touching the wrong-typed value.
  # label defaults to the key; pass a dotted path for nested fields.
  def typed?(hash, key, type, label = key)
    v = hash[key]
    return true if v.is_a?(type)

    log("warn", "#{label} is #{v.class}, expected #{type}") unless v.nil?
    false
  end

  def sev(text, pct, warn:, crit:)
    return "#{RED}#{text}#{RESET}" if pct >= crit
    return "#{YELLOW}#{text}#{RESET}" if pct >= warn

    text
  end

  def fmt_dur(secs)
    secs = 0 if secs.negative?
    d, r = secs.divmod(86_400)
    h, r = r.divmod(3_600)
    m, = r.divmod(60)
    return "#{d}d#{h}h" if d.positive?
    return "#{h}h#{m}m" if h.positive?

    "#{m}m"
  end

  def fmt_size(n)
    return nil unless n.is_a?(Numeric) && n.positive?

    n >= 1_000_000 ? "#{(n / 1_000_000.0).round}M" : "#{(n / 1000.0).round}k"
  end

  # $-left readout, shared by both weekly renderers: "$1.2k" past a grand, else "$47".
  def fmt_dollars(n) = n >= 1000 ? "$#{(n / 1000).round(1)}k" : "$#{n.round}"

  # COLOURED meter: on-pace used cells solid (plain), over-pace used cells RED, partial
  # leading edge in eighths, remaining dim. The red tail IS the "burning too fast" signal.
  def meter(used_frac, pace_frac, width)
    used_w = used_frac * width
    pace_w = (pace_frac || 1.0) * width
    (0...width).map do |i|
      if i + 1 <= used_w
        col = (i + 0.5) >= pace_w ? RED : BAR # over-pace tail red, on-pace fill cyan
        "#{col}#{SOLID}#{RESET}"
      elsif i < used_w
        col = (i + 0.5) >= pace_w ? RED : BAR # partial edge follows the same over-pace rule (no cyan sliver after the red tail)
        "#{col}#{EIGHTHS[[((used_w - i) * 8).round, 1].max]}#{RESET}"
      else
        "#{DIM}#{TRACK}#{RESET}" # dim remaining -> contrast with the fill
      end
    end.join
  end

  # Last-activity epoch + live prompt-cache TTL, detected from the transcript tail's
  # cache buckets (1h on subscription, 5m on paid credits). => [epoch, ttl|nil] or nil.
  def cache_state(path)
    return nil unless path.is_a?(String) && File.exist?(path)

    tail = File.open(path, "rb") do |f|
      f.seek([f.size - 32_768, 0].max)
      f.read
    end
    lines = tail.to_s.lines
    lines.shift if tail.bytesize >= 32_768 && lines.size > 1
    entries = lines.filter_map { |l| JSON.parse(l) rescue nil }.grep(Hash)

    tse = entries.rfind { |e| e["timestamp"].is_a?(String) } or return nil
    ts = (Time.parse(tse["timestamp"]).to_i rescue return nil) # rubocop:disable Style/RedundantParentheses -- parens are REQUIRED to scope `rescue return`
    ttl = nil
    entries.reverse_each do |e|
      cc = e.dig("message", "usage", "cache_creation")
      next unless cc.is_a?(Hash)

      if cc["ephemeral_5m_input_tokens"].to_i.positive?
        ttl = 300
        break
      elsif cc["ephemeral_1h_input_tokens"].to_i.positive?
        ttl = 3_600
        break
      end
    end
    [ts, ttl]
  rescue StandardError
    nil
  end

  # Render the whole line from the fresh CC payload (rate_limits is account-global, so the
  # payload IS current). `dpp` = cached $/1% (fast; never spawn ccusage in the statusline).
  # rubocop:disable Metrics/AbcSize -- inline segment-assembler (ctx/cache/5h/weekly); linear display code, deliberately not fragmented into helpers
  def render(data, now = Time.now.to_i)
    cols = (ENV["COLUMNS"] || "120").to_i
    rl = typed?(data, "rate_limits", Hash) ? data["rate_limits"] : {}
    dpp = Calibration.read_cache&.dig("dpp")
    now_grp = []
    ses_grp = []
    wk_grp = []

    # context window %
    if typed?(data, "context_window", Hash)
      cw = data["context_window"]
      ctx = typed?(cw, "used_percentage", Numeric, "context_window.used_percentage") ? cw["used_percentage"].to_f : nil
      if ctx
        seg = "ctx #{sev("#{ctx.round}%", ctx.round, warn: 70, crit: 90)}"
        (s = fmt_size(cw["context_window_size"])) && (seg += " #{s}")
        now_grp << seg
      end
    end

    # prompt-cache countdown (only when near expiry)
    if (st = cache_state(data["transcript_path"]))
      ts, ttl = st
      left = ts + (ttl || CACHE_TTL) - now
      if left <= 0
        now_grp << "cache #{BOLD}#{RED}cold#{RESET}"
      elsif left < CACHE_WARN
        col = left < CACHE_CRIT ? "#{BOLD}#{RED}" : YELLOW
        now_grp << "cache #{col}#{fmt_dur(left)}#{RESET}"
      end
    end

    # 5h session
    fh = rl["five_hour"]
    if typed?(rl, "five_hour", Hash) && typed?(fh, "used_percentage", Numeric, "five_hour.used_percentage")
      s = fh["used_percentage"].round
      seg = "ses #{sev("#{s}%", s, warn: 80, crit: 92)}"
      seg += " #{fmt_dur(fh['resets_at'] - now)}" if typed?(fh, "resets_at", Numeric, "five_hour.resets_at")
      ses_grp << seg
    end

    # weekly meter + $ + day-share
    sd = rl["seven_day"]
    if typed?(rl, "seven_day", Hash) && typed?(sd, "used_percentage", Numeric, "seven_day.used_percentage")
      used = sd["used_percentage"].to_f
      width = (cols - 82).clamp(14, 40)
      wknum = sev("#{used.round}%", used.round, warn: 75, crit: 90)
      left = (100 - used) * (dpp || 0)
      dollars = dpp ? " #{DIM}#{fmt_dollars(left)}#{RESET}" : ""
      if typed?(sd, "resets_at", Numeric, "seven_day.resets_at")
        reset = sd["resets_at"]
        pace = Profile.elapsed_fraction(reset - WEEK, now, reset) # same weighting as Pool.pace -> bar agrees with verdict
        days_left = [(reset - now).to_f / 86_400, 0.0001].max
        day = [100 - used, (100 - used) / days_left].min.clamp(0, 100) # burnable %/remaining-day
        wk_grp << "wk #{meter(used / 100.0, pace, width)} #{wknum}#{dollars} #{fmt_dur(reset - now)} #{DIM}day #{day.round}%#{RESET}"
      else
        wk_grp << "wk #{meter(used / 100.0, nil, width)} #{wknum}#{dollars}"
      end
    end

    [now_grp, ses_grp, wk_grp].reject(&:empty?).map { |g| g.join("  ") }.join(SEP)
  end
  # rubocop:enable Metrics/AbcSize

  # Compact one-segment render for EMBEDDING inside another statusline (e.g. as a ccstatusline
  # custom-command widget, which forwards the full payload incl. rate_limits -- verified). Shows
  # ONLY ccpool's differentiator -- pool $-left + pace -- and leaves ctx/5h/model/git to the host,
  # so we don't duplicate what it already renders. "" when there's no weekly window to speak to.
  def render_compact(data, now = Time.now.to_i)
    rl = typed?(data, "rate_limits", Hash) ? data["rate_limits"] : {}
    sd = rl["seven_day"]
    return "" unless typed?(rl, "seven_day", Hash) && typed?(sd, "used_percentage", Numeric, "seven_day.used_percentage")

    used = sd["used_percentage"].to_f
    parts = ["pool #{sev("#{used.round}%", used.round, warn: 75, crit: 90)}"]

    if (dpp = Calibration.read_cache&.dig("dpp")) # cache-only: NEVER spawn ccusage in a render path
      left = (100 - used) * dpp
      parts << "#{DIM}#{fmt_dollars(left)}#{RESET}"
    end

    # pace: over-pace (burning fast) is the red risk signal, under-pace is banked headroom (cyan) --
    # same convention as the meter's red tail, so the compact line agrees with the full one.
    if typed?(sd, "resets_at", Numeric, "seven_day.resets_at")
      reset = sd["resets_at"]
      d = (used - (Profile.elapsed_fraction(reset - WEEK, now, reset) * 100)).round
      parts << (d.positive? ? "#{RED}+#{d}↑#{RESET}" : "#{BAR}#{d}↓#{RESET}") if d.abs >= 1
    end

    parts.join(" ")
  end
end
