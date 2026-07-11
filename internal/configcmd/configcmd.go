// Package configcmd implements `ccpool config show` / `ccpool config init`, plus the Detect logic
// `ccpool init` calls to seed ~/.claude/ccpool.json on first setup. It sits above internal/config
// (imports rhythm, clock, env, paths -- none of which internal/config or internal/env may import
// back), so it is deliberately NOT imported by internal/env: only main wires it in. Detection lives
// here rather than in internal/config to keep that package a leaf.
//
// Detect is read-only and off the hot path (only `config init` and `init` call it); Show/Init are
// on-demand commands, so -- unlike the hot-path hooks -- they fail LOUD on a corrupt config file
// rather than silently falling back to defaults.
package configcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SeanLF/ccpool/internal/clock"
	"github.com/SeanLF/ccpool/internal/config"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rhythm"
)

// Detect builds the config a fresh install would seed: the pace profile read off the transcript
// rhythm, and the clock mode resolved once (so `auto` gets pinned to a concrete "12"/"24" rather
// than re-resolved, via a `defaults read`, on every future read). Everything else stays nil so
// Merge can fill a user's blanks without ever overwriting an existing choice.
func Detect(now int64) *config.Config {
	return detectFrom(rhythm.Histogram(now))
}

// detectFrom is Detect's pure core, split out so tests can drive it with a crafted histogram
// instead of scanning real transcripts (mirrors how rhythm_test.go drives rhythm.Detect directly).
func detectFrom(r float64, hours [24]int, wdays [7]int) *config.Config {
	profile, workDays, wakeHours := rhythm.Detect(r, hours, wdays)

	pace := &config.Pace{Profile: strPtr(profile)}
	// rhythm.Detect reports "workhours" with workDays=="" for an all-7-days rhythm (hour-only
	// restriction; see rhythm.Detect's doc comment). But profile.Load's "workhours" preset defaults
	// CCPOOL_WORK_DAYS to Mon-Fri when it's unset -- which would silently narrow the detected
	// all-week rhythm the moment this seeded config is read. Pin it explicitly so the persisted
	// config matches what was actually detected, not profile.Load's unrelated preset default.
	if profile == "workhours" && workDays == "" {
		workDays = "0-6"
	}
	if workDays != "" {
		pace.WorkDays = strPtr(workDays)
	}
	if wakeHours != "" {
		pace.WakeHours = strPtr(wakeHours)
	}

	clockMode := strconv.Itoa(clock.Mode())
	return &config.Config{
		Pace:  pace,
		Clock: &clockMode,
	}
}

func strPtr(s string) *string { return &s }

// setting is one row of `config show`: the env key env.Resolve reads, its documented default, and
// the dotted label mirroring the config file's JSON shape. This list IS the in-scope set -- it
// mirrors config.Config.Lookup's switch exactly (the keys the file can actually persist).
type setting struct {
	key, def, label string
}

var settings = []setting{
	{"CCPOOL_PACE_PROFILE", "even", "pace.profile"},
	{"CCPOOL_WORK_DAYS", "", "pace.work_days"},
	{"CCPOOL_WAKE_HOURS", "", "pace.wake_hours"},
	{"CCPOOL_PACE_FLOOR", "0.15", "pace.floor"},
	{"CCPOOL_PACE_WEIGHTS", "", "pace.weights"},
	{"CCPOOL_PACE_HOUR_WEIGHTS", "", "pace.hour_weights"},
	{"CCPOOL_DOWNSHIFT", "auto", "downshift.mode"},
	{"CCPOOL_DOWNSHIFT_MODEL", "haiku", "downshift.model"},
	{"CCPOOL_DOWNSHIFT_EFFORT", "low", "downshift.effort"},
	{"CCPOOL_CLOCK", "24", "clock"},
	{"CCPOOL_COLOR", "", "colour"},
	{"USAGE_TIER", "max_20x", "tier"},
	{"CCPOOL_HISTORY_KEEP_DAYS", "30", "history.keep_days"},
	{"CCPOOL_HISTORY_MIN_INTERVAL", "60", "history.min_interval"},
}

// Show is `ccpool config show`: the effective value of every in-scope setting plus which layer
// supplied it (env > file > default). On-demand, so a corrupt config file is surfaced LOUD (exit 2)
// rather than silently ignored the way the hot-path hooks treat it.
func Show(now int64) (lines []string, code int) {
	c, err := config.Load()
	if err != nil {
		return []string{fmt.Sprintf("ccpool: config file %s is corrupt: %v", paths.Config(), err)}, 2
	}

	lines = append(lines, fmt.Sprintf("%-26s %-12s (%s)", "setting", "value", "source"))
	enabledVal, enabledSrc := resolveEnabled(c)
	lines = append(lines, fmt.Sprintf("%-26s %-12s (%s)", "enabled", enabledVal, enabledSrc))
	for _, s := range settings {
		v, src := env.Resolve(s.key, s.def)
		lines = append(lines, fmt.Sprintf("%-26s %-12s (%s)", s.label, v, src))
	}
	return lines, 0
}

// resolveEnabled mirrors config.HooksEnabled's fail-open resolution (CCPOOL_ENABLED env > file
// enabled > true), but also reports which layer decided -- HooksEnabled itself only returns bool.
func resolveEnabled(c *config.Config) (value, source string) {
	if v, ok := os.LookupEnv("CCPOOL_ENABLED"); ok {
		on := v != "0" && !strings.EqualFold(v, "false")
		return strconv.FormatBool(on), "env"
	}
	if c.Enabled != nil {
		return strconv.FormatBool(*c.Enabled), "file"
	}
	return "true", "default"
}

// Init is `ccpool config init`: dry-run by default (prints the plan -- the JSON it would write --
// and writes nothing); --apply writes it. --force re-detects and overwrites outright (skipping
// Merge), for deliberately re-seeding after e.g. a rhythm change; the default fill-missing-only
// Merge never overwrites a value the user already set. On-demand, so a corrupt existing config is
// surfaced LOUD rather than papered over, even under --force (it still reads the file to report on
// it honestly, it just doesn't fold its values into the result).
func Init(args []string, now int64) (lines []string, code int) {
	apply := hasFlag(args, "--apply")
	force := hasFlag(args, "--force")

	detected := Detect(now)

	existing, err := config.Load()
	if err != nil {
		return []string{fmt.Sprintf("ccpool: config file %s is corrupt: %v", paths.Config(), err)}, 2
	}

	final := detected
	if !force {
		final = config.Merge(existing, detected)
	}

	b, err := json.MarshalIndent(final, "", "  ")
	if err != nil {
		return []string{fmt.Sprintf("ccpool: couldn't render the detected config: %v", err)}, 2
	}

	if !apply {
		return []string{
			"ccpool config init -- DRY RUN. This is what would be written to " + paths.Config() + ":",
			string(b),
			"",
			"Run `ccpool config init --apply` to write it.",
		}, 0
	}

	if err := config.Write(paths.Config(), final); err != nil {
		return []string{fmt.Sprintf("ccpool: couldn't write %s: %v", paths.Config(), err)}, 2
	}
	return []string{"ccpool config init: wrote " + paths.Config()}, 0
}

func hasFlag(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
