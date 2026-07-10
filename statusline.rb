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
  EIGHTHS = [" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"].freeze
  SOLID = "█"
  TRACK = "░"
  RESET = "\e[0m"
  DIM   = "\e[2m"
  YELLOW = "\e[93m"
  RED   = "\e[91m" # bright, legible on dark backgrounds
  # 24-bit truecolour teal-cyan: theme-INDEPENDENT. A 16-colour code (\e[96m) gets remapped by
  # the terminal palette -- Ghostty rendered cyan as pink -- so we pin the actual RGB. Calm/cool
  # when healthy; the over-pace tail overrides to red. Override via CCPOOL_BAR_COLOR.
  BAR   = ENV["CCPOOL_BAR_COLOR"] || "\e[38;2;86;182;194m"
  BOLD  = "\e[1m"
  SEP   = " #{DIM}·#{RESET} "
  WEEK  = 7 * 86_400
  CACHE_TTL  = (ENV["USAGE_CACHE_TTL_SECS"] || "3600").to_i
  CACHE_WARN = (ENV["USAGE_CACHE_WARN_SECS"] || "900").to_i
  CACHE_CRIT = (ENV["USAGE_CACHE_CRIT_SECS"] || "180").to_i
  TIER = ENV["USAGE_TIER"] || "max_20x"

  module_function

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
        "#{BAR}#{EIGHTHS[[((used_w - i) * 8).round, 1].max]}#{RESET}" # coloured partial edge
      else
        "#{DIM}#{TRACK}#{RESET}" # dim remaining -> contrast with the fill
      end
    end.join
  end

  # Last-activity epoch + live prompt-cache TTL, detected from the transcript tail's
  # cache buckets (1h on subscription, 5m on paid credits). => [epoch, ttl|nil] or nil.
  def cache_state(path)
    return nil unless path.is_a?(String) && File.exist?(path)

    tail = File.open(path, "rb") { |f| f.seek([f.size - 32_768, 0].max); f.read }
    lines = tail.to_s.lines
    lines.shift if tail.bytesize >= 32_768 && lines.size > 1
    entries = lines.filter_map { |l| (JSON.parse(l) rescue nil) }.select { |e| e.is_a?(Hash) }

    tse = entries.reverse_each.find { |e| e["timestamp"].is_a?(String) } or return nil
    ts = (Time.parse(tse["timestamp"]).to_i rescue return nil)
    ttl = nil
    entries.reverse_each do |e|
      cc = e.dig("message", "usage", "cache_creation")
      next unless cc.is_a?(Hash)

      if cc["ephemeral_5m_input_tokens"].to_i.positive? then ttl = 300; break
      elsif cc["ephemeral_1h_input_tokens"].to_i.positive? then ttl = 3_600; break
      end
    end
    [ts, ttl]
  rescue StandardError
    nil
  end

  # Render the whole line from the fresh CC payload (rate_limits is account-global, so the
  # payload IS current). `dpp` = cached $/1% (fast; never spawn ccusage in the statusline).
  def render(data, now = Time.now.to_i)
    cols = (ENV["COLUMNS"] || "120").to_i
    rl = data["rate_limits"].is_a?(Hash) ? data["rate_limits"] : {}
    dpp = Calibration.read_cache&.dig("dpp")
    now_grp = []
    ses_grp = []
    wk_grp = []

    # context window %
    cw = data["context_window"]
    if cw.is_a?(Hash)
      ctx = cw["used_percentage"].is_a?(Numeric) ? cw["used_percentage"].to_f : nil
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
      if left <= 0 then now_grp << "cache #{BOLD}#{RED}cold#{RESET}"
      elsif left < CACHE_WARN
        col = left < CACHE_CRIT ? "#{BOLD}#{RED}" : YELLOW
        now_grp << "cache #{col}#{fmt_dur(left)}#{RESET}"
      end
    end

    # 5h session
    fh = rl["five_hour"]
    if fh.is_a?(Hash) && fh["used_percentage"].is_a?(Numeric)
      s = fh["used_percentage"].round
      seg = "ses #{sev("#{s}%", s, warn: 80, crit: 92)}"
      seg += " #{fmt_dur(fh['resets_at'] - now)}" if fh["resets_at"].is_a?(Numeric)
      ses_grp << seg
    end

    # weekly meter + $ + day-share
    sd = rl["seven_day"]
    if sd.is_a?(Hash) && sd["used_percentage"].is_a?(Numeric)
      used = sd["used_percentage"].to_f
      width = [[cols - 82, 40].min, 14].max
      wknum = sev("#{used.round}%", used.round, warn: 75, crit: 90)
      left = (100 - used) * (dpp || 0)
      dollars = dpp ? " #{DIM}#{left >= 1000 ? "$#{(left / 1000).round(1)}k" : "$#{left.round}"}#{RESET}" : ""
      if sd["resets_at"].is_a?(Numeric)
        reset = sd["resets_at"]
        pace = ((now - (reset - WEEK)).to_f / WEEK).clamp(0.0, 1.0)
        days_left = [(reset - now).to_f / 86_400, 0.0001].max
        day = [100 - used, (100 - used) / days_left].min.clamp(0, 100) # burnable %/remaining-day
        wk_grp << "wk #{meter(used / 100.0, pace, width)} #{wknum}#{dollars} #{fmt_dur(reset - now)} #{DIM}day #{day.round}%#{RESET}"
      else
        wk_grp << "wk #{meter(used / 100.0, nil, width)} #{wknum}#{dollars}"
      end
    end

    [now_grp, ses_grp, wk_grp].reject(&:empty?).map { |g| g.join("  ") }.join(SEP)
  end
end
