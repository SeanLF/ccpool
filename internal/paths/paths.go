// Package paths resolves the on-disk file locations ccpool reads and writes under ~/.claude.
// These are the interop contract with any Ruby still running during the migration (see
// docs/GO-MIGRATION.md "The on-disk contract"), so the env overrides and default names match the
// Ruby modules exactly. Resolved fresh per call so the hermetic CCPOOL_*/USAGE_* test env is honoured.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

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

// History is the rate-limit history log (CCPOOL_HISTORY || ~/.claude/rate-limit-history.jsonl).
func History() string {
	return resolve("CCPOOL_HISTORY", "~/.claude/rate-limit-history.jsonl")
}

// CalibCache is the $/1% calibration cache (CCPOOL_CALIB_CACHE || ~/.claude/ccpool-calibration.json).
func CalibCache() string {
	return resolve("CCPOOL_CALIB_CACHE", "~/.claude/ccpool-calibration.json")
}

// BlocksCache is the ccusage blocks cache (CCPOOL_BLOCKS_CACHE || ~/.claude/ccpool-blocks-cache.json).
func BlocksCache() string {
	return resolve("CCPOOL_BLOCKS_CACHE", "~/.claude/ccpool-blocks-cache.json")
}

// StatuslineLog is the capped anomaly log (CCPOOL_STATUSLINE_LOG || ~/.claude/statusline.log).
func StatuslineLog() string {
	return resolve("CCPOOL_STATUSLINE_LOG", "~/.claude/statusline.log")
}

// Config is the ccpool config file (CCPOOL_CONFIG || ~/.claude/ccpool.json). The one file a user's
// persisted choices live in; read fresh per process so the hermetic test env is honoured.
func Config() string {
	return resolve("CCPOOL_CONFIG", "~/.claude/ccpool.json")
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
