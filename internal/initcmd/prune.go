package initcmd

import (
	"bytes"
	"encoding/json"
	"os"
	"syscall"

	"github.com/SeanLF/ccpool/internal/paths"
)

// PruneHistory compacts the raw history log to the last keepDays days and returns the number of rows
// removed. keepDays <= 0 (or a missing file) keeps everything. The rewrite is flock-guarded so a
// concurrent statusline append can't interleave, and it writes-THEN-truncates (not truncate-first):
// a crash mid-op leaves the kept rows plus a stale tail readers skip, never an empty file.
//
// Best-effort like the Ruby prune_history (which rescues to 0): on any IO/lock error it returns
// (0, nil) rather than aborting the surrounding `prune` command over an opportunistic compaction.
func PruneHistory(now int64, keepDays float64) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	path := paths.History()
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, nil
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, nil
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	raw, err := readAll(f)
	if err != nil {
		return 0, nil
	}

	cutoff := float64(now) - keepDays*86400
	lines := splitLines(raw) // each element keeps its trailing '\n', matching Ruby String#lines
	var kept [][]byte
	for _, line := range lines {
		if rowTime(line) >= cutoff {
			kept = append(kept, line)
		}
	}
	removed := len(lines) - len(kept)
	if removed <= 0 {
		return 0, nil
	}

	if _, err := f.Seek(0, 0); err != nil {
		return 0, nil
	}
	out := bytes.Join(kept, nil)
	if _, err := f.Write(out); err != nil {
		return 0, nil
	}
	if err := f.Sync(); err != nil {
		return 0, nil
	}
	if err := f.Truncate(int64(len(out))); err != nil {
		return 0, nil
	}
	return removed, nil
}

// rowTime reads the "t" field as a float; a torn/invalid line yields 0 so it falls before any
// positive cutoff and is dropped (Ruby: `JSON.parse(l)["t"] rescue 0`).
func rowTime(line []byte) float64 {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return 0
	}
	t, ok := obj["t"].(json.Number)
	if !ok {
		return 0
	}
	f, err := t.Float64()
	if err != nil {
		return 0
	}
	return f
}

// splitLines splits on '\n' keeping the newline attached to each line, and drops a final empty
// element after a trailing newline — matching Ruby String#lines so kept.join reproduces the bytes.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i+1])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func readAll(f *os.File) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err := buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
