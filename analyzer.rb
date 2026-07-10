# frozen_string_literal: true

# Retrospective over/under-provisioning review -- first-in-class: no tool judges whether
# you used the RIGHT model for the work. Effort isn't logged per turn, so we proxy
# "complexity" from output-token volume + tool-call count (Anthropic's own signal: high
# effort ~= more output + more tool calls). Flags expensive-model turns doing trivial
# work (candidates to downshift) and cheap-model turns that look like they struggled.
# Heuristic by nature -- the caller must disclose the caveats.
require "json"
require "time"

module Analyzer
  PROJECTS  = File.expand_path(ENV["CCPOOL_PROJECTS"] || "~/.claude/projects")
  EXPENSIVE = /opus|fable/i
  LOW_OUT   = (ENV["CCPOOL_LOW_OUTPUT"] || "500").to_i # below this + no tools => trivial turn

  module_function

  # => summary hash over the last `days` of transcript turns (main + subagent).
  def review(days: 7, now: Time.now.to_i)
    cutoff = now - (days * 86_400)
    exp_turns = 0
    exp_out = 0
    exp_trivial = 0
    exp_trivial_out = 0
    by_model = Hash.new { |h, k| h[k] = { turns: 0, out: 0 } }

    Dir.glob("#{PROJECTS}/**/*.jsonl").each do |f|
      next if File.mtime(f).to_i < cutoff

      File.foreach(f) do |l|
        next unless l.include?("output_tokens")

        j = JSON.parse(l) rescue next
        next unless j["type"] == "assistant"

        m = j.dig("message", "model")
        next if m.nil? || m == "<synthetic>" || !m.match?(/claude/i) # skip router/synthetic

        u = j.dig("message", "usage") or next
        ts = Time.parse(j["timestamp"].to_s).to_i rescue 0
        next if ts < cutoff

        out = u["output_tokens"].to_i
        tools = (j.dig("message", "content") || []).count { |c| c.is_a?(Hash) && c["type"] == "tool_use" }
        by_model[m][:turns] += 1
        by_model[m][:out] += out
        next unless m.match?(EXPENSIVE)

        exp_turns += 1
        exp_out += out
        if out < LOW_OUT && tools.zero?
          exp_trivial += 1
          exp_trivial_out += out
        end
      end
    end

    {
      days: days,
      by_model: by_model.sort_by { |_, v| -v[:turns] },
      exp_turns: exp_turns,
      exp_out: exp_out,
      exp_trivial: exp_trivial,
      exp_trivial_out: exp_trivial_out,
      trivial_pct: exp_turns.zero? ? 0 : (100.0 * exp_trivial / exp_turns),
      trivial_out_pct: exp_out.zero? ? 0 : (100.0 * exp_trivial_out / exp_out)
    }
  end
end
