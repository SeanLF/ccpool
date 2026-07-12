package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// countHistory reads the row count via a raw connection (the store exposes no counter).
func countHistory(t testing.TB, dbPath string) int {
	t.Helper()
	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow("SELECT count(*) FROM history").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The production write topology is several short-lived ccpool processes (statusline renders, the warm
// child), each with its OWN store handle, racing to append. WAL + busy_timeout(5000) serialize the
// writers; every append must land -- zero drops. This is the in-process analogue of the design-doc
// contention spike, kept as a committed regression guard.
func TestContendedWritesNoDrops(t *testing.T) {
	for _, m := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("writers=%d", m), func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "ccpool.db")
			t.Setenv("CCPOOL_DB", dbPath)

			// Create the schema once so the racing writers only contend on INSERTs, not creation.
			s0, st := store.Open()
			if st != store.StateOK || s0 == nil {
				t.Fatalf("seed open = %v", st)
			}
			s0.Close()

			const perWriter = 25
			var wg sync.WaitGroup
			errs := make(chan error, m)
			for w := 0; w < m; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					s, st := store.Open() // each writer its own handle, like a separate process
					if st != store.StateOK || s == nil {
						errs <- fmt.Errorf("writer %d open = %v", w, st)
						return
					}
					defer s.Close()
					for i := 0; i < perWriter; i++ {
						if err := s.AppendHistory(store.HistoryRow{T: int64(w*10_000 + i), Wk: float64(i)}); err != nil {
							errs <- fmt.Errorf("writer %d append %d: %w", w, i, err)
							return
						}
					}
				}(w)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}

			if got, want := countHistory(t, dbPath), m*perWriter; got != want {
				t.Fatalf("history rows = %d, want %d (dropped writes under contention)", got, want)
			}
		})
	}
}

// BenchmarkContendedAppend measures the per-append cost with a background writer contending for the
// single WAL write-lock, so a regression in the busy_timeout/serialization path shows up.
func BenchmarkContendedAppend(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		b.Fatalf("open = %v", st)
	}
	defer s.Close()

	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		c, st := store.Open()
		if st != store.StateOK || c == nil {
			return
		}
		defer c.Close()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				_ = c.AppendHistory(store.HistoryRow{T: int64(1_000_000 + i), Wk: 1})
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.AppendHistory(store.HistoryRow{T: int64(i), Wk: float64(i % 100)}); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
	b.StopTimer()
	close(stop)
	bg.Wait()
}
