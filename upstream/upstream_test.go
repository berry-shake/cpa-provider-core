package upstream

import (
	"encoding/json"
	"math"
	"testing"
)

// "NaN" and "Infinity" parse as floats but are no amount, and JSON cannot
// carry them to the page or CPA's quota panel.
func TestOptionalNumberRejectsNonFinite(t *testing.T) {
	for _, in := range []any{"NaN", "Infinity", "-Inf", math.Inf(1), math.NaN()} {
		if got, ok := OptionalNumber(in); ok || got != 0 {
			t.Errorf("OptionalNumber(%v) = %v, %v; want 0, false", in, got, ok)
		}
	}
	for in, want := range map[any]float64{"12.5": 12.5, 3.0: 3, json.Number("7"): 7} {
		if got, ok := OptionalNumber(in); !ok || got != want {
			t.Errorf("OptionalNumber(%v) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func TestOptionalNumber(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{" 0.75 ", 0.75, true},
		{json.Number("3"), 3, true},
		{nil, 0, false},
		{"n/a", 0, false},
		{true, 0, false},
	}
	for _, tc := range cases {
		if got, ok := OptionalNumber(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("OptionalNumber(%#v) = %v, %v", tc.in, got, ok)
		}
	}
	if got := NumberField(map[string]any{"available_usd": "4"}, "availableUsd", "available_usd"); got != 4 {
		t.Fatalf("NumberField fallback key = %v", got)
	}
	if got := NumberField(map[string]any{"x": "n/a"}, "x"); got != 0 {
		t.Fatalf("NumberField without a number = %v", got)
	}
}
