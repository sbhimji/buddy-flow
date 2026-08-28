package optequity

import (
	"testing"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optprofile"
)

// TestConvFlagIsCellPredicate: the C glyph is the conv_z cell's colour
// predicate — lit at exactly ±SignificantZ, unlit at 1.9σ, ok=false on a
// gap — and the cell's Style is its colour (G1).
func TestConvFlagIsCellPredicate(t *testing.T) {
	const mod = 9*60 + 44
	m0944 := minuteAt(t, mod)
	r := optprofile.Row{MinuteOfDay: mod, Days: 20, MedianNetConv: 100, SigmaNetConv: 50, MedianNetNotional: 1000, SigmaNetNotional: 0}
	base := baselines("semis", mod, r, 10, 250)
	conv := 200.0
	incomplete := false
	read := func(sym string, minuteSec int64) (optbucket.Bucket, bool) {
		if minuteSec != m0944 || incomplete || sym != "A" {
			return optbucket.Bucket{}, false
		}
		return optbucket.Bucket{NetConviction: conv}, true
	}
	s := &Source{Base: base, Read: read}
	at := m0944 + 60
	check := func(name string, lit, pos, ok bool) {
		t.Helper()
		rc := row(t, s, "semis", []string{"A"}, at)
		at++
		l, p, o := s.ConvFlag(rc)
		if l != lit || p != pos || o != ok {
			t.Errorf("%s: ConvFlag = %v %v %v, want %v %v %v", name, l, p, o, lit, pos, ok)
		}
		_, styles := render(s, rc)
		want := ""
		if l && o {
			want = sgrRed
			if p {
				want = sgrGreen
			}
		}
		if styles[0] != want || styles[1] != "" {
			t.Errorf("%s: styles %q, want conv %q net unstyled", name, styles, want)
		}
	}
	check("+2.0σ", true, true, true)
	conv = 195 // +1.9σ
	check("+1.9σ", false, true, true)
	conv = 0 // −2.0σ
	check("−2.0σ", true, false, true)
	incomplete = true
	check("gap", false, false, false)
}
