package calib

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
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
	if v, ok := os.LookupEnv("CCPOOL_CALIB_TTL"); ok {
		return rb.ToI(v)
	}
	return ttlDefault
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

// blocksArray extracts the blocks list: the doc itself if an array, else doc["blocks"], else the
// first array-valued field (keys sorted for determinism; real ccusage always uses "blocks").
func blocksArray(doc any) []any {
	if arr, ok := doc.([]any); ok {
		return arr
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	if arr, ok := m["blocks"].([]any); ok {
		return arr
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if arr, ok := m[k].([]any); ok {
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

// wkRuns reconstructs the monotonic wk% runs within each window from the history log: per
// (boundary, minute) it keeps the max wk, then records wk CHANGES only (skipping flat ses-padded
// minutes), splitting at a hard fall (a reset). Runs shorter than 3pts / 1h, or starting after
// their own boundary (+300s), are dropped.
func wkRuns() []wkRun {
	f, err := os.Open(paths.History())
	if err != nil {
		return nil
	}
	defer f.Close()

	// boundary -> minute -> max wk. Keyed by float64: wk_reset is always an integer epoch, so int
	// vs float representation never varies here (unlike Ruby's type-strict eql? hash keys).
	by := map[float64]map[int64]float64{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		r := rb.ParseObject(sc.Bytes())
		if r == nil {
			continue
		}
		wk, ok1 := rb.Num(r["wk"])
		bnd, ok2 := rb.Num(r["wk_reset"])
		tv, ok3 := rb.Num(r["t"])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		m := (int64(tv) / 60) * 60
		mins := by[bnd]
		if mins == nil {
			mins = map[int64]float64{}
			by[bnd] = mins
		}
		if cur, seen := mins[m]; !seen || wk > cur {
			mins[m] = wk
		}
	}

	var runs []wkRun
	bounds := make([]float64, 0, len(by))
	for b := range by {
		bounds = append(bounds, b)
	}
	sort.Float64s(bounds) // result set is order-independent; sorted for determinism

	for _, bnd := range bounds {
		sorted := sortedPoints(by[bnd])
		run := []wkPoint{sorted[0]}
		commit := func() {
			if r, ok := runFrom(run, bnd); ok {
				runs = append(runs, r)
			}
		}
		for i := 1; i < len(sorted); i++ {
			prev := sorted[i-1].wk
			cur := sorted[i]
			switch {
			case cur.wk < prev-1: // wk fell hard -> a reset boundary
				commit()
				run = []wkPoint{cur}
			case cur.wk != run[len(run)-1].wk: // a real wk CHANGE
				run = append(run, cur)
			}
		}
		commit()
	}
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

func sortedPoints(mins map[int64]float64) []wkPoint {
	pts := make([]wkPoint, 0, len(mins))
	for m, wk := range mins {
		pts = append(pts, wkPoint{m, wk})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].m < pts[j].m })
	return pts
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
