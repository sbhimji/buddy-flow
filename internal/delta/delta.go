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
// The trader window is the trailing RunWindow of completed minutes — a
// ratio of sums over the window, never an average of minute ratios (Δ6,
// decided on the 08-24 grid: the 1-minute cell flipped sign 38% of
// minutes). The 1-minute cell stays on the dev view as delta_1m.
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
// the nightly ledger review, like SignificantZ; not config. 0.20 on the
// trailing window (0.25 was the 1-minute figure).
const DeltaHighlight = 0.20

// Thin-basket rule (Δ5): a basket row renders its delta always but is
// never highlighted when it has fewer than DeltaMinMembers members or
// fewer than DeltaMinDollars counted dollars in the window — two names or
// $3M cannot carry a bold cell.
const (
	DeltaMinMembers = 3
	DeltaMinDollars = 5e6
)

// Per-ticker highlight floor (Δ5): a ticker's delta is styled only on at
// least TickerDeltaMinDollars counted dollars AND TickerDeltaMinPrints
// strong-classified prints in the window.
const (
	TickerDeltaMinDollars = 5e5
	TickerDeltaMinPrints  = 20
)

// D9Window is the open-suppression window (DEV-PLAN Appendix B D9, default
// 30 s): prints in the first D9Window seconds after 09:30:00 ET are
// excluded from every delta/class% window — the book is wide or crossed
// and Lee-Ready degrades there (F2). Applied to the prints, so the 09:30
// minute's delta computes over [09:30:30, 09:31) only.
const D9Window int64 = 30

// RunWindow is the trader window (Δ6): the trailing five completed
// minutes, a ratio of sums.
const RunWindow int64 = 5 * 60

// MinWindow is the shortest D9-clamped trailing span that renders (Δ2):
// 150 s, so the first trader cell is 09:33:00 covering 09:30:30–09:33;
// through 09:35 the window is shorter than RunWindow.
const MinWindow int64 = 150

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
// rule classified anything. Counted, Prints and Span are the highlight
// gates' operands.
type Minute struct {
	Delta      float64 // (QuoteAsk − QuoteBid) / Counted
	DeltaOK    bool
	DeltaAll   float64 // (AskSide − BidSide) / Counted — dev view only
	DeltaAllOK bool
	Class      float64 // (QuoteAsk + QuoteBid) / Counted
	ClassOK    bool
	Counted    float64 // counted dollars in the window
	Prints     int64   // strong-classified prints in the window
	Span       int64   // seconds in the window after the D9 clamp
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
	var m Minute
	if toSec > fromSec {
		m.Span = toSec - fromSec
	}
	var counted, qAsk, qBid, ask, bid float64
	recorded := len(states) > 0
	for _, st := range states {
		b := store.Window(st, fromSec, toSec)
		if b.Signed == nil {
			recorded = false // per source, not per bucket: nothing to sum
			break
		}
		_, d := profile.Counted(b)
		counted += float64(d)
		qAsk += float64(b.Signed.QuoteAsk.Dollars)
		qBid += float64(b.Signed.QuoteBid.Dollars)
		ask += float64(b.Signed.AskSide.Dollars)
		bid += float64(b.Signed.BidSide.Dollars)
		m.Prints += b.Signed.QuoteAsk.Trades + b.Signed.QuoteBid.Trades
	}
	if !recorded || counted == 0 {
		return Minute{Span: m.Span}
	}
	m.Counted = counted
	m.Class, m.ClassOK = (qAsk+qBid)/counted, true
	if qAsk+qBid > 0 {
		m.Delta, m.DeltaOK = (qAsk-qBid)/counted, true
	}
	if ask+bid > 0 {
		m.DeltaAll, m.DeltaAllOK = (ask-bid)/counted, true
	}
	return m
}

// Trailing is the trader window as of a render second: the RunWindow of
// completed minutes ending at the current minute boundary, D9-clamped,
// gapped entirely while the clamped span is under MinWindow (Δ2).
func Trailing(store Store, states []*ingest.SymbolState, atSec int64) Minute {
	cur := session.MinuteStart(atSec * 1e9)
	m := Compute(store, states, cur-RunWindow, cur)
	if m.Span < MinWindow {
		return Minute{Span: m.Span}
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
// ±DeltaHighlight (green positive, red negative), "" otherwise or on a
// gap. The thin-basket and per-ticker gates wrap it.
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

// BasketStyle applies the thin-basket rule before Style.
func BasketStyle(m Minute, members int) string {
	if members < DeltaMinMembers || m.Counted < DeltaMinDollars {
		return ""
	}
	return Style(m.Delta, m.DeltaOK)
}

// TickerStyle applies the per-ticker floor before Style.
func TickerStyle(m Minute) string {
	if m.Counted < TickerDeltaMinDollars || m.Prints < TickerDeltaMinPrints {
		return ""
	}
	return Style(m.Delta, m.DeltaOK)
}

// Calc memoizes the per-basket window reads within one render second
// (cells run on the single render goroutine; a fresh second invalidates
// everything — late prints amend completed buckets).
type Calc struct {
	store  Store
	memoAt int64
	memo1  map[*devview.BasketRow]Minute // last completed minute
	memo5  map[*devview.BasketRow]Minute // trailing window
}

// New binds the store.
func New(store Store) *Calc { return &Calc{store: store} }

func (c *Calc) reset(atSec int64) {
	if c.memoAt != atSec || c.memo1 == nil {
		c.memoAt, c.memo1, c.memo5 = atSec, map[*devview.BasketRow]Minute{}, map[*devview.BasketRow]Minute{}
	}
}

// minute is the basket's last completed minute (dev view).
func (c *Calc) minute(rc *devview.RowCtx) Minute {
	c.reset(rc.AtSec)
	if m, ok := c.memo1[rc.Basket]; ok {
		return m
	}
	cur := session.MinuteStart(rc.AtSec * 1e9)
	m := Compute(c.store, rc.Basket.States, cur-60, cur)
	c.memo1[rc.Basket] = m
	return m
}

// trailing is the basket's trader window.
func (c *Calc) trailing(rc *devview.RowCtx) Minute {
	c.reset(rc.AtSec)
	if m, ok := c.memo5[rc.Basket]; ok {
		return m
	}
	m := Trailing(c.store, rc.Basket.States, rc.AtSec)
	c.memo5[rc.Basket] = m
	return m
}

// Columns is the trader pair — delta then class% (Δ3: wherever delta
// renders, class% renders beside it), both on the trailing window.
func (c *Calc) Columns() []devview.Column {
	return []devview.Column{
		{Name: "delta", Width: 6,
			Cell: func(rc *devview.RowCtx) string { m := c.trailing(rc); return FmtDelta(m.Delta, m.DeltaOK) },
			Style: func(rc *devview.RowCtx) string {
				return BasketStyle(c.trailing(rc), len(rc.Basket.States))
			}},
		{Name: "class%", Width: 6,
			Cell: func(rc *devview.RowCtx) string { m := c.trailing(rc); return FmtClass(m.Class, m.ClassOK) }},
	}
}

// DevColumns is the dev-view set: the trader pair plus delta_1m (the last
// completed minute alone) and delta_all (every rule in the numerator, same
// minute) so the ledger can compare them; neither reaches the trader table.
func (c *Calc) DevColumns() []devview.Column {
	cols := c.Columns()
	cols[0].Legend = "delta/class% = trailing 5 completed minutes, strong (quote/midpoint) rules over counted dollars; delta_1m = last completed minute; delta_all = that minute with the tick rule in the numerator"
	return append(cols,
		devview.Column{Name: "delta_1m", Width: 8,
			Cell: func(rc *devview.RowCtx) string { m := c.minute(rc); return FmtDelta(m.Delta, m.DeltaOK) }},
		devview.Column{Name: "delta_all", Width: 9,
			Cell: func(rc *devview.RowCtx) string { m := c.minute(rc); return FmtDelta(m.DeltaAll, m.DeltaAllOK) }},
	)
}

// Footer is the trader legend for the pair — statements of measurement
// only; goes through the scanner test.
const Footer = `delta             = trailing 5 completed minutes: dollars printed at/above the ask or above the midpoint, minus dollars at/below the bid or below the midpoint, over ALL counted dollars in the basket (+1.00 = every dollar on the ask side). Dollars the book could not place (late, no valid quote, tick-rule, block, non-price-forming) stay in the denominator and pull toward 0. Bold at/beyond ±0.20; not highlighted under 3 members or $5M in the window; raw, no 20-day typical yet; 09:30's first 30 s excluded (quotes wide/crossed at the open); from 09:33.
class%            = the honesty number beside delta, same 5-minute window: % of those counted dollars the book could place at/beyond the touch or off the midpoint — read +0.30 on 40% and +0.30 on 85% as different statements; through 09:35 the window is shorter than 5 minutes
`

// ExtendTrader inserts the pair after concentration_day (README column
// order) and the footer block before the gap-glyph line. Both anchors are
// flowshare's; a miss is a wiring error, not a layout choice — it panics
// at composition time (the premarket pre_share posture) rather than
// quietly appending a mis-ordered column.
func (c *Calc) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	pair := c.Columns()
	at := -1
	for i, col := range cols {
		if col.Name == "concentration_day" {
			at = i + 1
			break
		}
	}
	if at < 0 {
		panic("delta.ExtendTrader: no concentration_day column to anchor delta/class% after")
	}
	out := make([]devview.Column, 0, len(cols)+len(pair))
	out = append(out, cols[:at]...)
	out = append(out, pair...)
	out = append(out, cols[at:]...)
	i := strings.Index(footer, "\n·  ")
	if i < 0 {
		panic("delta.ExtendTrader: trader footer has no gap-glyph line to anchor the delta block before")
	}
	return out, footer[:i+1] + Footer + footer[i+1:]
}
