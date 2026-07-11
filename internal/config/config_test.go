package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ccpool.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", p)
}

func TestLoadLookup(t *testing.T) {
	write(t, `{"pace":{"profile":"weekdays","floor":0.2,"weights":[1,1,0.3]},"clock":"12","history":{"keep_days":7}}`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := map[string]string{
		"CCPOOL_PACE_PROFILE":      "weekdays",
		"CCPOOL_PACE_FLOOR":        "0.2",
		"CCPOOL_PACE_WEIGHTS":      "1,1,0.3", // array joined to CSV
		"CCPOOL_CLOCK":             "12",
		"CCPOOL_HISTORY_KEEP_DAYS": "7",
	}
	for k, want := range cases {
		if got, ok := c.Lookup(k); !ok || got != want {
			t.Errorf("Lookup(%q) = (%q,%v), want (%q,true)", k, got, ok, want)
		}
	}
	if _, ok := c.Lookup("CCPOOL_COLOR"); ok {
		t.Error("absent CCPOOL_COLOR should not be present")
	}
}

// TestLoadLookupAllKeys exercises every one of Lookup's 14 env-key cases by name, so a typo in any
// one case (e.g. a copy-pasted "CCPOOL_DOWNSHIFT_MODEL" for the "_EFFORT" case) fails a test instead
// of silently falling through to Lookup's default `return "", false` -- which would make the file
// value invisible to internal/env's file layer without anything erroring.
func TestLoadLookupAllKeys(t *testing.T) {
	write(t, `{
		"pace": {
			"profile": "custom",
			"work_days": "1-5",
			"wake_hours": "9-17",
			"floor": 0.25,
			"weights": [1, 0.5],
			"hour_weights": [1, 1, 0.3]
		},
		"downshift": {
			"mode": "always",
			"model": "haiku",
			"effort": "low"
		},
		"clock": "12",
		"colour": "auto",
		"tier": "max_20x",
		"history": {
			"keep_days": 14,
			"min_interval": 120
		}
	}`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := map[string]string{
		"CCPOOL_PACE_PROFILE":         "custom",
		"CCPOOL_WORK_DAYS":            "1-5",
		"CCPOOL_WAKE_HOURS":           "9-17",
		"CCPOOL_PACE_FLOOR":           "0.25",
		"CCPOOL_PACE_WEIGHTS":         "1,0.5",
		"CCPOOL_PACE_HOUR_WEIGHTS":    "1,1,0.3",
		"CCPOOL_DOWNSHIFT":            "always",
		"CCPOOL_DOWNSHIFT_MODEL":      "haiku",
		"CCPOOL_DOWNSHIFT_EFFORT":     "low",
		"CCPOOL_CLOCK":                "12",
		"CCPOOL_COLOR":                "auto",
		"USAGE_TIER":                  "max_20x",
		"CCPOOL_HISTORY_KEEP_DAYS":    "14",
		"CCPOOL_HISTORY_MIN_INTERVAL": "120",
	}
	if len(cases) != 14 {
		t.Fatalf("test covers %d keys, want all 14 of Lookup's cases", len(cases))
	}
	for k, want := range cases {
		if got, ok := c.Lookup(k); !ok || got != want {
			t.Errorf("Lookup(%q) = (%q,%v), want (%q,true)", k, got, ok, want)
		}
	}

	// An unknown key must fall through to Lookup's default (not, false) rather than panicking or
	// matching by accident.
	if _, ok := c.Lookup("CCPOOL_NOT_A_REAL_KEY"); ok {
		t.Error("unknown key should not be present")
	}
}

func TestEnabledDefaultsTrue(t *testing.T) {
	write(t, `{"pace":{"profile":"even"}}`) // no "enabled" key
	c, _ := Load()
	if !c.HooksEnabled() {
		t.Error("absent enabled must default to true")
	}
	write(t, `{"enabled":false}`)
	c, _ = Load()
	if c.HooksEnabled() {
		t.Error("enabled:false must disable")
	}
}

func TestLoadFailOpen(t *testing.T) {
	// missing file -> empty config, no error surfaced as fatal for the hot path
	t.Setenv("CCPOOL_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	c, err := Load()
	if err != nil {
		t.Errorf("missing file must not error, got %v", err)
	}
	if _, ok := c.Lookup("CCPOOL_CLOCK"); ok {
		t.Error("empty config must have nothing present")
	}
	if !c.HooksEnabled() {
		t.Error("empty config must be enabled")
	}
	// corrupt file -> empty usable config + a surfaced error (for on-demand loud reporting)
	write(t, `{ not valid json`)
	c, err = Load()
	if err == nil {
		t.Error("corrupt file must surface an error for on-demand callers")
	}
	if c == nil || c.HooksEnabled() != true {
		t.Error("corrupt file must still yield a usable, enabled config for the hot path")
	}
}
