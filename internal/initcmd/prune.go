package initcmd

import (
	"github.com/SeanLF/ccpool/internal/store"
)

// PruneHistory compacts history to the last keepDays days and returns the number of rows removed.
// keepDays <= 0 keeps everything. A single DELETE WHERE t < cutoff in the store; WAL + busy_timeout
// handle a concurrent statusline append, so the old flock/write-then-truncate rewrite is gone.
//
// Best-effort like the Ruby prune_history (which rescues to 0): a non-OK store or a delete error
// returns (0, nil) rather than aborting the surrounding `prune` command over an opportunistic compaction.
func PruneHistory(now int64, keepDays float64) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	s, st := store.Open()
	if st != store.StateOK {
		return 0, nil
	}
	defer s.Close()
	cutoff := now - int64(keepDays*86400)
	n, err := s.PruneHistory(cutoff)
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}
