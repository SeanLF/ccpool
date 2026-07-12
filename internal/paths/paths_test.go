package paths

import (
	"path/filepath"
	"testing"
)

func TestConfigHonoursEnv(t *testing.T) {
	t.Setenv("CCPOOL_CONFIG", "/tmp/x/ccpool.json")
	if got := Config(); got != "/tmp/x/ccpool.json" {
		t.Errorf("Config() = %q, want the env override", got)
	}
}

func TestConfigDefault(t *testing.T) {
	t.Setenv("CCPOOL_CONFIG", "")
	got := Config()
	if filepath.Base(got) != "ccpool.json" {
		t.Errorf("Config() = %q, want a ccpool.json default", got)
	}
}

func TestHomeResolution(t *testing.T) {
	t.Setenv("CCPOOL_HOME", "/tmp/ccpool-home-x")
	t.Setenv("CCPOOL_DB", "")
	if got := Home(); got != "/tmp/ccpool-home-x" {
		t.Fatalf("Home() = %q, want /tmp/ccpool-home-x", got)
	}
	if got := DB(); got != "/tmp/ccpool-home-x/ccpool.db" {
		t.Fatalf("DB() = %q, want .../ccpool.db", got)
	}
}

func TestDBHonoursEnv(t *testing.T) {
	t.Setenv("CCPOOL_HOME", "/tmp/ccpool-home-x")
	t.Setenv("CCPOOL_DB", "/tmp/somewhere/other.db")
	if got := DB(); got != "/tmp/somewhere/other.db" {
		t.Fatalf("DB() = %q, want the env override", got)
	}
}

// ccpool-owned files default under Home(); each keeps its own first-precedence env override.
func TestCcpoolOwnedDefaultsUnderHome(t *testing.T) {
	t.Setenv("CCPOOL_HOME", "/tmp/ccpool-home-z")
	for _, tc := range []struct {
		name string
		env  string
		got  func() string
		base string
	}{
		{"History", "CCPOOL_HISTORY", History, "rate-limit-history.jsonl"},
		{"Config", "CCPOOL_CONFIG", Config, "ccpool.json"},
		{"CalibCache", "CCPOOL_CALIB_CACHE", CalibCache, "ccpool-calibration.json"},
		{"BlocksCache", "CCPOOL_BLOCKS_CACHE", BlocksCache, "ccpool-blocks-cache.json"},
		{"StatuslineLog", "CCPOOL_STATUSLINE_LOG", StatuslineLog, "statusline.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, "")
			want := "/tmp/ccpool-home-z/" + tc.base
			if got := tc.got(); got != want {
				t.Fatalf("%s() = %q, want %q", tc.name, got, want)
			}
		})
	}
}
