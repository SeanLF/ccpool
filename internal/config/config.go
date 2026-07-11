// Package config reads a user's persisted ccpool choices from ~/.claude/ccpool.json. It is the
// middle layer of env > file > default: internal/env consults it after os env, before the builtin
// default. Fail-open: a missing OR corrupt file yields an empty, usable Config (hot-path callers
// ignore the error), while on-demand callers (config show/init) surface it. Detection seeds it once
// off the hot path; see Detect. Data that must honour the on-disk json.Number contract stays in rb;
// this is user config, decoded with plain stdlib json into pointer fields so absent != zero.
package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/SeanLF/ccpool/internal/paths"
)

type Config struct {
	Enabled   *bool      `json:"enabled,omitempty"`
	Pace      *Pace      `json:"pace,omitempty"`
	Downshift *Downshift `json:"downshift,omitempty"`
	Clock     *string    `json:"clock,omitempty"`
	Colour    *string    `json:"colour,omitempty"`
	Tier      *string    `json:"tier,omitempty"`
	History   *History   `json:"history,omitempty"`
}

type Pace struct {
	Profile     *string   `json:"profile,omitempty"`
	WorkDays    *string   `json:"work_days,omitempty"`
	WakeHours   *string   `json:"wake_hours,omitempty"`
	Floor       *float64  `json:"floor,omitempty"`
	Weights     []float64 `json:"weights,omitempty"`
	HourWeights []float64 `json:"hour_weights,omitempty"`
}

type Downshift struct {
	Mode   *string `json:"mode,omitempty"`
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
}

type History struct {
	KeepDays    *float64 `json:"keep_days,omitempty"`
	MinInterval *int     `json:"min_interval,omitempty"`
}

// Load reads and decodes the config file. The returned *Config is ALWAYS non-nil and usable, even on
// error (empty config), so hot-path callers can ignore err and fail open; on-demand callers report it.
func Load() (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(paths.Config())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // absent is normal: zero-config default
		}
		return c, err // unreadable: surface for on-demand, empty c for hot path
	}
	if err := json.Unmarshal(b, c); err != nil {
		return &Config{}, err // corrupt: empty usable config + the error
	}
	return c, nil
}

// HooksEnabled reports whether the hooks should run (default true; only an explicit enabled:false
// disables). Named HooksEnabled, not Enabled, because Config already has an Enabled field.
func (c *Config) HooksEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// HooksEnabled resolves the kill-switch fail-open: CCPOOL_ENABLED env (escape hatch) > file enabled >
// true. A missing OR corrupt config never disables (an inability to read config must not silence the
// tool). Errors are swallowed here by design (hot path).
func HooksEnabled() bool {
	if v, ok := os.LookupEnv("CCPOOL_ENABLED"); ok {
		return v != "0" && !strings.EqualFold(v, "false")
	}
	c, _ := Load()
	return c.HooksEnabled()
}

// Lookup returns the file's value for an env key in string form (as if the env var were set), present
// only when the field is set. This lets internal/env flow file values through the same parse+validate
// path as env values.
func (c *Config) Lookup(envKey string) (string, bool) {
	switch envKey {
	case "CCPOOL_PACE_PROFILE":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.Profile }))
	case "CCPOOL_WORK_DAYS":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.WorkDays }))
	case "CCPOOL_WAKE_HOURS":
		return strP(pick(c.Pace, func(p *Pace) *string { return p.WakeHours }))
	case "CCPOOL_PACE_FLOOR":
		return floatP(pick(c.Pace, func(p *Pace) *float64 { return p.Floor }))
	case "CCPOOL_PACE_WEIGHTS":
		if c.Pace != nil && c.Pace.Weights != nil {
			return csv(c.Pace.Weights), true
		}
	case "CCPOOL_PACE_HOUR_WEIGHTS":
		if c.Pace != nil && c.Pace.HourWeights != nil {
			return csv(c.Pace.HourWeights), true
		}
	case "CCPOOL_DOWNSHIFT":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Mode }))
	case "CCPOOL_DOWNSHIFT_MODEL":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Model }))
	case "CCPOOL_DOWNSHIFT_EFFORT":
		return strP(pick(c.Downshift, func(d *Downshift) *string { return d.Effort }))
	case "CCPOOL_CLOCK":
		return strP(c.Clock)
	case "CCPOOL_COLOR":
		return strP(c.Colour)
	case "USAGE_TIER":
		return strP(c.Tier)
	case "CCPOOL_HISTORY_KEEP_DAYS":
		return floatP(pick(c.History, func(h *History) *float64 { return h.KeepDays }))
	case "CCPOOL_HISTORY_MIN_INTERVAL":
		return intP(pick(c.History, func(h *History) *int { return h.MinInterval }))
	}
	return "", false
}

// --- small extractors ---

func pick[T, R any](group *T, f func(*T) *R) *R {
	if group == nil {
		return nil
	}
	return f(group)
}

func strP(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

func floatP(p *float64) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatFloat(*p, 'f', -1, 64), true
}

func intP(p *int) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.Itoa(*p), true
}

func csv(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}

// Merge returns base with each NIL field filled from add -- never overwriting a value base already
// has (fill-missing-only, so re-seeding can't clobber a user's edits).
func Merge(base, add *Config) *Config {
	if base == nil {
		base = &Config{}
	}
	if add == nil {
		return base
	}
	if base.Enabled == nil {
		base.Enabled = add.Enabled
	}
	if base.Clock == nil {
		base.Clock = add.Clock
	}
	if base.Colour == nil {
		base.Colour = add.Colour
	}
	if base.Tier == nil {
		base.Tier = add.Tier
	}
	base.Pace = mergePace(base.Pace, add.Pace)
	base.Downshift = mergeDownshift(base.Downshift, add.Downshift)
	base.History = mergeHistory(base.History, add.History)
	return base
}

func mergePace(b, a *Pace) *Pace {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Profile == nil {
		b.Profile = a.Profile
	}
	if b.WorkDays == nil {
		b.WorkDays = a.WorkDays
	}
	if b.WakeHours == nil {
		b.WakeHours = a.WakeHours
	}
	if b.Floor == nil {
		b.Floor = a.Floor
	}
	if b.Weights == nil {
		b.Weights = a.Weights
	}
	if b.HourWeights == nil {
		b.HourWeights = a.HourWeights
	}
	return b
}

func mergeDownshift(b, a *Downshift) *Downshift {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Mode == nil {
		b.Mode = a.Mode
	}
	if b.Model == nil {
		b.Model = a.Model
	}
	if b.Effort == nil {
		b.Effort = a.Effort
	}
	return b
}

func mergeHistory(b, a *History) *History {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.KeepDays == nil {
		b.KeepDays = a.KeepDays
	}
	if b.MinInterval == nil {
		b.MinInterval = a.MinInterval
	}
	return b
}

// Write marshals c (indented) to path atomically (temp + rename).
func Write(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
