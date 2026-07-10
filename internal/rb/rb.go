// Package rb reproduces the handful of Ruby coercions the port must match byte-for-byte.
// Ruby's String#to_i / String#to_f parse the longest valid numeric prefix and yield 0 on no match
// (e.g. "120px".to_i == 120, "abc".to_f == 0.0, ".5".to_f == 0.5) — strconv rejects all of those.
// The renderer coerces env like COLUMNS and used_percentage this way, so the difference is visible
// in output. Keep this tiny and faithful, not a general parser.
package rb

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// RoundToInt mirrors Ruby Float#round to an integer: half away from zero (2.5 -> 3, -2.5 -> -3),
// which is exactly what math.Round does.
func RoundToInt(f float64) int { return int(math.Round(f)) }

// Round1 mirrors Ruby Float#round(1): round to one decimal, half away from zero (1.25 -> 1.3,
// where strconv's default half-to-even would give 1.2).
func Round1(f float64) float64 { return math.Round(f*10) / 10 }

// Fmt1 formats a value the way Ruby prints `x.round(1)`: one decimal place, e.g. 2.0 -> "2.0",
// 1.234 -> "1.2". Pre-rounds half-away-from-zero so it matches Ruby, not strconv's half-to-even.
func Fmt1(f float64) string { return strconv.FormatFloat(Round1(f), 'f', 1, 64) }

// ToI mirrors Ruby String#to_i (base 10): optional leading whitespace, optional sign, then digits
// (underscores allowed between digits) until the first non-digit. No digits -> 0.
func ToI(s string) int {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	i, sign := 0, 1
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		if s[i] == '-' {
			sign = -1
		}
		i++
	}
	var digits []byte
	lastDigit := false
	for i < len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
			lastDigit = true
			i++
		case c == '_' && lastDigit && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			// Ruby allows single underscores between digits ("1_000".to_i == 1000).
			lastDigit = false
			i++
		default:
			i = len(s) // stop
		}
	}
	if len(digits) == 0 {
		return 0
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil {
		return 0
	}
	return sign * n
}

// ToF mirrors Ruby String#to_f: parse the longest valid float prefix (optional sign, digits,
// fraction, exponent; underscores between digits); no valid number -> 0.0.
func ToF(s string) float64 {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	end := floatPrefixLen(s)
	if end == 0 {
		return 0.0
	}
	// Strip underscores (Ruby permits them between digits) before strconv.
	clean := strings.ReplaceAll(s[:end], "_", "")
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		// Ruby to_f yields ±Infinity on magnitude overflow (and 0.0 on underflow); ParseFloat
		// returns exactly those values alongside ErrRange, so keep them. Any other error -> 0.0.
		if errors.Is(err, strconv.ErrRange) {
			return f
		}
		return 0.0
	}
	return f
}

// floatPrefixLen returns the byte length of the longest leading substring of s that Ruby's to_f
// would consume as a float.
func floatPrefixLen(s string) int {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digitsBefore := 0
	for i < len(s) && (isDigit(s[i]) || (s[i] == '_' && digitsBefore > 0)) {
		if isDigit(s[i]) {
			digitsBefore++
		}
		i++
	}
	digitsAfter := 0
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && (isDigit(s[j]) || (s[j] == '_' && digitsAfter > 0)) {
			if isDigit(s[j]) {
				digitsAfter++
			}
			j++
		}
		if digitsAfter > 0 {
			i = j // only consume the dot if a fractional digit follows
		}
	}
	if digitsBefore == 0 && digitsAfter == 0 {
		return 0
	}
	// optional exponent
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		expDigits := 0
		for j < len(s) && isDigit(s[j]) {
			expDigits++
			j++
		}
		if expDigits > 0 {
			i = j
		}
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
