// Package paths resolves the on-disk file locations ccpool reads and writes. ccpool-owned state
// lives under Home() (~/.ccpool by default); reads of Claude Code's own data (projects transcripts,
// the legacy usage-cache snapshots) stay under ~/.claude. The env overrides and default names match
// the Ruby modules exactly (the on-disk contract; see docs/GO-MIGRATION.md). Resolved fresh per call
// so the hermetic CCPOOL_*/USAGE_* test env is honoured.
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

// SnapshotCache is the base per-session snapshot path (USAGE_CACHE || ~/.claude/usage-cache.json).
// Real snapshots are written to the "-<session>.json" sibling; see SnapshotFor / SnapshotGlob.
func SnapshotCache() string {
	return resolve("USAGE_CACHE", "~/.claude/usage-cache.json")
}

// SnapshotGlob matches every per-session snapshot: the base with ".json" -> "-*.json".
func SnapshotGlob() string {
	return strings.TrimSuffix(SnapshotCache(), ".json") + "-*.json"
}

// SnapshotFor is the snapshot path for a specific (sanitized) session id.
func SnapshotFor(sessionID string) string {
	return strings.TrimSuffix(SnapshotCache(), ".json") + "-" + sessionID + ".json"
}

// History is the rate-limit history log (CCPOOL_HISTORY || $Home/rate-limit-history.jsonl).
func History() string {
	return resolve("CCPOOL_HISTORY", filepath.Join(Home(), "rate-limit-history.jsonl"))
}

// CalibCache is the $/1% calibration cache (CCPOOL_CALIB_CACHE || $Home/ccpool-calibration.json).
func CalibCache() string {
	return resolve("CCPOOL_CALIB_CACHE", filepath.Join(Home(), "ccpool-calibration.json"))
}

// BlocksCache is the ccusage blocks cache (CCPOOL_BLOCKS_CACHE || $Home/ccpool-blocks-cache.json).
func BlocksCache() string {
	return resolve("CCPOOL_BLOCKS_CACHE", filepath.Join(Home(), "ccpool-blocks-cache.json"))
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
