package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store/db"
)

// HistoryRow is the caller-facing shape of one rate-limit history record. Optional columns are
// pointers (nil = SQL NULL), matching how the payload's nullable fields arrive.
type HistoryRow struct {
	T        int64
	Wk       float64
	WkReset  *int64
	Ses      *float64
	SesReset *int64
	Tier     string
	Cost     *float64
	Session  *string
}

// EnvRow is one point of the running-max envelope: the write time, the running-max used%, and the
// window's reset (NULL when there is no latest reset, i.e. the warm-up / !hasLatest path).
type EnvRow struct {
	T     int64
	Value float64
	Reset sql.NullInt64
}

// weeklyCutoffDays / envCutoff mirror burn.Read's 14-day trim: the envelope only considers rows
// written within the last 14 days.
const weeklyCutoffDays = 14

func envCutoff(now int64) int64 { return now - weeklyCutoffDays*86400 }

// AppendHistory writes one history row.
func (s *Store) AppendHistory(r HistoryRow) error {
	return s.q.AppendHistory(context.Background(), appendParams(r))
}

// CaptureAndAppend writes a snapshot UPSERT and a history INSERT in ONE transaction, so a reader
// never sees a snapshot without its paired history row. Fail-open callers treat any error as "skip".
func (s *Store) CaptureAndAppend(session string, capturedAt int64, payload []byte, r HistoryRow) error {
	ctx := context.Background()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	qtx := s.q.WithTx(tx)
	if err := qtx.PutSnapshot(ctx, db.PutSnapshotParams{Session: session, CapturedAt: capturedAt, Payload: string(payload)}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := qtx.AppendHistory(ctx, appendParams(r)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// LastSessionRow returns the most recent history row (for sid if non-nil, else overall) as a typed
// *HistoryRow, so history.skip() compares typed float64/*int64 values directly. No rows yet is StateOK
// with a nil row (a valid empty history, not an error). We deliberately do NOT round-trip the stored
// columns back through json.Number just to feed the old Ruby-shaped skip(): typed comparison is
// identical (SQLite REAL round-trips float64 exactly; the old numEqual also compared via Float64).
func (s *Store) LastSessionRow(sid *string) (*HistoryRow, ReadState) {
	var arg any // nil -> the query's `?1 IS NULL` matches every session
	if sid != nil {
		arg = *sid
	}
	row, err := s.q.LastSessionRow(context.Background(), arg)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, StateOK
		}
		return nil, classify(err)
	}
	return historyRowFromDB(row), StateOK
}

// EnvelopeWeekly / EnvelopeFiveHour run the two-pass running-max window query over the last 14 days
// and normalize the generated interface{} reset column to sql.NullInt64.
func (s *Store) EnvelopeWeekly(now int64) ([]EnvRow, ReadState) {
	rows, err := s.q.EnvelopeWeekly(context.Background(), envCutoff(now))
	if err != nil {
		return nil, classify(err)
	}
	out := make([]EnvRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, EnvRow{T: r.T, Value: r.Running, Reset: nullInt64FromAny(r.Reset)})
	}
	return out, StateOK
}

func (s *Store) EnvelopeFiveHour(now int64) ([]EnvRow, ReadState) {
	rows, err := s.q.EnvelopeFiveHour(context.Background(), envCutoff(now))
	if err != nil {
		return nil, classify(err)
	}
	out := make([]EnvRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, EnvRow{T: r.T, Value: r.Running, Reset: nullInt64FromAny(r.Reset)})
	}
	return out, StateOK
}

// PutSnapshot upserts one session's snapshot payload.
func (s *Store) PutSnapshot(session string, capturedAt int64, payload []byte) error {
	return s.q.PutSnapshot(context.Background(), db.PutSnapshotParams{Session: session, CapturedAt: capturedAt, Payload: string(payload)})
}

// Snapshots returns every session's payload parsed via rb.ParseObject (unparseable payloads dropped,
// exactly as the old file loader did). The payload already carries captured_at (spliced at capture);
// only if a payload lacks it do we splice the column value, as json.Number so rb.Num reads it.
func (s *Store) Snapshots() ([]map[string]any, ReadState) {
	rows, err := s.q.Snapshots(context.Background())
	if err != nil {
		return nil, classify(err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		m := rb.ParseObject([]byte(r.Payload))
		if m == nil {
			continue
		}
		if _, ok := m["captured_at"]; !ok {
			m["captured_at"] = intNum(r.CapturedAt)
		}
		out = append(out, m)
	}
	return out, StateOK
}

// DataAge is seconds since the freshest snapshot. ok=false (no data) when the table is empty: the
// query COALESCEs an empty max to 0, and captured_at is always a real epoch, never 0.
func (s *Store) DataAge(now int64) (age int64, ok bool, st ReadState) {
	newest, err := s.q.DataAge(context.Background())
	if err != nil {
		return 0, false, classify(err)
	}
	if newest == 0 {
		return 0, false, StateOK
	}
	return now - newest, true, StateOK
}

// GetKV returns the value for key. Missing key is StateOK with ok=false (a cold cache, not an error).
func (s *Store) GetKV(key string) ([]byte, bool, ReadState) {
	v, err := s.q.GetKV(context.Background(), key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, StateOK
		}
		return nil, false, classify(err)
	}
	return []byte(v), true, StateOK
}

// PutKV upserts a kv value. updated_at is bookkeeping only (the behavioural timestamp lives inside the
// JSON value blob, e.g. calibration's {dpp,at}), so wall-clock time is fine and non-load-bearing.
func (s *Store) PutKV(key string, value []byte) error {
	return s.q.PutKV(context.Background(), db.PutKVParams{Key: key, Value: string(value), UpdatedAt: time.Now().Unix()})
}

// PruneHistory / PruneSnapshots delete rows older than cutoff and report the count removed.
func (s *Store) PruneHistory(cutoff int64) (int64, error) {
	return s.q.PruneHistory(context.Background(), cutoff)
}

func (s *Store) PruneSnapshots(cutoff int64) (int64, error) {
	return s.q.PruneSnapshots(context.Background(), cutoff)
}

// --- conversion helpers ---

// nullInt64FromAny normalizes the envelope reset column (sqlc emits interface{}; the modernc driver
// scans a nullable INTEGER as int64 or nil) to sql.NullInt64. Comma-ok: anything unexpected -> NULL.
func nullInt64FromAny(v any) sql.NullInt64 {
	if i, ok := v.(int64); ok {
		return sql.NullInt64{Int64: i, Valid: true}
	}
	return sql.NullInt64{}
}

func appendParams(r HistoryRow) db.AppendHistoryParams {
	return db.AppendHistoryParams{
		T:        r.T,
		Wk:       r.Wk,
		WkReset:  nullInt64Ptr(r.WkReset),
		Ses:      nullFloat64Ptr(r.Ses),
		SesReset: nullInt64Ptr(r.SesReset),
		Tier:     r.Tier,
		Cost:     nullFloat64Ptr(r.Cost),
		Session:  nullStringPtr(r.Session),
	}
}

// historyRowFromDB converts a generated db row to the typed caller-facing *HistoryRow (NULL columns
// become nil pointers). skip() then compares these typed values directly, no json round-trip.
func historyRowFromDB(r db.History) *HistoryRow {
	return &HistoryRow{
		T:        r.T,
		Wk:       r.Wk,
		WkReset:  int64PtrFromNull(r.WkReset),
		Ses:      float64PtrFromNull(r.Ses),
		SesReset: int64PtrFromNull(r.SesReset),
		Tier:     r.Tier,
		Cost:     float64PtrFromNull(r.Cost),
		Session:  stringPtrFromNull(r.Session),
	}
}

// intNum formats an int64 as a json.Number for the snapshot captured_at splice, so rb.Num (which the
// snapshot-map readers use on the real payload) reads it just like the payload's own captured_at.
func intNum(i int64) json.Number { return json.Number(strconv.FormatInt(i, 10)) }

func int64PtrFromNull(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func float64PtrFromNull(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func stringPtrFromNull(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func nullInt64Ptr(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullFloat64Ptr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullStringPtr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}
