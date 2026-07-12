package main

// Per-command help, hand-rolled (no cobra -- ccpool stays a near-stdlib `switch os.Args[1]`; see
// docs/DECISIONS.md). `ccpool <cmd> --help` / `-h` prints the entry; the top-level `usage()` lists them.

// commandHelp maps a command to its detailed help text. Commands not present (version, help, the
// internal __warm-calib) fall back to the top-level usage.
var commandHelp = map[string]string{
	"status": `ccpool status -- weekly pool readout (bare ` + "`ccpool`" + ` runs this).

Usage: ccpool status

% of the weekly pool used, ~$ API-equivalent left (ccusage-calibrated), pace vs how far through the
week you are, a reset-robust burn projection, and working-hours runway. Reads the local store the
statusline populates.`,

	"check": `ccpool check -- time + budget + a keep-going/stop VERDICT.

Usage: ccpool check

For long or autonomous loops: the time, the 5h session and weekly windows, a burn projection, and a
VERDICT (KEEP GOING / COAST / WIND DOWN / SESSION-LIMITED / ...). Exit 0 with a readout, 2 on no data.`,

	"statusline": `ccpool statusline [--embed|--compact] -- render the Claude Code statusLine.

Usage: ccpool statusline [--embed]        (stdin = the CC payload)

Prints the status line from the payload; in a terminal (no payload) shows a preview from the newest
snapshot. --embed/--compact prints only the $-left + pace gauge, to embed in another statusline.
Wire it as your statusLine command with ` + "`ccpool init`" + `.`,

	"warn": `ccpool warn -- Claude Code hook: mid-turn pace / 5h / context warnings.

Usage: ccpool warn        (stdin = the hook payload)

Fails open and silent -- a hook must never break Claude Code. Wired into UserPromptSubmit and
PostToolUse by ` + "`ccpool init`" + `.`,

	"run": `ccpool run -- <command...> -- pace-aware subagent downshift launcher.

Usage: ccpool run -- <command...>

Runs <command>, downshifting subagent model/effort when you're ahead of pace or the 5h window is
saturating. Everything after -- is the command, passed through untouched.
Knobs: CCPOOL_DOWNSHIFT=auto|advise|off, CCPOOL_DOWNSHIFT_MODEL, CCPOOL_DOWNSHIFT_EFFORT.`,

	"review": `ccpool review [days] -- retrospective on model choice.

Usage: ccpool review [days]        (default 7)

Did you use the right model for the work over the last N days? Read-only.`,

	"rhythm": `ccpool rhythm -- your circadian work rhythm + a suggested pace profile.

Usage: ccpool rhythm

Read-only. Suggests a CCPOOL_PACE_PROFILE / work-days / wake-hours shape from your activity.`,

	"init": `ccpool init [--apply] [--replace-statusline] -- wire ccpool into Claude Code.

Usage: ccpool init [--apply]

Dry-run diff by default; --apply writes (after a timestamped backup). Idempotent, never-clobber,
symlink-aware; a dangling settings.json symlink ABORTS rather than being clobbered. Also seeds
~/.ccpool/ccpool.json from your detected rhythm (same dry-run/--apply).`,

	"config": `ccpool config <show|init> -- inspect or seed the config file.

Usage:
  ccpool config show                        effective value of every setting + its source (env/file/default)
  ccpool config init [--apply] [--force]    (re-)seed ~/.ccpool/ccpool.json from your rhythm
                                            (dry-run by default; --force overwrites instead of fill-missing)`,

	"prune": `ccpool prune [--history] -- delete stale rows from the store.

Usage: ccpool prune [--history]

Deletes snapshot rows older than CCPOOL_CACHE_KEEP_SECS (default 1h). --history also compacts
history rows older than CCPOOL_HISTORY_KEEP_DAYS (default 30).`,
}

// wantsHelp reports whether the command's own args request help. It stops at "--" so that a flag
// meant for a wrapped command (`ccpool run -- foo --help`) is NOT treated as a request for run's help.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
