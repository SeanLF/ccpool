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
