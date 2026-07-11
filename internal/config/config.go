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
