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
	blocksKey         = "blocks" // kv row for the ccusage blocks cache ({raw,at}); regenerable
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
// it was never computable (no ccusage / no history) -> caller shows % without $. The store is threaded
// in so the cache read/write and the wk-run history query share one open (nil store -> fail open).
func DollarPerPct(s *store.Store, now int64, force bool) (float64, bool) {
	cached := ReadCache(s)
	if !force && cached != nil {
		if at, ok := numField(cached, "at"); ok && now-int64(at) < int64(ttl()) {
			if dpp, ok := cachedDPP(cached); ok {
				return dpp, true
			}
		}
	}
	if dpp, ok := compute(s, now); ok {
		WriteCache(s, dpp, now)
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
func Stale(s *store.Store, now int64) bool {
	c := ReadCache(s)
	if c == nil {
		return true
	}
	if _, ok := cachedDPP(c); !ok {
		return true
	}
	at, _ := numField(c, "at")
	return now-int64(at) >= int64(ttl())
}

// WriteCache persists the calibration to the kv table. Best-effort: a nil store or write error is
// swallowed (fail open) -- the next read just recomputes.
func WriteCache(s *store.Store, dpp float64, at int64) {
	if s == nil {
		return
	}
	b, err := json.Marshal(struct {
		Dpp float64 `json:"dpp"`
		At  int64   `json:"at"`
	}{dpp, at})
	if err != nil {
		return
	}
	_ = s.PutKV(calibKey, b)
}

// compute pools cost over the recent monotonic wk windows. (0,false) when there is no history (fresh
// install; do not even spawn ccusage) or ccusage is unavailable.
func compute(s *store.Store, now int64) (float64, bool) {
	runs := wkRuns(s)
	if len(runs) == 0 {
		return 0, false
	}
	blocks, ok := ccusageBlocks(s, now)
	if !ok || len(blocks) == 0 {
		return 0, false
	}
	var wcs []windowCost
	for _, r := range runs {
		c := costBetween(blocks, r.t0, r.t1)
		if c <= 0 {
			continue
		}
		wcs = append(wcs, windowCost{cost: c, dw: r.dw})
	}
	dpp, ok := combineWindows(wcs)
	if !ok {
		return 0, false
	}
	return rb.RoundN(dpp, 4), true
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
// ccusage is unavailable. The store is threaded in for the blocks cache read/write.
func CostSince(s *store.Store, t0, t1 int64) (float64, bool) {
	blocks, ok := ccusageBlocks(s, time.Now().Unix())
	if !ok {
		return 0, false
	}
	return costBetween(blocks, t0, t1), true
}

var anthropicRe = regexp.MustCompile(`(?i)claude|anthropic`)

// ccusageBlocks returns the Anthropic-only 5h cost blocks. (nil,false) means ccusage is unavailable
// or its shape changed (which is logged loud, then $ stays disabled rather than silently zeroed).
func ccusageBlocks(s *store.Store, now int64) ([]block, bool) {
	blocks, ok, shapeOK := decodeBlocks(ccusageRaw(s, now))
	if !shapeOK {
		// ccusage ran but the shape changed -> fail LOUD (don't silently zero the $).
		os.Stderr.WriteString("[ccpool] ccusage (" + ccusageCmd() + ") returned an unexpected shape (no 'blocks' array); $ readout disabled until fixed\n")
	}
	return blocks, ok
}

// decodeBlocks is the quiet parse core shared by the compute path (which adds the loud shape warning)
// and the cache-only capture path (which must stay silent on the hot path). shapeOK=false flags the
// changed-shape case specifically, so only the caller that wants to warn does.
func decodeBlocks(out string) (blocks []block, ok, shapeOK bool) {
	if strings.TrimSpace(out) == "" {
		return nil, false, true
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, false, true
	}
	arr := blocksArray(doc)
	if arr == nil {
		return nil, false, false // shape changed
	}
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
	return blocks, true, true
}

// blocksCache decodes the kv blocks entry to its (raw, capturedAt). ("",0,false) on a nil store or a
// cold/garbage cache. Callers layer their own freshness policy on `at`: ccusageRaw enforces the TTL, the
// write-path reader ignores it. One place that knows the {raw,at} cache shape.
func blocksCache(s *store.Store) (raw string, at float64, ok bool) {
	if s == nil {
		return "", 0, false
	}
	b, present, _ := s.GetKV(blocksKey)
	if !present {
		return "", 0, false
	}
	var c map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&c) != nil {
		return "", 0, false
	}
	raw, _ = c["raw"].(string)
	at, _ = numField(c, "at")
	return raw, at, raw != ""
}

// CachedCumulativeCost sums the cumulative Anthropic $ from the CACHED ccusage blocks, to snapshot an
// aligned cost alongside each wk% sample. Cache-only: it NEVER spawns ccusage (this runs on the
// statusline write hot path; fail-open). (0,false) on a cold/absent/garbage cache.
func CachedCumulativeCost(s *store.Store) (float64, bool) {
	raw, _, ok := blocksCache(s) // cache-only: the write path never shells out
	if !ok {
		return 0, false
	}
	blocks, ok, _ := decodeBlocks(raw) // quiet: no shape warning on the write path
	if !ok {
		return 0, false
	}
	total := 0.0
	for _, b := range blocks {
		total += b.cost
	}
	return total, true
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
func ccusageRaw(s *store.Store, now int64) string {
	if raw, at, ok := blocksCache(s); ok && now-int64(at) < blocksTTL {
		return raw // fresh cache hit
	}
	cmd := exec.Command("sh", "-c", ccusageCmd()+" blocks --json 2>/dev/null") //nolint:gosec // user-owned command string, mirrors Ruby backtick
	outBytes, _ := cmd.Output()                                                // npx failure -> empty output -> caller fails open
	raw := string(outBytes)
	if s != nil && strings.TrimSpace(raw) != "" {
		if b, err := json.Marshal(map[string]any{"raw": raw, "at": now}); err == nil {
			_ = s.PutKV(blocksKey, b)
		}
	}
	return raw
}

type wkRun struct {
	t0, t1 int64
	dw     float64
}

// windowCost is one weekly window's Anthropic $ and the wk% it consumed, for pooling $/1%.
type windowCost struct {
	cost, dw float64
}

const (
	minRunDW      = 3    // a window must have consumed >= 3% to calibrate (integer-quantization floor)
	minRunDT      = 3600 // ...over >= 1h
	guardDropTol  = 2    // a drop this far below the running max is a real decrease, not +-1 read noise
	guardDropDur  = 3600 // ...that persisting this long means the fixed-window assumption is violated
	calibWindowsN = 3    // pool the last K windows (recency -> tracks Anthropic drift, not all history)
	disagreeRatio = 3.0  // recent windows disagreeing beyond this => a regime shift; trust the newest
)

func calibWindows() int { return env.Int("CCPOOL_CALIB_WINDOWS", calibWindowsN) }

// wkRuns reconstructs one calibration run per recent weekly window. The per-(boundary,minute) max-wk
// aggregation is a SQL GROUP BY (store.WkPoints, over the FULL history); this builds a monotonic,
// reset-clipped run per boundary and keeps only the last K (recency). Fail-soft: no store / no data.
func wkRuns(s *store.Store) []wkRun {
	if s == nil {
		return nil
	}
	pts, pst := s.WkPoints()
	if pst != store.StateOK {
		return nil
	}
	return runsFromPoints(pts)
}

// runsFromPoints groups the boundary-then-minute-ordered points into windows, builds one run each, and
// keeps the last K (recency). Split from wkRuns so the reconstruction is unit-testable without a store.
func runsFromPoints(pts []store.WkPoint) []wkRun {
	var runs []wkRun
	for i := 0; i < len(pts); {
		bnd := pts[i].Bnd
		j := i
		for j < len(pts) && pts[j].Bnd == bnd {
			j++
		}
		if r, ok := boundaryRun(pts[i:j], bnd); ok {
			runs = append(runs, r)
		}
		i = j
	}
	if k := calibWindows(); k > 0 && len(runs) > k {
		runs = runs[len(runs)-k:] // recency: the last K windows only
	}
	return runs
}

// boundaryRun builds one window's run: clip stale post-reset samples, reject a window that violates the
// non-decreasing assumption (guard), then take the monotonic dw = max - first counted ONCE (the old
// hard-fall split re-counted the reclimb after every spurious concurrent-read dip). (_,false) when the
// window is too short/small or guarded out.
func boundaryRun(pts []store.WkPoint, bnd int64) (wkRun, bool) {
	// Clip: a sample still carrying this wk_reset after the reset epoch is stale (a lagging session).
	var win []store.WkPoint
	for _, p := range pts {
		if p.Minute <= bnd {
			win = append(win, p)
		}
	}
	if len(win) < 2 {
		return wkRun{}, false
	}
	if hasSustainedDecrease(win) { // rolling-window / regime signal -> monotonic-max is unsafe here
		return wkRun{}, false
	}
	first, last := win[0], win[len(win)-1]
	maxWk := first.Wk
	for _, p := range win {
		maxWk = max(maxWk, p.Wk)
	}
	dw := maxWk - first.Wk
	if dw < minRunDW || last.Minute-first.Minute < minRunDT {
		return wkRun{}, false
	}
	return wkRun{t0: first.Minute, t1: last.Minute, dw: dw}, true
}

// hasSustainedDecrease reports whether used% falls meaningfully below its running max and stays there
// past guardDropDur -- the signature of a rolling window or a mid-window regime change, as opposed to
// the brief +-1 dips from concurrent reads that monotonic-max harmlessly absorbs.
func hasSustainedDecrease(pts []store.WkPoint) bool {
	rm := pts[0].Wk
	var dipStart int64
	dipping := false
	for _, p := range pts {
		if p.Wk < rm-guardDropTol {
			if !dipping {
				dipping, dipStart = true, p.Minute
			} else if p.Minute-dipStart >= guardDropDur {
				return true
			}
		} else {
			dipping = false
			rm = max(rm, p.Wk)
		}
	}
	return false
}

// combineWindows turns the recent windows' (cost,dw) into a single $/1%: pool dw-weighted when they
// agree, but on a sharp disagreement (a regime shift) trust the most-recent window rather than
// averaging an old regime with the new one. Windows are chronological (last = newest).
func combineWindows(wcs []windowCost) (float64, bool) {
	if len(wcs) == 0 {
		return 0, false
	}
	if len(wcs) >= 2 {
		lo, hi := wcs[0].cost/wcs[0].dw, wcs[0].cost/wcs[0].dw
		for _, w := range wcs {
			d := w.cost / w.dw
			lo, hi = min(lo, d), max(hi, d)
		}
		if lo > 0 && hi/lo > disagreeRatio {
			w := wcs[len(wcs)-1]
			return w.cost / w.dw, true
		}
	}
	totCost, totDW := 0.0, 0.0
	for _, w := range wcs {
		totCost += w.cost
		totDW += w.dw
	}
	if totDW == 0 {
		return 0, false
	}
	return totCost / totDW, true
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
