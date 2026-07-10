// Package golden supports golden-file conformance tests. During the Ruby -> Go migration each
// command was verified byte-identical to a live Ruby oracle; those oracle outputs are captured here
// as committed goldens, so the regression suite is preserved without a Ruby dependency. Go is now
// the source of truth: after an intentional, reviewed output change, refresh the goldens with
// CCPOOL_UPDATE_GOLDEN=1 (which rewrites them to the current Go output).
package golden

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

var update = os.Getenv("CCPOOL_UPDATE_GOLDEN") == "1"

// Assert compares got against the committed golden at path (a repo-relative path is fine). With
// CCPOOL_UPDATE_GOLDEN=1 it (re)writes the golden to got instead of comparing.
func Assert(t *testing.T, path string, got []byte) {
	t.Helper()
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden write: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing (seed it with CCPOOL_UPDATE_GOLDEN=1): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden mismatch %s\n got:  %q\n want: %q", path, got, want)
	}
}
