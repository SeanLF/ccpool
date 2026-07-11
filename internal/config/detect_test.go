package config

import "testing"

func TestMergeFillsMissingOnly(t *testing.T) {
	weekdays := "weekdays"
	even := "even"
	base := &Config{Pace: &Pace{Profile: &weekdays}} // user already set profile
	add := &Config{Pace: &Pace{Profile: &even}, Clock: ptr("24")}
	out := Merge(base, add)
	if *out.Pace.Profile != "weekdays" {
		t.Error("Merge must NOT overwrite an existing value")
	}
	if out.Clock == nil || *out.Clock != "24" {
		t.Error("Merge must fill a missing value from add")
	}
}

func ptr(s string) *string { return &s }
