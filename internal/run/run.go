// Package run is the pace-aware downshift launcher (`ccpool run -- <cmd...>`): it decides, from the
// same reconciled pool % the statusline/warn use, whether to run a wrapped command with subagent
// model/effort throttled down (when you're ahead of pace or the 5h window is saturating), then
// replaces the process image with the command (like the Ruby `exec`).
//
// DownshiftEnv is the pure, byte-checkable decision (env overrides + a human message); Run wires it
// to the process: MODE=off is a pure passthrough, an explicit CLAUDE_CODE_SUBAGENT_MODEL is left
// untouched, MODE=advise prints the recommendation without applying it, MODE=auto applies it.
package run

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/report"
)

// ErrUsage signals "no command after --"; the caller maps it to exit 2 (Ruby `exit 2`) without
// re-printing (Run already emitted the usage line).
var ErrUsage = errors.New("ccpool run: no command")

// Downshift target env keys — the subagent model + effort a downshift pins.
const (
	subagentModelKey = "CLAUDE_CODE_SUBAGENT_MODEL"
	effortKey        = "CLAUDE_CODE_EFFORT_LEVEL"
)

// --- env-driven knobs (read fresh per call, mirroring the Ruby module constants; keeps the
// hermetic per-fixture test env honoured). MARGIN/COAST also live in report so pace verdicts agree.

func mode() string {
	if v, ok := os.LookupEnv("CCPOOL_DOWNSHIFT"); ok {
		return strings.ToLower(v) // auto (enforce) | advise (print, don't apply) | off
	}
	return "auto"
}

func margin() float64 { return envF("CCPOOL_PACE_MARGIN", 3) }

func coast() int64 { return envI("CCPOOL_COAST_SECS", 43200) } // <12h to reset -> use-it-or-lose-it

func fiveHCap() float64 { return envF("CCPOOL_5H_CAP", 85) } // 5h window this full -> downshift too

func dmodel() string { return envS("CCPOOL_DOWNSHIFT_MODEL", "haiku") }

func deffort() string { return envS("CCPOOL_DOWNSHIFT_EFFORT", "low") }

// DownshiftEnv is the pace-aware decision: the subagent env to inject (empty = no downshift) and a
// one-line human explanation. Fails OPEN — missing/stale data yields no downshift, never an error.
func DownshiftEnv(now int64) (map[string]string, string) {
	wk, ok := report.ResolveWeekly(now)
	if !ok || wk.Confidence == report.Stale {
		return map[string]string{}, "no usable usage data -> no downshift (fail open)"
	}

	p := pool.GetPace(wk.Used, wk.Reset, now)
	snaps := pool.LoadSnapshots()
	fh, fhOK := pool.FiveHour(snaps, now)
	age, ageOK := pool.DataAge(snaps, now) // Ruby: fh[:age] || 0
	if !ageOK {
		age = 0
	}
	fhHot := fhOK && age <= pool.Stale() && fh.Used >= fiveHCap()

	tag := ""
	if wk.Confidence == report.Estimated {
		tag = " est"
	}
	dm, de := dmodel(), deffort()
	down := map[string]string{subagentModelKey: dm, effortKey: de}
	m := margin()

	switch {
	// 5h saturation throttles within minutes -> downshift first, regardless of weekly/coast.
	case fhHot && p.Delta <= m:
		return down, fmt.Sprintf("5h at %d%% (%d%% wk%s) -> downshifting subagents to %s/%s",
			rb.RoundToInt(fh.Used), rb.RoundToInt(wk.Used), tag, dm, de)
	// Near weekly reset: unspent budget is lost anyway, let it burn.
	case p.ToReset < coast():
		return map[string]string{}, fmt.Sprintf("%d%% used%s, reset in %s -> no downshift (burn it)",
			rb.RoundToInt(wk.Used), tag, fmtx.Dur(p.ToReset))
	case p.Delta > m:
		return down, fmt.Sprintf("pace +%dpts (%d%% used%s) -> downshifting subagents to %s/%s",
			rb.RoundToInt(p.Delta), rb.RoundToInt(wk.Used), tag, dm, de)
	default:
		return map[string]string{}, fmt.Sprintf("%d%% used%s, %s -> no downshift",
			rb.RoundToInt(wk.Used), tag, report.PacePhrase(p))
	}
}

// Run executes `ccpool run -- <cmd...>`, replacing this process with the command (syscall.Exec, like
// Ruby exec). On-demand command -> fails LOUD: returns an error rather than swallowing. Only returns
// on an error (a successful exec never comes back).
func Run(args []string, now int64) error {
	cmd := splitCmd(args)
	if len(cmd) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ccpool run -- <command...>")
		return ErrUsage
	}

	if mode() == "off" { // pure passthrough -- never downshift
		return execPassthrough(cmd, nil)
	}
	// Respect an explicit user choice -- don't override a model the user set themselves.
	if _, set := os.LookupEnv(subagentModelKey); set {
		fmt.Fprintln(os.Stderr, "[ccpool] "+subagentModelKey+" already set -> leaving it")
		return execPassthrough(cmd, nil)
	}

	env, msg := DownshiftEnv(now)
	if mode() == "advise" { // print the recommendation, like the native tab -- but don't apply it
		suffix := ""
		if len(env) > 0 {
			suffix = " (advise mode -> not applied; CCPOOL_DOWNSHIFT=auto to enforce)"
		}
		fmt.Fprintf(os.Stderr, "[ccpool] %s%s\n", msg, suffix)
		return execPassthrough(cmd, nil)
	}
	fmt.Fprintf(os.Stderr, "[ccpool] %s\n", msg)
	return execPassthrough(cmd, env)
}

// splitCmd returns the command after the first "--", or the whole slice if there's no separator
// (Ruby: `sep = argv.index("--"); sep ? argv[(sep+1)..] : argv`).
func splitCmd(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return args
}

// execPassthrough replaces the process image with cmd, injecting extra env on top of the inherited
// environment (overriding any existing key, like Ruby's `exec(env, *cmd)`).
func execPassthrough(cmd []string, extra map[string]string) error {
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, cmd, mergedEnv(extra))
}

func mergedEnv(extra map[string]string) []string {
	env := os.Environ()
	if len(extra) == 0 {
		return env
	}
	idx := make(map[string]int, len(env))
	for i, kv := range env {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			idx[kv[:eq]] = i
		}
	}
	for k, v := range extra {
		if i, ok := idx[k]; ok {
			env[i] = k + "=" + v
		} else {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func envS(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envI(key string, def int64) int64 {
	if v, ok := os.LookupEnv(key); ok {
		return int64(rb.ToI(v))
	}
	return def
}

func envF(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		return rb.ToF(v)
	}
	return def
}
