// Package optclassify is the single source of per-print options policy
// (mini-spec 7.3, the 0.3 print-inclusion analog): sweep detection, side
// sign, and the conviction weighting of SECTOR spec §3.1. The decoder
// transcribes; this package decides. Weights live in a PM-editable config
// (docs/foundations/options-weights-v1.json), and the hash of that file is
// stamped into every derived artifact so a stale-weights bucket or profile
// is a loud refusal, never a silently blended number.
package optclassify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"buddy-flow/internal/optingest"
)

// sweepCodes are the OPRA intermarket-sweep condition codes (K2; openapi
// + 7.0 findings). trade_code is a comma-joined list on the wire.
var sweepCodes = map[string]bool{
	"slan": true, "slai": true, "slcn": true, "slci": true, "slft": true,
}

// Weights is the parsed conviction-weight config.
type Weights struct {
	Version string  `json:"version"`
	Sweep   float64 `json:"sweep_weight"`
	Money   struct {
		ATMBandPct     float64 `json:"atm_band_pct"`
		ATMWeight      float64 `json:"atm_weight"`
		OTMNearMaxPct  float64 `json:"otm_near_max_pct"`
		OTMNearWeight  float64 `json:"otm_near_weight"`
		OTMFarWeight   float64 `json:"otm_far_weight"`
		ITMNearWeight  float64 `json:"itm_near_weight"`
		DeepITMMinPct  float64 `json:"deep_itm_min_pct"`
		DeepITMWeight  float64 `json:"deep_itm_weight"`
		NoUPriceWeight float64 `json:"no_underlying_price_weight"`
	} `json:"moneyness"`
	DTE struct {
		MaxDays int     `json:"max_days"`
		Weight  float64 `json:"weight"`
	} `json:"dte"`
	SizeOI struct {
		Frac         float64 `json:"frac"`
		Weight       float64 `json:"weight"`
		ZeroOIWeight float64 `json:"zero_oi_weight"`
	} `json:"size_oi"`
}

// LoadWeights reads and validates the config, returning the parsed weights
// and the artifact stamp: SHA-256 of the raw file bytes, first 12 hex.
// A malformed config refuses loudly (K6) — never a silent default.
func LoadWeights(path string) (Weights, string, error) {
	var w Weights
	raw, err := os.ReadFile(path)
	if err != nil {
		return w, "", fmt.Errorf("read weights config: %w", err)
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return w, "", fmt.Errorf("parse weights config %s: %w", path, err)
	}
	if err := w.validate(); err != nil {
		return w, "", fmt.Errorf("invalid weights config %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return w, hex.EncodeToString(sum[:])[:12], nil
}

func (w *Weights) validate() error {
	if w.Version == "" {
		return fmt.Errorf("version missing")
	}
	pos := map[string]float64{
		"sweep_weight":               w.Sweep,
		"atm_weight":                 w.Money.ATMWeight,
		"otm_near_weight":            w.Money.OTMNearWeight,
		"otm_far_weight":             w.Money.OTMFarWeight,
		"itm_near_weight":            w.Money.ITMNearWeight,
		"deep_itm_weight":            w.Money.DeepITMWeight,
		"no_underlying_price_weight": w.Money.NoUPriceWeight,
		"dte.weight":                 w.DTE.Weight,
		"size_oi.weight":             w.SizeOI.Weight,
		"size_oi.zero_oi_weight":     w.SizeOI.ZeroOIWeight,
		"size_oi.frac":               w.SizeOI.Frac,
	}
	for name, v := range pos {
		if v <= 0 {
			return fmt.Errorf("%s must be > 0 (got %v)", name, v)
		}
	}
	m := &w.Money
	if !(0 < m.ATMBandPct && m.ATMBandPct < m.OTMNearMaxPct && m.OTMNearMaxPct <= m.DeepITMMinPct) {
		return fmt.Errorf("moneyness bands must satisfy 0 < atm_band_pct < otm_near_max_pct <= deep_itm_min_pct (got %v, %v, %v)",
			m.ATMBandPct, m.OTMNearMaxPct, m.DeepITMMinPct)
	}
	if w.DTE.MaxDays <= 0 {
		return fmt.Errorf("dte.max_days must be > 0 (got %d)", w.DTE.MaxDays)
	}
	return nil
}

// Print is one classified print: the two numbers everything downstream
// sums, plus the flags the bucket store counts.
type Print struct {
	Sign             int8    // +1 ask_side, -1 bid_side, 0 mid/no_side/absent (K5)
	SignedNotional   float64 // premium × cpSign × Sign
	ConvictionWeight float64 // w_sweep × w_money × w_dte × w_size
	Weighted         float64 // SignedNotional × ConvictionWeight
	IsSweep          bool
	NoUPrice         bool // moneyness fell to the D17 neutral weight
	ZeroOI           bool // w_size fell to the D16 neutral weight
}

// Classifier applies one weights config over one session date. Not safe
// for concurrent use (the expiry cache is unguarded); the pipeline's
// single consumer goroutine is the intended caller.
type Classifier struct {
	W           Weights
	sessionDate time.Time // midnight ET of the session being classified
	dteCache    map[string]int
	loc         *time.Location
}

// NewClassifier binds weights to a session date ("2006-01-02", ET).
func NewClassifier(w Weights, sessionDate string) (*Classifier, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	d, err := time.ParseInLocation("2006-01-02", sessionDate, loc)
	if err != nil {
		return nil, fmt.Errorf("bad session date %q: %w", sessionDate, err)
	}
	return &Classifier{W: w, sessionDate: d, dteCache: make(map[string]int), loc: loc}, nil
}

// Classify computes the per-print numbers. ok=false only for an
// unparseable expiry (K3) — the caller counts it; every other input has a
// defined weight by D14–D17.
func (c *Classifier) Classify(t *optingest.OptionTrade) (Print, bool) {
	var p Print
	dte, ok := c.dte(t.Expiry)
	if !ok {
		return p, false
	}

	switch t.SideTag {
	case "ask_side":
		p.Sign = 1
	case "bid_side":
		p.Sign = -1
	}
	cpSign := -1.0
	if t.IsCall {
		cpSign = 1.0
	}
	// Premium verbatim — the vendor field already carries the contract
	// multiplier (100 / XSP 10 / NANOS 1); recomputing is the trap.
	p.SignedNotional = t.Premium * cpSign * float64(p.Sign)

	p.IsSweep = isSweep(t.TradeCode)
	wSweep := 1.0
	if p.IsSweep {
		wSweep = c.W.Sweep
	}
	wMoney := c.moneyWeight(t, &p)
	wDTE := 1.0
	if dte <= c.W.DTE.MaxDays {
		wDTE = c.W.DTE.Weight
	}
	wSize := c.sizeWeight(t, &p)

	p.ConvictionWeight = wSweep * wMoney * wDTE * wSize
	p.Weighted = p.SignedNotional * p.ConvictionWeight
	return p, true
}

// moneyWeight implements K1/D14/D17: f = K/S − 1 for calls, 1 − K/S for
// puts; f > 0 is OTM by f, f < 0 is ITM by |f|; ATM band wins first.
func (c *Classifier) moneyWeight(t *optingest.OptionTrade, p *Print) float64 {
	if !t.UPriceOK || t.UPrice <= 0 || t.Strike <= 0 {
		p.NoUPrice = true
		return c.W.Money.NoUPriceWeight
	}
	ratio := t.Strike / t.UPrice
	f := ratio - 1 // call: strike above spot = OTM
	if !t.IsCall {
		f = 1 - ratio // put: strike below spot = OTM
	}
	m := &c.W.Money
	abs := f
	if abs < 0 {
		abs = -abs
	}
	// bandEps absorbs division round-off at exact band edges (105/100−1 is
	// 0.05000…044 in float64): a strike sitting precisely on a boundary
	// belongs to the nearer-the-money band, deterministically.
	const bandEps = 1e-9
	switch {
	case abs <= m.ATMBandPct+bandEps:
		return m.ATMWeight
	case f > 0 && f <= m.OTMNearMaxPct+bandEps:
		return m.OTMNearWeight
	case f > 0:
		return m.OTMFarWeight
	case abs <= m.DeepITMMinPct+bandEps:
		return m.ITMNearWeight
	default:
		return m.DeepITMWeight
	}
}

// sizeWeight implements K4/D16: size > frac × prior-close OI (strict).
func (c *Classifier) sizeWeight(t *optingest.OptionTrade, p *Print) float64 {
	if t.OpenInterest <= 0 {
		p.ZeroOI = true
		return c.W.SizeOI.ZeroOIWeight
	}
	if float64(t.Size) > c.W.SizeOI.Frac*float64(t.OpenInterest) {
		return c.W.SizeOI.Weight
	}
	return 1.0
}

// dte returns calendar days from the session date to expiry (K3), cached.
func (c *Classifier) dte(expiry string) (int, bool) {
	if d, ok := c.dteCache[expiry]; ok {
		return d, d != dteInvalid
	}
	e, err := time.ParseInLocation("2006-01-02", expiry, c.loc)
	if err != nil {
		c.dteCache[expiry] = dteInvalid
		return 0, false
	}
	d := int(e.Sub(c.sessionDate).Hours() / 24)
	if d < 0 {
		d = 0 // late print after expiry: clamp, still classifiable
	}
	c.dteCache[expiry] = d
	return d, true
}

// dteInvalid marks a cached unparseable expiry (distinct from any real DTE).
const dteInvalid = -1

// isSweep implements K2 over the comma-joined trade_code list.
func isSweep(tradeCode string) bool {
	if tradeCode == "" {
		return false
	}
	if sweepCodes[tradeCode] {
		return true // common case: single code, no allocation
	}
	if !strings.Contains(tradeCode, ",") {
		return false
	}
	for _, part := range strings.Split(tradeCode, ",") {
		if sweepCodes[strings.TrimSpace(part)] {
			return true
		}
	}
	return false
}
