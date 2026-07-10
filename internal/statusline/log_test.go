package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The anomaly log must stay capped AND carry structured, greppable records, even when a persistent
// schema drift makes a frequently-rendering statusline log on every draw. Also proves it honours the
// CCPOOL_STATUSLINE_LOG override (resolved per write, not cached).
func TestDiagCapsAndStructures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statusline.log")
	t.Setenv("CCPOOL_STATUSLINE_LOG", path)

	for i := 0; i < maxLogLines+50; i++ {
		diag.Warn("segment schema mismatch", "field", "seven_day", "got", "float64")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != maxLogLines {
		t.Fatalf("log has %d lines, want the cap of %d", len(lines), maxLogLines)
	}
	last := lines[len(lines)-1]
	for _, want := range []string{"level=WARN", "segment schema mismatch", "field=seven_day", "got=float64"} {
		if !strings.Contains(last, want) {
			t.Errorf("last record missing %q: %s", want, last)
		}
	}
}
