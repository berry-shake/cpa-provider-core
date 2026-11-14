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
