package calib

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// $/1%-of-weekly self-calibration. Every dollar is delegated to ccusage (authoritative pricing,
// never hand-rolled); the history log supplies the wk% deltas ccusage can't see. Pooled, Δ-weighted
// over monotonic no-reset runs, Anthropic-only. Cached because ccusage is a slow subprocess.

const (
	ttlDefault        = 21600 // 6h
	blocksTTL         = 120   // don't re-spawn ccusage on every idle read
	ccusageCmdDefault = "npx -y ccusage@20"
)

func ttl() int {
	return env.Int("CCPOOL_CALIB_TTL", ttlDefault)
}

func ccusageCmd() string {
	if v := os.Getenv("CCPOOL_CCUSAGE_CMD"); v != "" {
		return v
	}
	return ccusageCmdDefault
}

// DollarPerPct returns the cached $/1% or recomputes it, refreshing the cache. (0,false) only when
// it was never computable (no ccusage / no history) -> caller shows % without $.
func DollarPerPct(now int64, force bool) (float64, bool) {
	cached := ReadCache()
	if !force && cached != nil {
		if at, ok := numField(cached, "at"); ok && now-int64(at) < int64(ttl()) {
			if dpp, ok := cachedDPP(cached); ok {
				return dpp, true
			}
		}
	}
	if dpp, ok := compute(now); ok {
		WriteCache(dpp, now)
		return dpp, true
	}
	// recompute failed: fall back to a stale cached value if present.
	if cached != nil {
		if f, ok := cachedDPP(cached); ok {
			return f, true
		}
	}
	return 0, false
}

// Stale reports whether DollarPerPct would recompute (spawn ccusage) right now. Callers warm the
// cache out-of-band so a render never blocks on the compute.
func Stale(now int64) bool {
	c := ReadCache()
	if c == nil {
		return true
	}
	if _, ok := cachedDPP(c); !ok {
		return true
	}
	at, _ := numField(c, "at")
	return now-int64(at) >= int64(ttl())
}

// WriteCache persists the calibration. Best-effort: a write failure is swallowed (fail open).
func WriteCache(dpp float64, at int64) {
	b, err := json.Marshal(struct {
		Dpp float64 `json:"dpp"`
		At  int64   `json:"at"`
	}{dpp, at})
	if err != nil {
		return
	}
	_ = os.WriteFile(paths.CalibCache(), b, 0o644)
}

// compute pools cost over monotonic wk runs. (0,false) when there is no history (fresh install; do
// not even spawn ccusage) or ccusage is unavailable.
func compute(now int64) (float64, bool) {
	runs := wkRuns()
	if len(runs) == 0 {
		return 0, false
	}
	blocks, ok := ccusageBlocks(now)
	if !ok || len(blocks) == 0 {
		return 0, false
	}
	totCost, totDW := 0.0, 0.0
	for _, r := range runs {
		c := costBetween(blocks, r.t0, r.t1)
		if c <= 0 {
			continue
		}
		totCost += c
		totDW += r.dw
	}
	if totDW == 0 {
		return 0, false
	}
	return rb.RoundN(totCost/totDW, 4), true
}

type block struct {
	s, e int64
	cost float64
}

// costBetween sums the Anthropic $ overlapping [t0, t1], prorated within each 5h block.
func costBetween(blocks []block, t0, t1 int64) float64 {
	total := 0.0
	for _, b := range blocks {
		ov := min(b.e, t1) - max(b.s, t0)
		if ov <= 0 {
			continue
		}
		total += b.cost * float64(ov) / float64(b.e-b.s)
	}
	return total
}

// CostSince is the Anthropic $ spent in [t0, t1] (for extrapolating a stale %). (0,false) if
// ccusage is unavailable.
func CostSince(t0, t1 int64) (float64, bool) {
	blocks, ok := ccusageBlocks(time.Now().Unix())
	if !ok {
		return 0, false
	}
	return costBetween(blocks, t0, t1), true
}

var anthropicRe = regexp.MustCompile(`(?i)claude|anthropic`)

// ccusageBlocks returns the Anthropic-only 5h cost blocks. (nil,false) means ccusage is unavailable
// or its shape changed (which is logged loud, then $ stays disabled rather than silently zeroed).
func ccusageBlocks(now int64) ([]block, bool) {
	out := ccusageRaw(now)
	if strings.TrimSpace(out) == "" {
		return nil, false
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	arr := blocksArray(doc)
	if arr == nil {
		// ccusage ran but the shape changed -> fail LOUD (don't silently zero the $).
		os.Stderr.WriteString("[ccpool] ccusage (" + ccusageCmd() + ") returned an unexpected shape (no 'blocks' array); $ readout disabled until fixed\n")
		return nil, false
	}
	var blocks []block
	for _, item := range arr {
		b, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if isGap, _ := b["isGap"].(bool); isGap {
			continue
		}
		if !allAnthropic(b["models"]) {
			continue
		}
		s, ok1 := parseTime(str(b["startTime"]))
		endStr := str(b["actualEndTime"])
		if endStr == "" {
			endStr = str(b["endTime"])
		}
		e, ok2 := parseTime(endStr)
		c, ok3 := rb.Num(b["costUSD"])
		if !ok1 || !ok2 || !ok3 || e <= s {
			continue
		}
		blocks = append(blocks, block{s: s, e: e, cost: c})
	}
	return blocks, true
}

// allAnthropic reports whether the block's models are all Claude/Anthropic. An empty/absent models
// list counts as Anthropic (mirrors Ruby: the reject only fires when models is non-empty).
func allAnthropic(v any) bool {
	models, ok := v.([]any)
	if !ok || len(models) == 0 {
		return true
	}
	for _, m := range models {
		if !anthropicRe.MatchString(str(m)) {
			return false
		}
	}
	return true
}

// blocksArray extracts the blocks list from ccusage's `blocks --json`: the doc itself if it is a bare
// array, else doc["blocks"]. Nothing else -- no "first array-valued field" guess. ccusage@20 always
// uses "blocks", so a rename should trip the caller's fail-LOUD probe (nil here) rather than silently
// matching some unrelated array and misreading it as cost blocks.
func blocksArray(doc any) []any {
	if arr, ok := doc.([]any); ok {
		return arr
	}
	if m, ok := doc.(map[string]any); ok {
		if arr, ok := m["blocks"].([]any); ok {
			return arr
		}
	}
	return nil
}

// ccusageRaw runs `<CCUSAGE> blocks --json` with a short file cache so tight staleness doesn't
// re-spawn npx on every idle read. Shells out via `sh -c` exactly like the Ruby backtick so
// CCPOOL_CCUSAGE_CMD's full command string (which the user owns, e.g. a wrapper with pipes) is
// honoured; the input is user config, not untrusted data.
func ccusageRaw(now int64) string {
	if b, err := os.ReadFile(paths.BlocksCache()); err == nil {
		var c map[string]any
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if d.Decode(&c) == nil {
			if at, ok := numField(c, "at"); ok && now-int64(at) < blocksTTL {
				if raw, ok := c["raw"].(string); ok && raw != "" {
					return raw
				}
			}
		}
	}
	cmd := exec.Command("sh", "-c", ccusageCmd()+" blocks --json 2>/dev/null") //nolint:gosec // user-owned command string, mirrors Ruby backtick
	outBytes, _ := cmd.Output()                                                // npx failure -> empty output -> caller fails open
	raw := string(outBytes)
	if strings.TrimSpace(raw) != "" {
		if b, err := json.Marshal(map[string]any{"raw": raw, "at": now}); err == nil {
			_ = os.WriteFile(paths.BlocksCache(), b, 0o644)
		}
	}
	return raw
}

type wkRun struct {
	t0, t1 int64
	dw     float64
}

type wkPoint struct {
	m  int64
	wk float64
}

// wkRuns reconstructs the monotonic wk% runs within each window from history. The per-(boundary,
// minute) max-wk aggregation is a SQL GROUP BY (store.WkPoints, over the FULL history); this
// reconstructs runs over those pre-aggregated points, recording wk CHANGES only (skipping flat
// ses-padded minutes) and splitting at a hard fall (a reset). Runs shorter than 3pts / 1h, or
// starting after their own boundary (+300s), are dropped. Fail-soft: no store / no data -> nil runs.
func wkRuns() []wkRun {
	s, st := store.Open()
	if st != store.StateOK {
		return nil
	}
	defer s.Close()
	pts, pst := s.WkPoints()
	if pst != store.StateOK {
		return nil
	}

	// WkPoints is ordered by boundary then minute, so points for one boundary are contiguous.
	var runs []wkRun
	for i := 0; i < len(pts); {
		bnd := pts[i].Bnd
		j := i
		for j < len(pts) && pts[j].Bnd == bnd {
			j++
		}
		runs = append(runs, splitRuns(pts[i:j], float64(bnd))...)
		i = j
	}
	return runs
}

// splitRuns reconstructs runs within one boundary's minute-sorted points: append on a real wk change,
// commit-and-restart on a hard fall, keeping only runs that pass runFrom.
func splitRuns(pts []store.WkPoint, bnd float64) []wkRun {
	var runs []wkRun
	run := []wkPoint{{m: pts[0].Minute, wk: pts[0].Wk}}
	commit := func() {
		if r, ok := runFrom(run, bnd); ok {
			runs = append(runs, r)
		}
	}
	for i := 1; i < len(pts); i++ {
		prev := pts[i-1].Wk
		cur := wkPoint{m: pts[i].Minute, wk: pts[i].Wk}
		switch {
		case cur.wk < prev-1: // wk fell hard -> a reset boundary
			commit()
			run = []wkPoint{cur}
		case cur.wk != run[len(run)-1].wk: // a real wk CHANGE
			run = append(run, cur)
		}
	}
	commit()
	return runs
}

// runFrom applies the keep filter (dw>=3, dt>=3600, start within boundary+300) to a candidate run.
func runFrom(run []wkPoint, bnd float64) (wkRun, bool) {
	first, last := run[0], run[len(run)-1]
	dw := last.wk - first.wk
	dt := last.m - first.m
	if dw < 3 || dt < 3600 || float64(first.m) > bnd+300 {
		return wkRun{}, false
	}
	return wkRun{t0: first.m, t1: last.m, dw: dw}, true
}

// --- small helpers ---

func numField(m map[string]any, key string) (float64, bool) { return rb.Num(m[key]) }

func cachedDPP(m map[string]any) (float64, bool) { return rb.Num(m["dpp"]) }

func str(v any) string {
	s, _ := v.(string)
	return s
}

// parseTime parses an ISO8601 timestamp to unix seconds (Ruby Time.parse().to_i).
func parseTime(s string) (int64, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}
