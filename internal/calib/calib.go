// Package calib reads (and later computes) the $/1%-of-weekly self-calibration. The renderer only
// needs the cached number — computing it spawns ccusage and must never happen on a render path.
// The compute + warm-up land alongside the statusline command.
package calib

import (
	"encoding/json"
	"os"

	"github.com/SeanLF/ccpool/internal/paths"
)

// ReadCache returns the parsed calibration cache, or nil if it is missing, unreadable, corrupt, or
// not a JSON object. Mirroring the Ruby read_cache's Hash-or-nil guard is deliberate: a cache that
// parsed to a non-object would otherwise crash every caller, and since the warm-up that would
// overwrite it crashes too, a bad cache self-perpetuates. Hash-or-nil lets it self-heal.
func ReadCache() map[string]any {
	b, err := os.ReadFile(paths.CalibCache())
	if err != nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// DPP returns the cached $/1% and whether it is present and numeric. Value 0 still reports true
// (Ruby treats 0 as truthy: only nil/false are falsy), so a genuine zero calibration shows a $.
func DPP() (float64, bool) {
	c := ReadCache()
	if c == nil {
		return 0, false
	}
	f, ok := c["dpp"].(float64)
	return f, ok
}
