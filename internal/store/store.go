// Package store owns ccpool's embedded SQLite database: history, per-session snapshots, and small
// kv state, behind a thin fail-open facade. sqlc-generated types (internal/store/db) never leak past
// this package. Reads return a typed 3-way state (OK / Corrupt / Transient) so the hot path can fail
// open while on-demand commands distinguish a genuinely unreadable DB from a merely-busy one.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/store/db"
)

//go:embed schema.sql
var schemaSQL string

// ReadState classifies the outcome of a store read. StateOK with an empty result set is the valid
// warm-up / no-data-yet case; the finer "rows present but none parse" corruption distinction is drawn
// in Go over the returned rows, not here. StateCorrupt / StateTransient are DB-file-level states the
// file world never had.
type ReadState int

const (
	StateOK        ReadState = iota // query ran; rows (possibly empty) are valid
	StateCorrupt                    // SQLITE_CORRUPT / SQLITE_NOTADB -> genuinely unreadable
	StateTransient                  // SQLITE_BUSY after timeout, I/O error, or any non-corrupt failure -> unknown, retry
)

// Store wraps the sql.DB and the generated query set. Constructed only via Open.
type Store struct {
	q     *db.Queries
	sqlDB *sql.DB
}

// SQLite primary result codes are a permanent part of its public API (never renumbered), so we match
// them directly rather than import the heavy modernc lib package for two constants.
const (
	sqliteCorrupt = 11 // SQLITE_CORRUPT: the database disk image is malformed
	sqliteNotADB  = 26 // SQLITE_NOTADB: opened a file that is not a database
)

// Open resolves the DB path, creates the home dir and DB if absent, and self-heals a corrupt file by
// quarantining it aside and recreating an empty one (fresh install and post-corruption land on the
// same empty-DB path). On a healed DB the caller sees StateOK; if the corrupt file could NOT be set
// aside (e.g. a read-only mount or permission denied), Open returns StateCorrupt rather than blindly
// re-opening the still-corrupt file, which is the truthful "genuinely unhealable" signal.
func Open() (*Store, ReadState) {
	path := paths.DB()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, StateTransient
	}
	s, st := openAt(path)
	if st == StateCorrupt {
		if err := quarantine(path); err != nil {
			return nil, StateCorrupt // could not move the corrupt file aside; do not recreate over it
		}
		s, st = openAt(path) // recreate empty over the now-quarantined path
	}
	return s, st
}

// openAt opens (or creates) the DB at path with the WAL pragmas and applies the idempotent schema.
// The schema Exec IS the integrity probe: it reads the header + catalog, so a garbage/NOTADB or
// header-corrupt file fails here -> StateCorrupt (caller quarantines + recreates). We deliberately do
// NOT run PRAGMA quick_check: it is a full-page scan (measured ~11ms on a 4MB DB, growing with size)
// and Open is on the statusline/warn hot path, which cannot afford a per-open scan. Rare logical
// page corruption (valid header) instead surfaces lazily as SQLITE_CORRUPT from a real query, where
// classify degrades it to StateCorrupt (hot path empty, commands "unreadable") without panicking.
func openAt(path string) (*Store, ReadState) {
	// Build the DSN via url.URL, not fmt.Sprintf: modernc opens a "file:" DSN as a URI, so a raw path
	// containing %/# would be percent-decoded / treated as a fragment and silently resolve to a
	// DIFFERENT file. url.URL.Path encodes the path so it round-trips to the literal filename while
	// the pragmas (in RawQuery, used verbatim) still apply. Verified for %/# and clean paths alike.
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
	}).String()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, classify(err)
	}
	// One connection: ccpool processes are short-lived and single-goroutine on the write path, and a
	// single conn keeps the pragmas and the capture+append transaction on one session.
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec(schemaSQL); err != nil { // catches NOTADB/garbage/header corruption cheaply
		_ = sqlDB.Close()
		return nil, classify(err)
	}
	return &Store{q: db.New(sqlDB), sqlDB: sqlDB}, StateOK
}

// classify maps a driver error to a ReadState. Only a definitive corruption code earns the
// destructive self-heal; everything else (busy, I/O, an unknown or non-sqlite error) is transient so
// a momentarily-locked or hiccuping DB is never quarantined. Comma-ok throughout: never panics.
func classify(err error) ReadState {
	if err == nil {
		return StateOK
	}
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 0xff { // mask extended codes (e.g. CORRUPT_VTAB) down to the primary code
		case sqliteCorrupt, sqliteNotADB:
			return StateCorrupt
		}
	}
	return StateTransient
}

// quarantine moves a corrupt DB and its WAL/SHM sidecars aside so openAt can recreate an empty DB in
// their place. The sidecars are handled FIRST: a stale -wal left beside a freshly recreated DB could
// be replayed by SQLite and reintroduce the corrupt data, so if a sidecar cannot be renamed it is
// removed instead. The main-file rename gates the heal: its failure is returned so Open does not
// recreate over a corrupt file it never actually moved. A missing file is not an error. A unique
// suffix avoids clobbering an earlier quarantine.
func quarantine(path string) error {
	suffix := fmt.Sprintf(".corrupt-%d", time.Now().UnixNano())
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if err := os.Rename(side, side+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(side) // can't move it aside; delete so it can't replay into the fresh DB
		}
	}
	if err := os.Rename(path, path+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Close releases the underlying DB. Safe on a nil Store (Open can return nil on StateTransient).
func (s *Store) Close() error {
	if s == nil || s.sqlDB == nil {
		return nil
	}
	return s.sqlDB.Close()
}
