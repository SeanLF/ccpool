# frozen_string_literal: true

# `ccpool init` -- wire ccpool into Claude Code with ZERO required config.
#
# Dry-run by DEFAULT: computes the settings.json changes and prints a diff; writes nothing.
# `--apply` merges them in after a timestamped backup. The merge is:
#   * idempotent      -- re-running detects existing ccpool wiring, never duplicates it;
#   * never-clobber   -- preserves every other hook / permission / key untouched;
#   * symlink-aware   -- ~/.claude/settings.json is often a symlink to a dotfiles source, so
#                        we follow it and rewrite the REAL target, leaving the symlink intact
#                        (the exact hazard that bit us before);
#   * fail-safe       -- a present-but-unparseable settings file ABORTS with guidance rather
#                        than overwriting something we can't understand. init is on-demand
#                        (not a hook), so it reports errors loudly instead of failing silent.
require "json"
require "fileutils"

module Init
  module_function

  SETTINGS = File.expand_path(ENV["CCPOOL_SETTINGS"] || "~/.claude/settings.json")
  # The bin/ccpool launcher (robust: uses PATH ruby, falls back to mise) -- the right thing to
  # wire so hooks work even when `ruby` isn't on the hook's PATH. __dir__ = the repo root.
  LAUNCHER       = File.expand_path("bin/ccpool", __dir__)
  STATUSLINE_CMD = "#{LAUNCHER} statusline".freeze
  WARN_CMD       = "#{LAUNCHER} warn".freeze
  WARN_EVENTS    = %w[UserPromptSubmit PostToolUse].freeze

  # -- detection (idempotency) ---------------------------------------------------------
  # Does this command already invoke ccpool with `verb`? Matches BOTH `ruby /x/ccpool.rb warn`
  # and `/x/bin/ccpool warn`, so init recognises a hand-wired setup (Sean's) as already-done.
  def ccpool_cmd?(cmd, verb)
    cmd.is_a?(String) && cmd.include?("ccpool") && cmd.match?(/\b#{verb}\b/)
  end

  def warn_wired?(settings, event)
    groups = settings.dig("hooks", event)
    groups.is_a?(Array) && groups.any? do |g|
      g.is_a?(Hash) && g["hooks"].is_a?(Array) &&
        g["hooks"].any? { |h| h.is_a?(Hash) && ccpool_cmd?(h["command"], "warn") }
    end
  end

  # :absent (no statusLine) | :ours (already ccpool) | :foreign (some other command we must
  # not clobber). ccpool needs to OWN the statusLine because that's what captures rate_limits.
  def statusline_state(settings)
    sl = settings["statusLine"]
    return :absent unless sl.is_a?(Hash) && sl["command"].is_a?(String)

    ccpool_cmd?(sl["command"], "statusline") ? :ours : :foreign
  end

  # -- plan (pure: settings hash -> what would change) ---------------------------------
  def plan(settings, replace_statusline: false)
    sl    = statusline_state(settings)
    hooks = WARN_EVENTS.to_h { |e| [e, warn_wired?(settings, e) ? :present : :missing] }
    {
      statusline: sl,
      statusline_existing: (settings.dig("statusLine", "command") if sl == :foreign),
      add_statusline: sl == :absent || (sl == :foreign && replace_statusline),
      # a foreign statusLine we're leaving alone -- capture won't work until it's resolved
      conflict: sl == :foreign && !replace_statusline,
      hooks: hooks,
      add_hooks: hooks.select { |_, v| v == :missing }.keys
    }
  end

  def changes?(pl) = pl[:add_statusline] || !pl[:add_hooks].empty?

  # -- apply (pure-ish: mutate the settings hash per the plan) --------------------------
  def apply_plan!(settings, pl)
    settings["statusLine"] = { "type" => "command", "command" => STATUSLINE_CMD, "refreshInterval" => 10 } if pl[:add_statusline]
    unless pl[:add_hooks].empty?
      hooks = (settings["hooks"] ||= {})
      pl[:add_hooks].each do |event|
        (hooks[event] ||= []) << { "hooks" => [{ "type" => "command", "command" => WARN_CMD }] }
      end
    end
    settings
  end

  # -- IO --------------------------------------------------------------------------------
  # Follow the symlink so we edit the real dotfiles target, not replace the link. A missing
  # file is a fresh install (edit the literal path, creating it on write).
  def real_target = File.exist?(SETTINGS) ? File.realpath(SETTINGS) : SETTINGS

  # A symlink whose target doesn't currently exist. `File.exist?` follows links and reads
  # FALSE for this, so without an explicit check init would mistake it for a fresh install and
  # `File.rename` over the link -- destroying the symlink and severing the dotfiles wiring (the
  # exact hazard this file promises to avoid). `File.symlink?` sees the link regardless of target.
  def dangling_symlink?(path) = File.symlink?(path) && !File.exist?(path)

  # nil = no file (fresh). :unreadable = present but not a JSON object (refuse to touch).
  def load_settings(path)
    return nil unless File.exist?(path)

    parsed = JSON.parse(File.read(path))
    parsed.is_a?(Hash) ? parsed : :unreadable
  rescue JSON::ParserError
    :unreadable
  end

  def backup_settings(path, now)
    return nil unless File.exist?(path)

    bak = "#{path}.bak.#{now}"
    bak = "#{bak}.#{Process.pid}" if File.exist?(bak) # never overwrite an existing backup
    FileUtils.cp(path, bak)
    bak
  end

  # Atomic write: tmp in the same dir + rename, so a crash can't leave a half-written file.
  # `path` is the resolved real target, so this preserves any symlink pointing at it.
  def write_settings(path, settings)
    FileUtils.mkdir_p(File.dirname(path))
    tmp = "#{path}.#{Process.pid}.tmp"
    File.write(tmp, "#{JSON.pretty_generate(settings)}\n")
    File.rename(tmp, path)
  end

  def which(cmd)
    ENV["PATH"].to_s.split(File::PATH_SEPARATOR).any? { |d| File.executable?(File.join(d, cmd)) }
  rescue StandardError
    false
  end

  # ccusage powers the $ calibration -- but ccpool works without it (just no dollar value), so
  # this reports, never blocks. Probe the invocation's first token (usually `npx`) on PATH.
  def ccusage_line
    cmd = (ENV["CCPOOL_CCUSAGE_CMD"] || "npx -y ccusage@20").split.first
    if which(cmd)
      "ccusage: `#{cmd}` found -> the $ value self-calibrates from a few days of usage history."
    else
      "ccusage: `#{cmd}` NOT found -> ccpool still works, but the $ readout stays blank until it's installed."
    end
  end

  # -- rendering -------------------------------------------------------------------------
  def mark(sym, label, val, note = nil)
    s = "  #{sym} #{label.ljust(22)} #{val}"
    s += "\n      #{note}" if note
    s
  end

  def render_header(target)
    hdr = "ccpool init -- wiring plan for #{SETTINGS}"
    hdr += "\n  real target: #{target} (via symlink)" if target != SETTINGS
    hdr
  end

  def render_plan(pl)
    lines = []
    case pl[:statusline]
    when :absent
      lines << mark("+", "statusLine", STATUSLINE_CMD, "captures rate_limits + renders the pool line")
    when :ours
      lines << mark("=", "statusLine", "already wired to ccpool")
    when :foreign
      lines << if pl[:add_statusline]
                 mark("~", "statusLine", "REPLACE with ccpool", "was: #{pl[:statusline_existing]}")
               else
                 mark("!", "statusLine", "left as-is (a non-ccpool command is set)",
                      "ccpool must own the statusLine to capture rate_limits -- re-run with --replace-statusline to take it over")
               end
    end
    WARN_EVENTS.each do |e|
      lines << if pl[:hooks][e] == :present
                 mark("=", "#{e} hook", "ccpool warn already present")
               else
                 mark("+", "#{e} hook", WARN_CMD, "warn the agent mid-turn on pace / 5h / context")
               end
    end
    lines.join("\n")
  end

  def preview
    puts
    puts "Statusline preview:"
    CCPool.preview_statusline
  end

  # -- orchestration ---------------------------------------------------------------------
  def run(argv, now = Time.now.to_i)
    apply      = argv.include?("--apply")
    replace_sl = argv.include?("--replace-statusline")

    if dangling_symlink?(SETTINGS)
      warn "ccpool init: #{SETTINGS} is a symlink to #{File.readlink(SETTINGS)}, which doesn't exist."
      warn "Create or stow that target first so init edits the real file (not the link), then re-run."
      exit 1
    end

    target   = real_target
    existing = load_settings(target)
    if existing == :unreadable
      warn "ccpool init: #{SETTINGS} exists but isn't a JSON object -- refusing to touch it."
      warn "Fix or move it, then re-run. (ccpool never overwrites a settings file it can't parse.)"
      exit 1
    end
    settings = existing || {}

    pl = plan(settings, replace_statusline: replace_sl)
    puts render_header(target)
    puts render_plan(pl)
    puts
    puts ccusage_line

    unless changes?(pl)
      puts
      if pl[:conflict]
        puts "Your warn hooks are wired, but the statusLine points at a non-ccpool command, so"
        puts "ccpool can't capture rate_limits. Re-run `ccpool init --apply --replace-statusline`"
        puts "to take it over (your current one is backed up first)."
      else
        puts "Already set up -- nothing to change."
      end
      preview
      return
    end

    unless apply
      puts
      puts "This is a DRY RUN -- nothing was written."
      puts "Run `ccpool init --apply` to apply it (a timestamped backup is taken first)."
      return
    end

    backup = backup_settings(target, now)
    apply_plan!(settings, pl)
    write_settings(target, settings)

    puts
    puts "You're set up. #{backup ? "Backup: #{backup}" : '(no prior settings to back up)'}"
    puts "ccpool is now wired -- open Claude Code and it starts capturing your pool usage."
    preview
  end
end
