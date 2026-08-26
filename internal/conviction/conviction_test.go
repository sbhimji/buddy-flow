package conviction

import (
	"math"
	"strings"
	"testing"

	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/session"
)

func synthBaselines(days int, sigma, floor float64) *Baselines {
	p := &optprofile.Profile{Name: "ab", Rows: make([]optprofile.Row, session.MinutesPerSession)}
	fl := &optprofile.Floors{
		MinuteOfDay: make([]int, session.MinutesPerSession), NetConv: make([]float64, session.MinutesPerSession),
		NetNotional: make([]float64, session.MinutesPerSession), BasketNetConv: make([]float64, session.MinutesPerSession),
		BasketNetNotional: make([]float64, session.MinutesPerSession),
	}
	for i := range p.Rows {
		p.Rows[i] = optprofile.Row{MinuteOfDay: session.OpenMinute + i, Days: days,
			MedianNetConv: 1000, SigmaNetConv: sigma, MedianNetNotional: -500, SigmaNetNotional: sigma}
		fl.MinuteOfDay[i] = session.OpenMinute + i
		fl.BasketNetConv[i] = floor
		fl.BasketNetNotional[i] = floor
	}
	return &Baselines{Baskets: map[string]*optprofile.Profile{"ab": p}, Floors: fl}
}

func TestZKnownValues(t *testing.T) {
	b := synthBaselines(20, 200, 50)
	cz, nz, cok, nok := b.Z("ab", session.OpenMinute+5, 1400, -900)
	if !cok || !nok {
		t.Fatal("expected measurable z")
	}
	if math.Abs(cz-2.0) > 1e-12 || math.Abs(nz-(-2.0)) > 1e-12 {
		t.Errorf("z = %v / %v, want +2.0 / -2.0", cz, nz)
	}
	if FormatZ(cz, cok) != "+2.0" || FormatZ(nz, nok) != "-2.0" {
		t.Errorf("format = %q / %q", FormatZ(cz, cok), FormatZ(nz, nok))
	}
}

func TestFloorApplies(t *testing.T) {
	// σ 10 below floor 50 → σ_used = 50 → z = 100/50 = 2.
	b := synthBaselines(20, 10, 50)
	cz, _, ok, _ := b.Z("ab", session.OpenMinute, 1100, 0)
	if !ok || math.Abs(cz-2.0) > 1e-12 {
		t.Errorf("floored z = %v ok=%v, want +2.0", cz, ok)
	}
}

func TestNulls(t *testing.T) {
	cases := map[string]*Baselines{
		"below MinProfiledDays": synthBaselines(9, 200, 50),
		"sigma 0 floor 0":       synthBaselines(20, 0, 0),
	}
	for name, b := range cases {
		if _, _, cok, nok := b.Z("ab", session.OpenMinute, 1400, -900); cok || nok {
			t.Errorf("%s: expected null", name)
		}
	}
	b := synthBaselines(20, 200, 50)
	if _, _, ok, _ := b.Z("missing", session.OpenMinute, 1, 1); ok {
		t.Error("missing basket profile must be null")
	}
	if _, _, ok, _ := b.Z("ab", session.CloseMinute, 1, 1); ok {
		t.Error("outside the regular session must be null")
	}
	if FormatZ(0, false) != "·" {
		t.Error("null must render as ·")
	}
}

// Scope law: rendered strings are measurements — the footer must pass the
// same banned-substring scan flowshare's footer does.
func TestFooterLanguage(t *testing.T) {
	for _, banned := range []string{"buy", "sell", "Buy", "Sell"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q — scope law bans buy/sell language", banned)
		}
	}
}
