// Package env reads ccpool's numeric configuration knobs from the environment. Every knob has a
// sensible default, so parsing FAILS OPEN: an unset OR unparseable value yields the default. This is
// deliberate on two counts. The hot-path hooks (statusline, warn) read these and must never abort,
// so erroring out is not an option. And falling back to the documented default is clearer than the
// old Ruby String#to_i/to_f coercion, under which a typo'd threshold silently became 0 (or a garbage
// value was treated worse than an unset one). Whitespace is trimmed; anything else non-numeric is a
// miss, not a partial parse.
//
// This is CONFIG only. Data that must honour the on-disk json.Number / Ruby contract (history rows,
// transcript fields, marker files) stays in internal/rb.
package env

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/SeanLF/ccpool/internal/config"
)

// Int returns key parsed as a base-10 int, or def if key is unset or does not parse cleanly.
func Int(key string, def int) int {
	if n, ok := parseInt(key); ok {
		return int(n)
	}
	return def
}

// Int64 returns key parsed as a base-10 int64, or def if key is unset or does not parse cleanly.
func Int64(key string, def int64) int64 {
	if n, ok := parseInt(key); ok {
		return n
	}
	return def
}

// Float returns key parsed as a finite float64, or def if key is unset, unparseable, or non-finite.
// Rejecting NaN/Inf matters: strconv.ParseFloat accepts "nan"/"inf" (Ruby's to_f did not), and a NaN
// knob would slip past downstream clamps and silently poison the pace math.
func Float(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		v, ok = fileValue(key)
	}
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return f
}

func parseInt(key string) (int64, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		v, ok = fileValue(key)
	}
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// fileValue returns the config-file value for key (fail-open: any load error yields no value, so env
// falls through to its default rather than breaking the hot path).
func fileValue(key string) (string, bool) {
	c, err := config.Load()
	if err != nil {
		return "", false
	}
	return c.Lookup(key)
}

// String returns key resolved os env > config file > def.
func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	if v, ok := fileValue(key); ok {
		return v
	}
	return def
}

// Resolve returns the effective value AND which layer supplied it, for `config show`.
func Resolve(key, def string) (string, string) {
	if v, ok := os.LookupEnv(key); ok {
		return v, "env"
	}
	if v, ok := fileValue(key); ok {
		return v, "file"
	}
	return def, "default"
}
