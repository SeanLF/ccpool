# frozen_string_literal: true

# $/1%-of-weekly self-calibration. DELEGATES every dollar to ccusage (authoritative
# pricing; never hand-rolled) and uses rate-limit-history.jsonl only for the wk% deltas
# ccusage can't see. Pooled Δ-weighted over monotonic no-reset runs, Anthropic-only.
# Cached (TTL) because ccusage is a slow subprocess -- so `status` reads a number, not npx.
require "json"
require "time"

module Calibration
  CACHE   = File.expand_path(ENV["CCPOOL_CALIB_CACHE"] || "~/.claude/ccpool-calibration.json")
  TTL     = (ENV["CCPOOL_CALIB_TTL"] || "21600").to_i # 6h
  HIST    = File.expand_path(ENV["CCPOOL_HISTORY"] || "~/.claude/rate-limit-history.jsonl")
  # Pin the epoch, don't float @latest: ccusage ships ~10x/month with "epoch semver" (major
  # is marketing, not breaking), and the field surface we parse has been stable across all of
  # it -- but @latest silently inherits Node-version bumps (v19 dropped Node <22.11 = hard
  # failure) and fresh-rewrite regressions. @20 gets pricing/patch updates without those.
  CCUSAGE = ENV["CCPOOL_CCUSAGE_CMD"] || "npx -y ccusage@20"
  BLOCKS_CACHE = File.expand_path(ENV["CCPOOL_BLOCKS_CACHE"] || "~/.claude/ccpool-blocks-cache.json")
  BLOCKS_TTL   = 120 # don't re-spawn ccusage on every idle read

  module_function

  # $/1% of the weekly pool. Cached; falls back to a stale cache if recompute fails; nil
  # if never computable (no ccusage / no history) -> caller shows % without $.
  def dollar_per_pct(now = Time.now.to_i, force: false)
    cached = read_cache
    return cached["dpp"] if !force && cached && (now - cached["at"] < TTL) && cached["dpp"]

    dpp = compute
    write_cache(dpp, now) if dpp
    dpp || cached&.dig("dpp")
  end

  # Only ever hand back a Hash. A corrupt cache that parses to an array/number/string would make
  # callers (stale?/dollar_per_pct/render) crash on []-indexing -- and because the warmup that
  # would overwrite it crashes too, a bad cache self-perpetuates ($ blank forever). Hash-or-nil
  # turns that into "stale" -> recompute -> overwrite: the cache self-heals.
  def read_cache = (c = JSON.parse(File.read(CACHE)) rescue nil).is_a?(Hash) ? c : nil
  def write_cache(dpp, at) = File.write(CACHE, JSON.generate("dpp" => dpp, "at" => at)) rescue nil

  # Would dollar_per_pct recompute (spawn ccusage) right now? True when there's no usable cached
  # $/1% or it's aged past TTL. Callers use this to warm the cache out-of-band (see CCPool
  # .warm_calibration) so a render never has to block on the compute.
  def stale?(now = Time.now.to_i)
    c = read_cache
    c.nil? || c["dpp"].nil? || (now - c["at"].to_i) >= TTL
  end

  def compute
    runs = wk_runs
    return nil if runs.empty? # fresh install / no history -> don't even spawn ccusage

    blocks = ccusage_blocks
    return nil if blocks.nil? || blocks.empty?

    tot_cost = 0.0
    tot_dw = 0
    runs.each do |r|
      c = cost_between(blocks, r[:t0], r[:t1])
      next if c <= 0

      tot_cost += c
      tot_dw += r[:dw]
    end
    tot_dw.zero? ? nil : (tot_cost / tot_dw).round(4)
  end

  # Raw `ccusage blocks --json` with a short file cache, so tight staleness doesn't re-spawn
  # ccusage (a multi-second npx call) on every idle status read.
  def ccusage_raw(now = Time.now.to_i)
    c = JSON.parse(File.read(BLOCKS_CACHE)) rescue nil
    return c["raw"] if c.is_a?(Hash) && c["at"].is_a?(Numeric) && now - c["at"] < BLOCKS_TTL && c["raw"]

    raw = `#{CCUSAGE} blocks --json 2>/dev/null`
    File.write(BLOCKS_CACHE, JSON.generate("raw" => raw, "at" => now)) rescue nil unless raw.strip.empty?
    raw
  end

  # Anthropic-only 5h cost blocks (router models don't consume the pool -> drop mixed).
  def ccusage_blocks
    out = ccusage_raw
    return nil if out.strip.empty? # ccusage missing / npx failed -> fail open (no $)

    doc = JSON.parse(out)
    arr = doc.is_a?(Array) ? doc : (doc["blocks"] || doc.values.find { _1.is_a?(Array) })
    if arr.nil? # ccusage ran but the shape changed -> fail LOUD, don't silently zero the $
      warn "[ccpool] ccusage (#{CCUSAGE}) returned an unexpected shape (no 'blocks' array); $ readout disabled until fixed"
      return nil
    end
    # `models` can be prefixed for non-Claude agents (e.g. "[pi] gpt-5.4"); the regex still
    # correctly excludes those and keeps prefixed-Claude.
    anthropic = ->(m) { m.to_s.match?(/claude|anthropic/i) }
    (arr || []).reject { _1["isGap"] }.filter_map do |b|
      next if (m = b["models"] || []).any? && !m.all?(&anthropic)

      s = Time.parse(b["startTime"]).to_i
      e = Time.parse(b["actualEndTime"] || b["endTime"]).to_i
      c = b["costUSD"]
      next unless c.is_a?(Numeric) && e > s

      { s: s, e: e, cost: c.to_f }
    end
  rescue StandardError
    nil
  end

  # Anthropic-only $ spent in [t0, t1] -- for extrapolating a stale % forward. nil if
  # ccusage unavailable.
  def cost_since(t0, t1)
    (b = ccusage_blocks) && cost_between(b, t0, t1)
  end

  def cost_between(blocks, t0, t1)
    blocks.sum do |b|
      ov = [[b[:e], t1].min - [b[:s], t0].max, 0].max
      ov.zero? ? 0.0 : b[:cost] * ov / (b[:e] - b[:s])
    end
  end

  # Monotonic wk% climbs within one window (no reset), per (boundary, minute) max wk;
  # drop runs whose start postdates their own boundary (stale-session readings).
  def wk_runs
    return [] unless File.exist?(HIST)

    by = Hash.new { |h, k| h[k] = Hash.new(-1) }
    File.foreach(HIST) do |l|
      r = JSON.parse(l) rescue next
      next unless r["wk"].is_a?(Numeric) && r["wk_reset"].is_a?(Numeric) && r["t"].is_a?(Numeric)

      m = (r["t"] / 60) * 60
      by[r["wk_reset"]][m] = r["wk"] if r["wk"] > by[r["wk_reset"]][m]
    end

    runs = []
    by.each do |bnd, mins|
      sorted = mins.sort
      run = [sorted.first]
      sorted.each_cons(2) do |(_, a), (t2, b)|
        if b < a - 1              # weekly fell hard -> a reset boundary
          runs << [bnd, run]
          run = [[t2, b]]
        elsif b != run.last[1]    # a real wk CHANGE -> record the transition. Skipping flat minutes
          run << [t2, b]          # keeps ses-only padding rows from extending t1 and inflating $/1%.
        end
      end
      runs << [bnd, run]
    end
    runs.filter_map do |bnd, run|
      dw = run.last[1] - run.first[1]
      dt = run.last[0] - run.first[0]
      next if dw < 3 || dt < 3_600 || run.first[0] > bnd + 300

      { t0: run.first[0], t1: run.last[0], dw: dw }
    end
  rescue StandardError
    []
  end
end
