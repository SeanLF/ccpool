package initcmd

import (
	"github.com/SeanLF/ccpool/internal/store"
)

// PruneHistory compacts history to the last keepDays days and returns the number of rows removed.
// keepDays <= 0 keeps everything. A single DELETE WHERE t < cutoff on the threaded store; WAL +
// busy_timeout handle a concurrent statusline append, so the old flock/write-then-truncate rewrite is
// gone. Best-effort (mirrors the Ruby prune_history's rescue-to-0): a nil store or a delete error
// returns (0, nil) rather than aborting the surrounding `prune` command over an opportunistic compaction.
func PruneHistory(s *store.Store, now int64, keepDays float64) (int, error) {
	if s == nil || keepDays <= 0 {
		return 0, nil
	}
	n, err := s.PruneHistory(now - int64(keepDays*86400))
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}
