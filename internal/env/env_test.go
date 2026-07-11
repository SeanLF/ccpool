package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInt(t *testing.T) {
	const key = "CCPOOL_TEST_INT"
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{"unset -> default", false, "", 42},
		{"clean", true, "7", 7},
		{"surrounding whitespace", true, "  9\t", 9},
		{"negative", true, "-3", -3},
		{"garbage -> default (not 0)", true, "abc", 42},
		{"trailing garbage is NOT a partial parse", true, "12x", 42},
		{"float string -> default", true, "1.5", 42},
		{"empty -> default", true, "", 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv(key, c.val)
			} else {
				// t.Setenv has no "unset"; clear it directly (dedicated test-only key, no restore needed).
				os.Unsetenv(key)
			}
			if got := Int(key, 42); got != c.want {
				t.Errorf("Int(%q)=%d, want %d", c.val, got, c.want)
			}
		})
	}
}

func TestFloat(t *testing.T) {
	const key = "CCPOOL_TEST_FLOAT"
	cases := []struct {
		name string
		val  string
		want float64
	}{
		{"clean", "1.5", 1.5},
		{"integer-valued", "3", 3},
		{"whitespace", " 0.25 ", 0.25},
		{"garbage -> default (not 0.0)", "abc", 9.9},
		{"trailing garbage -> default", "1.5x", 9.9},
		{"empty -> default", "", 9.9},
		{"NaN -> default (would poison downstream math)", "nan", 9.9},
		{"Inf -> default", "inf", 9.9},
		{"-Inf -> default", "-inf", 9.9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.val)
			if got := Float(key, 9.9); got != c.want {
				t.Errorf("Float(%q)=%v, want %v", c.val, got, c.want)
			}
		})
	}
}

// Float and Int64 share parseInt with Int, so a single Int64 range case suffices.
func TestInt64(t *testing.T) {
	const key = "CCPOOL_TEST_INT64"
	t.Setenv(key, "9000000000") // > math.MaxInt32, must survive as int64
	if got := Int64(key, 0); got != 9_000_000_000 {
		t.Errorf("Int64=%d, want 9000000000", got)
	}
	t.Setenv(key, "nope")
	if got := Int64(key, 120); got != 120 {
		t.Errorf("Int64(garbage)=%d, want default 120", got)
	}
}

func TestStringAndFileLayer(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ccpool.json")
	if err := os.WriteFile(p, []byte(`{"pace":{"profile":"weekdays"},"history":{"keep_days":7}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", p)

	// file supplies the value when env is unset
	os.Unsetenv("CCPOOL_PACE_PROFILE")
	if got := String("CCPOOL_PACE_PROFILE", "even"); got != "weekdays" {
		t.Errorf("String from file = %q, want weekdays", got)
	}
	// numeric knobs get the file layer for free
	if got := Float("CCPOOL_HISTORY_KEEP_DAYS", 30); got != 7 {
		t.Errorf("Float from file = %v, want 7", got)
	}
	// env still WINS over the file
	t.Setenv("CCPOOL_PACE_PROFILE", "workhours")
	if got := String("CCPOOL_PACE_PROFILE", "even"); got != "workhours" {
		t.Errorf("env must win over file, got %q", got)
	}
	// default when neither set
	if got := String("CCPOOL_NOPE", "d"); got != "d" {
		t.Errorf("default = %q, want d", got)
	}
}

func TestResolveProvenance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ccpool.json")
	os.WriteFile(p, []byte(`{"clock":"12"}`), 0o644)
	t.Setenv("CCPOOL_CONFIG", p)

	os.Unsetenv("CCPOOL_CLOCK")
	if v, s := Resolve("CCPOOL_CLOCK", "24"); v != "12" || s != "file" {
		t.Errorf("Resolve = (%q,%q), want (12,file)", v, s)
	}
	t.Setenv("CCPOOL_CLOCK", "24")
	if v, s := Resolve("CCPOOL_CLOCK", "24"); v != "24" || s != "env" {
		t.Errorf("Resolve = (%q,%q), want (24,env)", v, s)
	}
	if v, s := Resolve("CCPOOL_MISSING", "d"); v != "d" || s != "default" {
		t.Errorf("Resolve = (%q,%q), want (d,default)", v, s)
	}
}
