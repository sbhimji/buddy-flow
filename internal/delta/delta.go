// Package delta is the view half of dev-plan story 3.3 (mini-spec
// docs/mini-specs/metric-overload/MO-3-delta-view.md): the ask-side
// question with its honesty number attached, read from the signed
// columns MO-2 stores in the 1-second bucket.
//
//	delta  = (QuoteAsk.Dollars − QuoteBid.Dollars) / counted dollars   ∈ [−1, +1]
//	class% = (QuoteAsk.Dollars + QuoteBid.Dollars) / counted dollars   ∈ [0, 1]
//
// Both numerators are the STRONG classifiers only — prints at/beyond the
// touch or off the midpoint (MO-2 QuoteAsk/QuoteBid). Tick-rule-classified,
// late, unclassified, BLOCK and NON_PRICE_FORMING dollars sit in the
// denominator only (profile.Counted — the one slice policy), so uncertainty
// damps delta toward 0 and never biases it (D15 posture). delta_all, dev
// view only, widens the numerator to every rule (AskSide − BidSide) so the
// nightly ledger can judge whether the tick rule adds information.
//
// Raw ratios, no z (Δ1): a baseline needs signed bucket days that do not
// exist yet (MO-9). Everything computes at read time; renders are pure in
// (store state, second). Every rendered string is a statement of which
// side of the book the prints hit — never buy/sell.
package delta

import (
	"fmt"
	"strings"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// DeltaHighlight is the |delta| at or beyond which the cell renders bold —
// green when positive, red when negative (Δ5). A code constant tuned at
// the nightly ledger review, like SignificantZ; not config.
const DeltaHighlight = 0.25

// D9Window is the open-suppression window (DEV-PLAN Appendix B D9, default
// 30 s): prints in the first D9Window seconds after 09:30:00 ET are
// excluded from every delta/class% window — the book is wide or crossed
// and Lee-Ready degrades there (F2). Applied to the prints, so the 09:30
// minute's delta computes over [09:30:30, 09:31) only.
const D9Window int64 = 30

// RunWindow is the trailing span of the dev-only delta_5m column (Δ6): a
// ratio of sums over the window, not a sum of per-minute ratios.
const RunWindow int64 = 5 * 60

const gap = "·"

const (
	sgrGreen = "\x1b[1;32m"
	sgrRed   = "\x1b[1;31m"
)

// Store is the read path: bucket.Store satisfies it; tests hand-build one.
type Store interface {
	Window(st *ingest.SymbolState, fromSec, toSec int64) bucket.Bucket
}

// Minute is one window's measurements over a set of symbols. Every
// ok=false is a defined gap (Δ4): Signed not recorded or counted dollars 0
// → all three gap; strong-classified dollars 0 with counted > 0 → Delta
// gaps while Class renders 0 (a true measurement); DeltaAll gaps when no
// rule classified anything.
type Minute struct {
	Delta      float64 // (QuoteAsk − QuoteBid) / Counted
	DeltaOK    bool
	DeltaAll   float64 // (AskSide − BidSide) / Counted — dev view only
	DeltaAllOK bool
	Class      float64 // (QuoteAsk + QuoteBid) / Counted
	ClassOK    bool
}

// clampD9 applies D9 to a window: the part of [fromSec, toSec) inside the
// first D9Window seconds of the session open is dropped. Unresolvable
// dates leave the window as is.
func clampD9(fromSec, toSec int64) (int64, int64) {
	openSec, err := session.BucketStart(session.Date(fromSec*1e9), session.OpenMinute)
	if err != nil {
		return fromSec, toSec
	}
	if cut := openSec + D9Window; fromSec < cut && toSec > openSec {
		fromSec = cut
	}
	return fromSec, toSec
}

// Compute sums the given symbols' buckets over [fromSec, toSec) (after
// D9) and returns the window's measurements. Sums run in the given symbol
// order with explicit float64 conversions, so the same store state yields
// the same bytes on every render and architecture.
func Compute(store Store, states []*ingest.SymbolState, fromSec, toSec int64) Minute {
	fromSec, toSec = clampD9(fromSec, toSec)
	var counted, qAsk, qBid, ask, bid float64
	recorded := len(states) > 0
	for _, st := range states {
		b := store.Window(st, fromSec, toSec)
		_, d := profile.Counted(b)
		counted += float64(d)
		if b.Signed == nil {
			recorded = false
			continue
		}
		qAsk += float64(b.Signed.QuoteAsk.Dollars)
		qBid += float64(b.Signed.QuoteBid.Dollars)
		ask += float64(b.Signed.AskSide.Dollars)
		bid += float64(b.Signed.BidSide.Dollars)
	}
	var m Minute
	if !recorded || counted == 0 {
		return m
	}
	m.Class, m.ClassOK = (qAsk+qBid)/counted, true
	if qAsk+qBid > 0 {
		m.Delta, m.DeltaOK = (qAsk-qBid)/counted, true
	}
	if ask+bid > 0 {
		m.DeltaAll, m.DeltaAllOK = (ask-bid)/counted, true
	}
	return m
}

// FmtDelta renders a delta cell: "+0.31", or the gap.
func FmtDelta(v float64, ok bool) string {
	if !ok {
		return gap
	}
	return fmt.Sprintf("%+.2f", v)
}

// FmtClass renders a class% cell: "62%", or the gap.
func FmtClass(v float64, ok bool) string {
	if !ok {
		return gap
	}
	return fmt.Sprintf("%.0f%%", 100*v)
}

// Style is the Δ5 highlight: the SGR prefix for a delta at or beyond
// ±DeltaHighlight (green positive, red negative), "" otherwise or on a gap.
func Style(v float64, ok bool) string {
	switch {
	case !ok || (v < DeltaHighlight && v > -DeltaHighlight):
		return ""
	case v > 0:
		return sgrGreen
	default:
		return sgrRed
	}
}

// Calc memoizes the per-basket window reads within one render second
// (cells run on the single render goroutine; a fresh second invalidates
// everything — late prints amend completed buckets).
type Calc struct {
	store  Store
	memoAt int64
	memo   map[*devview.BasketRow]Minute
	memo5  map[*devview.BasketRow]Minute
}

// New binds the store.
func New(store Store) *Calc { return &Calc{store: store} }

func (c *Calc) reset(atSec int64) {
	if c.memoAt != atSec || c.memo == nil {
		c.memoAt, c.memo, c.memo5 = atSec, map[*devview.BasketRow]Minute{}, map[*devview.BasketRow]Minute{}
	}
}

// prev is the basket's last completed minute.
func (c *Calc) prev(rc *devview.RowCtx) Minute {
	c.reset(rc.AtSec)
	if m, ok := c.memo[rc.Basket]; ok {
		return m
	}
	cur := session.MinuteStart(rc.AtSec * 1e9)
	m := Compute(c.store, rc.Basket.States, cur-60, cur)
	c.memo[rc.Basket] = m
	return m
}

// trailing is the basket's last RunWindow seconds of completed minutes.
func (c *Calc) trailing(rc *devview.RowCtx) Minute {
	c.reset(rc.AtSec)
	if m, ok := c.memo5[rc.Basket]; ok {
		return m
	}
	cur := session.MinuteStart(rc.AtSec * 1e9)
	m := Compute(c.store, rc.Basket.States, cur-RunWindow, cur)
	c.memo5[rc.Basket] = m
	return m
}

// Columns is the trader pair — delta then class% (Δ3: wherever delta
// renders, class% renders beside it), both on the last completed minute.
func (c *Calc) Columns() []devview.Column {
	return []devview.Column{
		{Name: "delta", Width: 6,
			Cell:  func(rc *devview.RowCtx) string { m := c.prev(rc); return FmtDelta(m.Delta, m.DeltaOK) },
			Style: func(rc *devview.RowCtx) string { m := c.prev(rc); return Style(m.Delta, m.DeltaOK) }},
		{Name: "class%", Width: 6,
			Cell: func(rc *devview.RowCtx) string { m := c.prev(rc); return FmtClass(m.Class, m.ClassOK) }},
	}
}

// DevColumns is the dev-view set: the trader pair plus delta_all (every
// rule in the numerator) and delta_5m (Δ6, trailing RunWindow) so the
// ledger can compare them; neither reaches the trader table.
func (c *Calc) DevColumns() []devview.Column {
	cols := c.Columns()
	cols[0].Legend = "delta/class% = completed minute, strong (quote/midpoint) rules over counted dollars; delta_all adds the tick rule; delta_5m = trailing 5 completed minutes"
	return append(cols,
		devview.Column{Name: "delta_all", Width: 9,
			Cell: func(rc *devview.RowCtx) string { m := c.prev(rc); return FmtDelta(m.DeltaAll, m.DeltaAllOK) }},
		devview.Column{Name: "delta_5m", Width: 8,
			Cell: func(rc *devview.RowCtx) string { m := c.trailing(rc); return FmtDelta(m.Delta, m.DeltaOK) }},
	)
}

// Footer is the trader legend for the pair — statements of measurement
// only; goes through the scanner test.
const Footer = `delta             = last completed minute: dollars printed at/above the ask or above the midpoint, minus dollars printed at/below the bid or below the midpoint, over ALL counted dollars in this basket — +1.00 = every counted dollar hit the ask side, −1.00 = every one hit the bid side; prints the book could not place (late, no valid quote, tick-rule-only, block, non-price-forming) stay in the denominator and pull toward 0; bold green at/beyond +0.25, bold red at/beyond −0.25; raw, no 20-day typical yet; the 09:30 minute excludes its first 30 s (quotes are wide or crossed at the open)
class%            = the honesty number beside delta: % of those counted dollars the book could place at/beyond the touch or off the midpoint — read +0.30 on 40% and +0.30 on 85% as different statements
`

// ExtendTrader inserts the pair after concentration_day (README column
// order; appended when that column is absent) and adds the footer block.
func (c *Calc) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	pair := c.Columns()
	at := len(cols)
	for i, col := range cols {
		if col.Name == "concentration_day" {
			at = i + 1
			break
		}
	}
	out := make([]devview.Column, 0, len(cols)+len(pair))
	out = append(out, cols[:at]...)
	out = append(out, pair...)
	out = append(out, cols[at:]...)
	return out, insertFooter(footer, Footer)
}

// insertFooter places block before the gap-glyph line ("·  = ...") so the
// legend reads in column order, or appends it when that line is absent.
func insertFooter(footer, block string) string {
	if i := strings.Index(footer, "\n·  "); i >= 0 {
		return footer[:i+1] + block + footer[i+1:]
	}
	return footer + block
}
