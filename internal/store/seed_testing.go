package store

import (
	"fmt"
	"strings"

	"github.com/SeanLF/ccpool/internal/rb"
)

// This file is the test/one-off seeder for the store: it turns the old rate-limit-history JSONL (one
// object per line) into DB rows. Conformance suites across packages seed a temp DB with it, and the
// one-off importer shares HistoryRowFromJSONL so both insert byte-identically. It is a shipped
// (non-_test) file only because cross-package tests can't import a _test file, mirroring internal/golden.

// HistoryRowFromJSONL parses one old-format history line (t, wk, wk_reset, ses, ses_reset, cost,
// session) into a HistoryRow. ok=false for a blank/unparseable line, one missing a numeric t or wk,
// or a synthetic sentinel (session=="bench" or wk_reset==9999999999) -- the sentinels the live data
// carried, skipped so they never re-enter the envelope.
func HistoryRowFromJSONL(line string) (HistoryRow, bool) {
	m := rb.ParseObject([]byte(line))
	if m == nil {
		return HistoryRow{}, false
	}
	t, okT := rb.Num(m["t"])
	wk, okWk := rb.Num(m["wk"])
	if !okT || !okWk {
		return HistoryRow{}, false
	}
	if s, _ := m["session"].(string); s == "bench" {
		return HistoryRow{}, false
	}
	if wr, ok := rb.Num(m["wk_reset"]); ok && int64(wr) == 9999999999 {
		return HistoryRow{}, false
	}
	return HistoryRow{
		T:        int64(t),
		Wk:       wk,
		WkReset:  jsonlIntPtr(m["wk_reset"]),
		Ses:      jsonlFloatPtr(m["ses"]),
		SesReset: jsonlIntPtr(m["ses_reset"]),
		Cost:     jsonlFloatPtr(m["cost"]),
		Session:  jsonlStrPtr(m["session"]),
	}, true
}

// SeedHistoryJSONL inserts old-format history JSONL into the DB at dbPath in file/arrival order, so
// the id rowid ascends with arrival and the running-max envelope's tie-break matches the live append
// path. Lines that don't parse (or are sentinels) are skipped, exactly as the importer does.
func SeedHistoryJSONL(dbPath, jsonl string) error {
	s, st := openAt(dbPath)
	if st != StateOK || s == nil {
		return fmt.Errorf("seed: open %s: state %v", dbPath, st)
	}
	defer s.Close()
	for _, line := range strings.Split(jsonl, "\n") {
		row, ok := HistoryRowFromJSONL(line)
		if !ok {
			continue
		}
		if err := s.AppendHistory(row); err != nil {
			return err
		}
	}
	return nil
}

func jsonlIntPtr(v any) *int64 {
	if f, ok := rb.Num(v); ok {
		i := int64(f)
		return &i
	}
	return nil
}

func jsonlFloatPtr(v any) *float64 {
	if f, ok := rb.Num(v); ok {
		return &f
	}
	return nil
}

func jsonlStrPtr(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}
