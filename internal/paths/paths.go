// Package paths resolves the on-disk file locations ccpool reads and writes. ccpool-owned state
// lives under Home() (~/.ccpool by default) -- the SQLite DB plus the few remaining files (config,
// the warm-up throttle marker, the anomaly log); reads of Claude Code's own data (projects
// transcripts) stay under ~/.claude. Resolved fresh per call so the hermetic CCPOOL_* test env is
// honoured.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Home is ccpool's own state dir (CCPOOL_HOME || ~/.ccpool). Only ccpool-owned files live here;
// reads of Claude Code's own data (projects transcripts) stay under ~/.claude.
func Home() string { return resolve("CCPOOL_HOME", "~/.ccpool") }

// DB is the SQLite database path (CCPOOL_DB || $Home/ccpool.db).
func DB() string { return resolve("CCPOOL_DB", filepath.Join(Home(), "ccpool.db")) }


// History is the rate-limit history log (CCPOOL_HISTORY || $Home/rate-limit-history.jsonl).
func History() string {
	return resolve("CCPOOL_HISTORY", filepath.Join(Home(), "rate-limit-history.jsonl"))
}

// WarmMarker is the calibration warm-up throttle marker ($Home/calib.warming). It stays a FILE, not a
// kv row: it is a short-lived "don't re-fork the recompute" lock whose natural check is the file mtime
// (kv has no mtime), the same shape as warn's /tmp throttle markers. (The $/1% calibration itself and
// the ccusage blocks cache moved into the store's kv table, so their old cache-file paths are gone.)
func WarmMarker() string {
	return filepath.Join(Home(), "calib.warming")
}

// StatuslineLog is the capped anomaly log (CCPOOL_STATUSLINE_LOG || $Home/statusline.log).
func StatuslineLog() string {
	return resolve("CCPOOL_STATUSLINE_LOG", filepath.Join(Home(), "statusline.log"))
}

// Config is the ccpool config file (CCPOOL_CONFIG || $Home/ccpool.json). The one file a user's
// persisted choices live in; read fresh per process so the hermetic test env is honoured.
func Config() string {
	return resolve("CCPOOL_CONFIG", filepath.Join(Home(), "ccpool.json"))
}

// Projects is the base dir of Claude Code transcripts the analyzer scans
// (CCPOOL_PROJECTS || ~/.claude/projects); mirrors Ruby Analyzer::PROJECTS.
func Projects() string {
	return resolve("CCPOOL_PROJECTS", "~/.claude/projects")
}

// resolve reads an env override or expands the ~/-prefixed default, mirroring Ruby File.expand_path.
func resolve(env, def string) string {
	v := os.Getenv(env)
	if v == "" {
		v = def
	}
	return expand(v)
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			// filepath.Join cleans the leading "/" on the second element, and "~" -> "" -> home.
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
