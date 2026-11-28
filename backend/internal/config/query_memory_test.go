package config

import "testing"

func Test_ParseQueryMemoryFraction_DefaultsOffAndRange(t *testing.T) {
	for raw, want := range map[string]float64{"": DefaultQueryMemoryFraction, "0": 0, "0.6": 0.6, " 1 ": 1} {
		got, err := ParseQueryMemoryFraction(raw)
		if err != nil || got != want {
			t.Errorf("ParseQueryMemoryFraction(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-0.1", "1.5", "most"} {
		if _, err := ParseQueryMemoryFraction(raw); err == nil {
			t.Errorf("ParseQueryMemoryFraction(%q) accepted", raw)
		}
	}
}
