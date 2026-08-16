package zguard

import (
	"math"
	"testing"
)

// TestZDegenerateInputs is the 2.2 done-when #2 table: 0/0 → null, NaN/Inf
// anywhere → null, nothing panics, no Inf escapes.
func TestZDegenerateInputs(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	max := math.MaxFloat64
	cases := []struct {
		name                        string
		value, center, sigma, floor float64
		wantZ                       float64
		wantOK                      bool
	}{
		{"plain", 10, 4, 3, 1, 2, true},
		{"guard engages on sigma=0", 105, 100, 0, 2, 2.5, true},
		{"sigma above floor wins", 110, 100, 5, 2, 2, true},
		{"zero numerator, floored", 100, 100, 0, 2, 0, true},
		{"literal 0/0 is null", 100, 100, 0, 0, 0, false},
		{"nonzero over 0/0 is null", 105, 100, 0, 0, 0, false},
		{"NaN value", nan, 0, 1, 1, 0, false},
		{"NaN center", 0, nan, 1, 1, 0, false},
		{"NaN sigma", 0, 0, nan, 1, 0, false},
		{"NaN floor", 0, 0, 1, nan, 0, false},
		{"+Inf value", inf, 0, 1, 1, 0, false},
		{"-Inf center", 0, -inf, 1, 1, 0, false},
		{"+Inf sigma", 0, 0, inf, 1, 0, false},
		{"-Inf floor", 0, 0, 1, -inf, 0, false},
		{"negative sigma and floor", 5, 0, -1, -2, 0, false},
		{"subtraction overflow does not escape", max, -max, 0, 1, 0, false},
		{"division overflow does not escape", max, 0, 0, 0.5, 0, false},
	}
	for _, c := range cases {
		z, ok := Z(c.value, c.center, c.sigma, c.floor)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if math.IsNaN(z) || math.IsInf(z, 0) {
			t.Errorf("%s: z=%v escaped", c.name, z)
			continue
		}
		if ok && z != c.wantZ {
			t.Errorf("%s: z=%v, want %v", c.name, z, c.wantZ)
		}
		if !ok && z != 0 {
			t.Errorf("%s: null returned z=%v, want 0", c.name, z)
		}
	}
}
