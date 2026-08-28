package flowshare

import (
	"testing"

	"buddy-flow/internal/devview"
)

// TestSignedFlagThreshold: lit at exactly ±threshold, not just inside; a
// gap is never lit; SGR follows the flag; CumZFlag is SignedFlag at
// SignificantZ over the cum_share_z key (the Z glyph, MO-6 G1).
func TestSignedFlagThreshold(t *testing.T) {
	cases := []struct {
		v                  float64
		ok                 bool
		lit, positive, okW bool
		sgr                string
	}{
		{2.0, true, true, true, true, sgrGreen},
		{-2.0, true, true, false, true, sgrRed},
		{1.99, true, false, true, true, ""},
		{-1.99, true, false, false, true, ""},
		{0, true, false, false, true, ""},
		{5, false, false, false, false, ""},
	}
	for _, c := range cases {
		lit, pos, ok := SignedFlag(c.v, c.ok, SignificantZ)
		if lit != c.lit || pos != c.positive || ok != c.okW {
			t.Errorf("SignedFlag(%v,%v) = %v %v %v, want %v %v %v", c.v, c.ok, lit, pos, ok, c.lit, c.positive, c.okW)
		}
		if got := SGR(lit, pos, ok); got != c.sgr {
			t.Errorf("SGR(%v) = %q, want %q", c.v, got, c.sgr)
		}
		z := c.v
		zok := c.ok
		f := CumZFlag(func(*devview.RowCtx) (float64, bool) { return z, zok })
		if l, p, o := f(nil); l != c.lit || p != c.positive || o != c.okW {
			t.Errorf("CumZFlag(%v,%v) = %v %v %v", c.v, c.ok, l, p, o)
		}
	}
}
