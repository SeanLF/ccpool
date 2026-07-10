// Package history appends the rate-limit history log (rate-limit-history.jsonl) that calibration
// and burn projection read. One JSON object per line; see docs/GO-MIGRATION.md "on-disk contract".
// The append is flock-guarded with per-session dedup and a min-interval throttle, exactly as the
// Ruby seed_history, so Go and Ruby statuslines can interleave writes without corrupting the log.
package history

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"syscall"

	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
)

// minIntervalDefault throttles 5h-only writes (wk flat) to curb log growth.
const minIntervalDefault = 60

// row is the history record. Field order matches the Ruby hash literal so json.Marshal emits the
// same key order (t, wk, wk_reset, ses, ses_reset, tier, cost, session) byte-for-byte. Numeric
// fields stay json.Number so the on-disk literal matches the payload's (int vs float preserved).
type row struct {
	T        int64        `json:"t"`
	Wk       json.Number  `json:"wk"`
	WkReset  *json.Number `json:"wk_reset"`
	Ses      *json.Number `json:"ses"`
	SesReset *json.Number `json:"ses_reset"`
	Tier     string       `json:"tier"`
	Cost     *json.Number `json:"cost"`
	Session  *string      `json:"session"`
}

// Seed appends a history row for this render. No-op (returns nil) unless the payload carries a
// numeric seven_day used_percentage, mirroring the Ruby guard. Best-effort: any error is returned
// for the caller to log, never panics.
func Seed(payload map[string]any, now int64) error {
	rl, ok := payload["rate_limits"].(map[string]any)
	if !ok {
		return nil
	}
	sd, ok := rl["seven_day"].(map[string]any)
	if !ok {
		return nil
	}
	wk, ok := sd["used_percentage"].(json.Number)
	if !ok {
		return nil
	}

	var sid *string
	if s, ok := payload["session_id"].(string); ok {
		sid = &s
	}

	r := row{
		T:        now,
		Wk:       wk,
		WkReset:  numPtr(sd["resets_at"]),
		Ses:      nil,
		SesReset: nil,
		Tier:     tier(),
		Cost:     cost(payload),
		Session:  sid,
	}
	if fh, ok := rl["five_hour"].(map[string]any); ok {
		r.Ses = numPtr(fh["used_percentage"])
		r.SesReset = numPtr(fh["resets_at"])
	}

	line, err := marshalRow(r)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(paths.History(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	last, err := lastSessionRow(f, sid)
	if err != nil {
		return err
	}
	if skip(last, r, now) {
		return nil
	}

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// skip reports whether the dedup/throttle rules drop this row. Matches the Ruby check: if the last
// row for this session has the same wk + wk_reset, drop it when nothing moved (same ses) or when
// only the 5h % moved but we wrote too recently (throttle).
func skip(last map[string]any, r row, now int64) bool {
	if last == nil {
		return false
	}
	if !numEqual(last["wk"], r.Wk) || !numEqualPtr(last["wk_reset"], r.WkReset) {
		return false
	}
	if numEqualPtr(last["ses"], r.Ses) {
		return true // nothing moved
	}
	return now-toI(last["t"]) < int64(minInterval()) // only the 5h % moved -> throttle
}

// lastSessionRow scans the tail (last 64 KB) for this session's most recent row. A nil sid matches
// any row (takes the last overall). Only the tail is read because it runs under the lock on every
// render; a missed older line just appends a harmless duplicate.
func lastSessionRow(f *os.File, sid *string) (map[string]any, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	off := size - 65536
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		e := rb.ParseObject(lines[i])
		if e == nil {
			continue
		}
		if sid == nil {
			return e, nil
		}
		if s, ok := e["session"].(string); ok && s == *sid {
			return e, nil
		}
	}
	return nil, nil
}

// --- helpers ---

// marshalRow serializes without Go's default HTML escaping (Ruby JSON.generate does not escape
// <, >, &), and without the trailing newline the encoder adds.
func marshalRow(r row) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func numPtr(v any) *json.Number {
	if n, ok := v.(json.Number); ok {
		return &n
	}
	return nil
}

func tier() string {
	if v, ok := os.LookupEnv("USAGE_TIER"); ok {
		return v
	}
	return "max_20x"
}

func cost(payload map[string]any) *json.Number {
	c, ok := payload["cost"].(map[string]any)
	if !ok {
		return nil
	}
	return numPtr(c["total_cost_usd"])
}

func minInterval() int {
	return env.Int("CCPOOL_HISTORY_MIN_INTERVAL", minIntervalDefault)
}

// numEqual compares a parsed value against a fresh json.Number the way Ruby == does (45 == 45.0).
func numEqual(a any, b json.Number) bool {
	an, ok := a.(json.Number)
	if !ok {
		return false
	}
	af, err1 := an.Float64()
	bf, err2 := b.Float64()
	return err1 == nil && err2 == nil && af == bf
}

// numEqualPtr compares a parsed value against a fresh *json.Number, treating both-nil as equal.
func numEqualPtr(a any, b *json.Number) bool {
	if b == nil {
		return a == nil
	}
	return numEqual(a, *b)
}

func toI(v any) int64 {
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
	}
	return 0
}
