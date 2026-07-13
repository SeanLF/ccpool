// Command ccpool helps get the most out of a fixed Claude subscription pool.
//
// One static Go binary; the durable interop boundary is the on-disk SQLite store
// (see internal/store). main dispatches on os.Args[1]; bare invocation -> status.
package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SeanLF/ccpool/internal/analyzer"
	"github.com/SeanLF/ccpool/internal/configcmd"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/initcmd"
	"github.com/SeanLF/ccpool/internal/rhythm"
	"github.com/SeanLF/ccpool/internal/run"
	"github.com/SeanLF/ccpool/internal/status"
	"github.com/SeanLF/ccpool/internal/statusline"
	"github.com/SeanLF/ccpool/internal/store"
	"github.com/SeanLF/ccpool/internal/warn"
)

// Build metadata, injected at release time by GoReleaser via -ldflags -X.
// Defaults keep `go run`/`go install` builds honest ("dev") rather than blank.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// checkingUsageSkill is the bundled Claude Code skill `ccpool init` installs into ~/.claude/skills/.
// Embedded from the canonical source so the binary carries it (brew/go-install ship only the binary).
//
//go:embed skills/checking-usage/SKILL.md
var checkingUsageSkill []byte

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch is the command core, kept separate from main so it is testable and returns an exit code.
// Hot-path hooks (statusline, warn) fail OPEN via recover; on-demand commands fail LOUD (non-zero).
func dispatch(args []string) int {
	now := time.Now().Unix()

	// Bare invocation -> status (Ruby `when "status", nil`).
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	// `ccpool <cmd> --help` / `-h` prints that command's detailed help (hand-rolled, no cobra).
	if wantsHelp(args[1:]) {
		if h, ok := commandHelp[cmd]; ok {
			fmt.Println(h)
			return 0
		}
	}

	switch cmd {
	case "", "status":
		printLines(os.Stdout, status.Status(now))
		return 0
	case "check":
		lines, code := status.Report(now)
		printLinesToCode(lines, code)
		return code
	case "statusline":
		embed := hasFlag(args[1:], "--embed") || hasFlag(args[1:], "--compact")
		statusline.Command(now, embed)
		return 0
	case "__warm-calib": // internal: detached background $/1% warm-up (see statusline warm)
		statusline.WarmCalib(now)
		return 0
	case "warn":
		warn.Hook(now)
		return 0
	case "run":
		if err := run.Run(args[1:], now); err != nil {
			if errors.Is(err, run.ErrUsage) {
				return 2 // usage already printed to stderr
			}
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0 // unreachable on success: Run exec's the child
	case "review":
		analyzer.ReviewCommand(args[1:], now)
		return 0
	case "rhythm":
		printLines(os.Stdout, rhythm.Report(now))
		return 0
	case "init":
		initcmd.SetSkill(checkingUsageSkill)
		if err := initcmd.Run(args[1:], now); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// Seed ~/.ccpool/ccpool.json too (fill-missing-only via Merge, so re-running init is
		// idempotent and never clobbers a value the user already set in the file). This is a
		// best-effort bonus on top of init's real job (hook wiring, already done above): print
		// whatever configcmd.Init reports (including a corrupt-file warning) but don't let its
		// exit code override init's own -- a caller checking $? must be able to trust that init's
		// exit code reflects hook wiring, not an unrelated config-seed failure.
		lines, code := configcmd.Init(args[1:], now)
		printLinesToCode(lines, code)
		return 0
	case "config":
		return configCommand(args[1:], now)
	case "prune":
		return prune(args[1:], now)
	case "version", "--version", "-v":
		fmt.Printf("ccpool %s (%s, built %s)\n", version, commit, date)
		return 0
	case "help", "--help", "-h":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "ccpool: unknown command %q. Run `ccpool help` for usage.\n", cmd)
		return 2
	}
}

// configCommand dispatches `ccpool config <show|init>`, args already past "config".
func configCommand(args []string, now int64) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	var lines []string
	var code int
	switch sub {
	case "show":
		lines, code = configcmd.Show(now)
	case "init":
		lines, code = configcmd.Init(args[1:], now)
	default:
		fmt.Fprintf(os.Stderr, "ccpool: unknown config subcommand %q (want show or init). Run `ccpool help` for usage.\n", sub)
		return 2
	}

	printLinesToCode(lines, code)
	return code
}

// prune deletes stale snapshot rows, and with --history compacts the history rows too. One store open
// serves both DELETEs (fail-open: a non-OK store prunes nothing).
func prune(args []string, now int64) int {
	s, _ := store.Open()
	if s != nil {
		defer s.Close()
	}
	n := statusline.PruneCaches(s, now)
	msg := fmt.Sprintf("pruned %d stale snapshot(s)", n)
	if hasFlag(args, "--history") {
		keep := env.Float("CCPOOL_HISTORY_KEEP_DAYS", 30)
		removed, _ := initcmd.PruneHistory(s, now, keep)
		msg += fmt.Sprintf("; compacted %d old history row(s)", removed)
	}
	fmt.Printf("ccpool: %s\n", msg)
	return 0
}

func printLines(w io.Writer, lines []string) {
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// printLinesToCode routes lines to stdout on success (code==0) or stderr otherwise -- the print
// half of "print then return an exit code," shared by every call site below even though each
// chooses its OWN return value afterward (e.g. "init" always returns 0 regardless of code; see its
// call site's comment for why). Only the routing is shared; the exit-code decision stays local to
// each caller so it can't blur.
func printLinesToCode(lines []string, code int) {
	w := os.Stdout
	if code != 0 {
		w = os.Stderr
	}
	printLines(w, lines)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func usage(w io.Writer) {
	fmt.Fprint(w, `ccpool -- get the most out of your fixed Claude subscription pool.

Usage: ccpool <command> [args]        (no command -> status)

Commands:
  init [--apply]     wire ccpool into Claude Code (dry-run diff by default; --apply writes) and
                     seed ~/.ccpool/ccpool.json from your detected rhythm (same dry-run/--apply).
  config show        effective value of every setting + which layer supplied it (env/file/default).
  config init        (re-)seed ~/.ccpool/ccpool.json from your detected rhythm; dry-run by default,
                     --apply writes, --force re-detects and overwrites instead of fill-missing-only.
  status             % used, ~$ API-equiv left, and pace vs how far through the week you are.
  check              time + budget + a keep-going/stop VERDICT, for long or autonomous loops.
  rhythm             your circadian work rhythm + a suggested pace profile (read-only).
  run -- <cmd...>    run <cmd>, downshifting subagent model/effort when you're ahead of pace.
  review [days]      retrospective: did you use the right model for the work? (default 7d)
  statusline         render the Claude Code statusLine; bare in a terminal shows a preview.
  statusline --embed compact $-left + pace only, to embed in another statusline (e.g. a
                     ccstatusline custom-command widget). Keep your line, add ccpool's gauge.
  warn               Claude Code hook: warn mid-turn on pace / 5h / context (stdin = payload).
  prune [--history]  delete stale snapshot rows (add --history to also compact old history rows).

Run 'ccpool <command> --help' for details on any command.
  version            print version, commit, and build date.
  help               this message (also -h, --help).

Pace knobs:  CCPOOL_PACE_PROFILE=even|weekdays|workhours|custom · CCPOOL_WORK_DAYS=0-6 ·
             CCPOOL_WAKE_HOURS=9-17 · CCPOOL_CLOCK=24|12|auto
Full reference + env vars: see the README.
`)
}
