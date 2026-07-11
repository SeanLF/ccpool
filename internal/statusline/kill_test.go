package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKillSwitchNoOps(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "ccpool.json")
	os.WriteFile(cfg, []byte(`{"enabled":false}`), 0o644)
	t.Setenv("CCPOOL_CONFIG", cfg)

	payload := `{"rate_limits":{"seven_day":{"used_percentage":50,"resets_at":9999999999}}}`
	old := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString(payload)
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = old }()

	// Capture stdout
	oldOut := os.Stdout
	or, ow, _ := os.Pipe()
	os.Stdout = ow
	Command(1720000000, false)
	ow.Close()
	os.Stdout = oldOut
	buf := make([]byte, 4096)
	n, _ := or.Read(buf)
	if strings.TrimSpace(string(buf[:n])) != "" {
		t.Errorf("disabled statusline must print nothing, got %q", buf[:n])
	}
}
