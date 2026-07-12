// Package calib reads (and later computes) the $/1%-of-weekly self-calibration. The renderer only
// needs the cached number — computing it spawns ccusage and must never happen on a render path.
// The compute + warm-up land alongside the statusline command.
package calib

import (
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// calibKey is the kv row holding the $/1% calibration ({dpp,at}); the value blob shape is unchanged
// from the retired ccpool-calibration.json file, so cachedDPP/numField read it identically.
const calibKey = "calibration"

// ReadCache returns the parsed calibration cache from the kv table, or nil if it is missing (cold
// cache), the store is nil/unreadable, or the value is not a JSON object. Nil-or-object (mirroring the
// retired file reader's Hash-or-nil guard) is deliberate: a value that parsed to a non-object would
// crash every caller, and since the warm-up that would overwrite it crashes too, a bad cache
// self-perpetuates. Nil lets it self-heal by recompute. The store is threaded in (nil -> cold cache).
func ReadCache(s *store.Store) map[string]any {
	if s == nil {
		return nil
	}
	b, ok, _ := s.GetKV(calibKey)
	if !ok {
		return nil
	}
	// UseNumber (via rb.ParseObject) so the whole package reads the cache as json.Number, matching
	// compute.go's num()/cachedDPP() — decoding as float64 here would silently disable them.
	return rb.ParseObject(b)
}

// DPP returns the cached $/1% and whether it is present and numeric. Value 0 still reports true
// (Ruby treats 0 as truthy: only nil/false are falsy), so a genuine zero calibration shows a $.
func DPP(s *store.Store) (float64, bool) {
	c := ReadCache(s)
	if c == nil {
		return 0, false
	}
	return cachedDPP(c)
}
