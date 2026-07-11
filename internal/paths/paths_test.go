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
