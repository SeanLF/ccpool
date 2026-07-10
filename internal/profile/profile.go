// Package profile computes activity-weighted pace: the fraction of the weekly window's activity
// WEIGHT elapsed, not its raw seconds. The weekly pool is a rolling 7-day window whose start is
// whenever you first prompted, so pacing used% against the plain wall-clock fraction assumes you
// burn evenly 24/7. Weighting time by a wall-clock activity function corrects that for a Mon-Fri
// or 9-5 user. See the Ruby profile.rb for the full rationale.
//
// Two orthogonal knobs, both 24/7 by default (default == plain linear pace):
//
//	CCPOOL_WORK_DAYS   active days (wday 0=Sun..6=Sat; default all 7)
//	CCPOOL_WAKE_HOURS  waking window on them (e.g. 9-17; default 0-24 = no sleep)
//
// CCPOOL_PACE_PROFILE is sugar picking defaults for those two: even (default), weekdays, workhours,
// custom (graded day*hour weight vectors). An explicit knob always overrides the preset default.
package profile

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/rb"
)

// Config is a resolved profile: active days, waking window [H0, H1), and optional graded weight
// vectors (custom profile only: DayWeights[wday] * HourWeights[hour]).
type Config struct {
	Days        map[int]bool
	H0, H1      int
	DayWeights  []float64 // nil unless the custom profile is active
	HourWeights []float64 // nil unless the custom profile is active
}

// Load resolves the profile from the environment. Read fresh (not at package init) so a process
// that renders under different CCPOOL_* env per call honours each — and so it fails open: every
// parser below falls back to a safe default rather than erroring.
func Load() Config {
	name := strings.ToLower(getenv("CCPOOL_PACE_PROFILE", "even"))

	// A preset only sets DEFAULTS for the two knobs; an explicit env var overrides them.
	dayDefault := allDays()
	if name == "weekdays" || name == "workhours" {
		dayDefault = daySet(1, 5)
	}
	hourDefault := [2]int{0, 24}
	if name == "workhours" {
		hourDefault = [2]int{9, 17}
	}

	days := intSet(os.Getenv("CCPOOL_WORK_DAYS"), dayDefault)
	h0, h1 := hours(os.Getenv("CCPOOL_WAKE_HOURS"), hourDefault)

	var dayW, hourW []float64
	if name == "custom" {
		dayW = weights(os.Getenv("CCPOOL_PACE_WEIGHTS"), 7)
		hourW = weights(os.Getenv("CCPOOL_PACE_HOUR_WEIGHTS"), 24)
	}
	return Config{Days: days, H0: h0, H1: h1, DayWeights: dayW, HourWeights: hourW}
}

// floorValue re-reads the FLOOR knob (kept out of Config to mirror the Ruby module constant; it is
// only consulted inside weight()). Matches Ruby's `(ENV[...] || "0.15").to_f`: the "0.15" default
// applies only when the var is UNSET; a set-but-garbage value coerces to 0.0 like String#to_f.
func floorValue() float64 {
	v, ok := os.LookupEnv("CCPOOL_PACE_FLOOR")
	if !ok {
		v = "0.15"
	}
	return rb.ToF(v)
}

// Uniform reports whether the weight is 1.0 everywhere, so pace is just the plain time fraction and
// we can skip the integral. Detected from the CONFIG, so it fires however you reached 24/7.
func (c Config) Uniform() bool {
	if c.DayWeights != nil || c.HourWeights != nil {
		return false
	}
	if c.H0 > 0 || c.H1 < 24 {
		return false
	}
	for d := 0; d <= 6; d++ {
		if !c.Days[d] {
			return false
		}
	}
	return true
}

// Scheduled is the negation of Uniform (a non-24/7 rhythm is configured).
func (c Config) Scheduled() bool { return !c.Uniform() }

// weight for a local (wday, hour). >= 0. Off a work day OR outside waking hours -> FLOOR.
func (c Config) weight(wday, hour int) float64 {
	if c.DayWeights != nil {
		return c.DayWeights[wday] * c.HourWeights[hour]
	}
	if c.Days[wday] && hour >= c.H0 && hour < c.H1 {
		return 1.0
	}
	return floorValue()
}

func (c Config) weightAt(epoch int64) float64 {
	lt := time.Unix(epoch, 0).Local()
	return c.weight(int(lt.Weekday()), lt.Hour())
}

// Integral of weight dt over [a, b], stepping on hour boundaries (weight is constant within an
// hour). ~168 steps for a full window. Exported so runway can measure a burn run in working hours.
func (c Config) Integral(a, b int64) float64 {
	if b <= a {
		return 0.0
	}
	total := 0.0
	t := a
	for t < b {
		// align to the next hour boundary (or the end)
		step := 3600 - (t % 3600)
		if b-t < step {
			step = b - t
		}
		total += c.weightAt(t) * float64(step)
		t += step
	}
	return total
}

// ElapsedFraction is the fraction of the window's activity WEIGHT elapsed by now. Uniform (24/7) or
// a degenerate all-zero weight -> plain time fraction, so a broken profile can never divide by zero.
func (c Config) ElapsedFraction(windowStart, now, reset int64) float64 {
	span := reset - windowStart
	if span <= 0 {
		return 0.0
	}
	linear := clamp(float64(now-windowStart)/float64(span), 0.0, 1.0)
	if c.Uniform() {
		return linear
	}
	denom := c.Integral(windowStart, reset)
	if denom <= 0 {
		return linear
	}
	return clamp(c.Integral(windowStart, now)/denom, 0.0, 1.0)
}

// --- env parsers (all fail open to a default) ---

// intSet parses "1-5" or "1,2,4" (or a mix) into a day set; nil/blank or all-garbage -> default.
func intSet(str string, def map[int]bool) map[int]bool {
	if strings.TrimSpace(str) == "" {
		return def
	}
	out := map[int]bool{}
	for _, part := range strings.Split(str, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := parseRange(part); ok {
			for d := lo; d <= hi; d++ {
				out[d] = true
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			out[n] = true
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// parseRange parses "1-5" (optional surrounding space) into (1, 5, true).
func parseRange(part string) (int, int, bool) {
	dash := strings.IndexByte(part, '-')
	if dash <= 0 || dash == len(part)-1 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(part[:dash]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(part[dash+1:]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lo, hi, true
}

// hours parses "9-17" into [start, end]; missing dash / garbled -> default.
func hours(str string, def [2]int) (int, int) {
	parts := strings.SplitN(str, "-", 2)
	if len(parts) != 2 {
		return def[0], def[1]
	}
	// base-10 so "09" isn't read as octal (matches Ruby Integer(_, 10)).
	h0, err0 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 0)
	h1, err1 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 0)
	if err0 != nil || err1 != nil {
		return def[0], def[1]
	}
	return int(h0), int(h1)
}

// weights parses a comma list of floats into a fixed-size lookup (index -> weight), 1.0 for
// unspecified entries; garbled -> all 1.0.
func weights(str string, size int) []float64 {
	table := allOnes(size)
	if strings.TrimSpace(str) == "" {
		return table
	}
	for i, v := range strings.Split(str, ",") {
		if i >= size {
			break
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			// Ruby's Float() raises on a bad entry -> the whole parse rescues to all-1.0.
			return allOnes(size)
		}
		table[i] = f
	}
	return table
}

// --- small helpers ---

func allDays() map[int]bool { return daySet(0, 6) }

func daySet(lo, hi int) map[int]bool {
	m := map[int]bool{}
	for d := lo; d <= hi; d++ {
		m[d] = true
	}
	return m
}

func allOnes(size int) []float64 {
	t := make([]float64, size)
	for i := range t {
		t[i] = 1.0
	}
	return t
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
