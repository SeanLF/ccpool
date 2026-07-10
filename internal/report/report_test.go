package report

import "testing"

// Values are the authoritative output of Ruby CCPool.usd / CCPool.usdk (verified against ruby 4.0).

func TestUSD(t *testing.T) {
	cases := map[float64]string{0: "$0", 47: "$47", 1234: "$1,234", 1234567: "$1,234,567", 999: "$999"}
	for in, want := range cases {
		if got := USD(in); got != want {
			t.Errorf("USD(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUSDk(t *testing.T) {
	cases := map[float64]string{47: "$47", 1234: "$1.2k", 2000: "$2.0k", 999: "$999"}
	for in, want := range cases {
		if got := USDk(in); got != want {
			t.Errorf("USDk(%v) = %q, want %q", in, got, want)
		}
	}
}
