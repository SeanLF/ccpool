// Package pool reconciles the account-global rate_limits % across the per-session statusline
// snapshots — the one thing ccusage can't see. Each snapshot is frozen at a session's last API
// turn, so a window is reconciled across sessions: newest still-plausible reset, max used% on it.
package pool

import (
	"os"
	"path/filepath"

	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
)

// Week is the 7-day weekly window length in seconds.
const Week = 7 * 86400

// Stale is how old a snapshot may be before the raw % is distrusted (CCPOOL_STALE_SECS, default
// 120s). During active use the statusline re-renders seconds apart, so a trustworthy snapshot is
// seconds old; past this the caller extrapolates or tiers down.
func Stale() int64 {
	if v, ok := os.LookupEnv("CCPOOL_STALE_SECS"); ok {
		return int64(rb.ToI(v))
	}
	return 120
}

// Window is a reconciled rate-limit window: the used % and its reset epoch.
type Window struct {
	Used  float64
	Reset int64
}

// LoadSnapshots reads every per-session snapshot (falling back to the bare cache file if no
// per-session files exist), dropping anything unreadable or non-object. Order matches Ruby's sorted
// Dir.glob so callers that take the first match agree.
func LoadSnapshots() []map[string]any {
	files, _ := filepath.Glob(paths.SnapshotGlob())
	if len(files) == 0 {
		if _, err := os.Stat(paths.SnapshotCache()); err == nil {
			files = []string{paths.SnapshotCache()}
		}
	}
	var out []map[string]any
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if m := rb.ParseObject(b); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// GetWindow reconciles one account-global window across snapshots frozen at differing staleness:
// the newest still-plausible reset, and the MAX used% on it (monotonic within a window -> freshest).
// Guards the leak bug (#52326: used% carrying a resets_at epoch) and clamps garbage. ok=false when
// no plausible window exists.
func GetWindow(snaps []map[string]any, key string, now, maxAhead int64) (Window, bool) {
	var lives []Window
	for _, d := range snaps {
		w := digNum(d, "rate_limits", key)
		if w == nil {
			continue
		}
		u, ok := rb.Num(w["used_percentage"])
		if !ok || u < 0 || u >= 10000 {
			continue
		}
		r, ok := rb.Num(w["resets_at"])
		if !ok {
			continue
		}
		reset := int64(r)
		if reset <= now || reset > now+maxAhead {
			continue
		}
		lives = append(lives, Window{Used: min(u, 100), Reset: reset})
	}
	if len(lives) == 0 {
		return Window{}, false
	}
	// Newest plausible reset, then the max used% on it (monotonic within a window -> freshest).
	// used% is clamped to [0,100] above and maxReset is one of lives', so a 0.0 seed is safe.
	maxReset := lives[0].Reset
	for _, l := range lives {
		maxReset = max(maxReset, l.Reset)
	}
	maxUsed := 0.0
	for _, l := range lives {
		if l.Reset == maxReset {
			maxUsed = max(maxUsed, l.Used)
		}
	}
	return Window{Used: maxUsed, Reset: maxReset}, true
}

// DataAge is the seconds since the freshest snapshot across all sessions. ok=false if none.
func DataAge(snaps []map[string]any, now int64) (int64, bool) {
	var newest int64
	found := false
	for _, d := range snaps {
		if c, ok := rb.Num(d["captured_at"]); ok {
			if ci := int64(c); !found || ci > newest {
				newest, found = ci, true
			}
		}
	}
	if !found {
		return 0, false
	}
	return now - newest, true
}

// Pace measures used% against the fraction of the window's ACTIVITY WEIGHT elapsed (Profile). With
// the default even profile that is the plain elapsed fraction. delta>0 = ahead of pace.
type Pace struct {
	ElapsedPct float64
	Delta      float64
	ToReset    int64
}

func GetPace(used float64, reset, now int64) Pace {
	pct := profile.Load().ElapsedFraction(reset-Week, now, reset) * 100
	return Pace{ElapsedPct: pct, Delta: used - pct, ToReset: reset - now}
}

// Weekly / FiveHour reconcile the two windows with their age attached (age via the caller).
func Weekly(snaps []map[string]any, now int64) (Window, bool) {
	return GetWindow(snaps, "seven_day", now, Week+86400)
}

func FiveHour(snaps []map[string]any, now int64) (Window, bool) {
	return GetWindow(snaps, "five_hour", now, 6*3600)
}

// --- helpers ---

func digNum(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}
