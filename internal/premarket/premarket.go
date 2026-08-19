// Package premarket is the premarket-view-v0 read: where extended-hours
// dollars concentrated between 04:00 and the open, raw — no z, no
// "typical" (no premarket baselines exist, and 20-day matched buckets are
// likely too thin there; see the extended-hours backlog item this pulls
// forward). Live during premarket, frozen at 09:30, kept as context all
// day. Reads the NON_FLOW class through the premarket window
// (profile.ExtendedHours) — a session-scoped lens; the 0.3 classification
// and every regular-session metric are untouched.
//
// Honesty (footer-carried): premarket volume is thin, lumpy, and heavily
// off-exchange (F3 compounds pre-open) — read magnitudes, not σ.
package premarket

import (
	"fmt"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// PreOpenMinute is the premarket start, 04:00 ET — the earliest the
// vendor's extended-hours tape prints.
const PreOpenMinute = 4 * 60

const gap = "·"

// Calc computes the three premarket cells from the 1-second store. One
// union pass per render second (same memo posture as flowshare's
// snapshots: single render goroutine, never concurrent).
type Calc struct {
	store *bucket.Store
	union []*ingest.SymbolState

	memoAt  int64
	winOK   bool
	dollars map[*ingest.SymbolState]float64
	uni     float64
}

// New wires the calc over the same union the share metrics use (D1
// denominator: distinct basket members, benchmarks excluded).
func New(store *bucket.Store, union []*ingest.SymbolState) *Calc {
	return &Calc{store: store, union: union}
}

// window is the premarket span for atSec's date: [04:00, atSec+1) clamped
// at the open — after 09:30 the read freezes to the full premarket.
// ok=false before 04:00 or off a resolvable date.
func (c *Calc) window(atSec int64) (fromSec, toSec int64, ok bool) {
	date := session.Date(atSec * 1e9)
	pre, err1 := session.BucketStart(date, PreOpenMinute)
	open, err2 := session.BucketStart(date, session.OpenMinute)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	toSec = atSec + 1
	if toSec > open {
		toSec = open
	}
	if toSec <= pre {
		return 0, 0, false
	}
	return pre, toSec, true
}

// memo primes the per-render union snapshot; reports whether a premarket
// window exists at atSec. Gapped renders never read the (stale) map — the
// winOK guard is the contract.
func (c *Calc) memo(atSec int64) bool {
	if c.memoAt == atSec && c.dollars != nil {
		return c.winOK
	}
	c.memoAt = atSec
	if c.dollars == nil {
		c.dollars = make(map[*ingest.SymbolState]float64, len(c.union))
	}
	from, to, ok := c.window(atSec)
	c.winOK, c.uni = ok, 0
	if !ok {
		return false
	}
	for _, st := range c.union {
		_, d := profile.ExtendedHours(c.store.Window(st, from, to))
		c.dollars[st] = d
		c.uni += d
	}
	return true
}

// share is the basket's slice of universe premarket dollars; ok=false with
// no window or a 0/0 (a gap, never 0%).
func (c *Calc) share(rc *devview.RowCtx) (float64, bool) {
	if !c.memo(rc.AtSec) || c.uni == 0 {
		return 0, false
	}
	var bd float64
	for _, st := range rc.Basket.States {
		bd += c.dollars[st]
	}
	return bd / c.uni, true
}

// fmtUSD renders a dollar magnitude compactly ($1.2B / $34.5M / $680K).
func fmtUSD(d float64) string {
	switch {
	case d >= 1e9:
		return fmt.Sprintf("$%.1fB", d/1e9)
	case d >= 1e6:
		return fmt.Sprintf("$%.1fM", d/1e6)
	case d >= 1e3:
		return fmt.Sprintf("$%.0fK", d/1e3)
	default:
		return fmt.Sprintf("$%.0f", d)
	}
}

// Columns is the premarket column set. pre_vol renders $0 as $0 — zero
// premarket dollars in an open window is a true measurement; only a
// missing window gaps.
func (c *Calc) Columns() []devview.Column {
	return []devview.Column{
		{Name: "pre_vol", Width: 8, Cell: func(rc *devview.RowCtx) string {
			if !c.memo(rc.AtSec) {
				return gap
			}
			var bd float64
			for _, st := range rc.Basket.States {
				bd += c.dollars[st]
			}
			return fmtUSD(bd)
		}},
		{Name: "pre_share", Width: 9, Cell: func(rc *devview.RowCtx) string {
			s, ok := c.share(rc)
			if !ok {
				return gap
			}
			return fmt.Sprintf("%.1f%%", 100*s)
		}},
		{Name: "pre_conc", Width: 12, Cell: func(rc *devview.RowCtx) string {
			if !c.memo(rc.AtSec) {
				return gap
			}
			var total, top float64
			var sym string
			for _, st := range rc.Basket.States {
				d := c.dollars[st]
				total += d
				if d > top {
					top, sym = d, st.Symbol
				}
			}
			if total == 0 {
				return gap // "X 0%" would be a fake statement
			}
			return fmt.Sprintf("%s %.0f%%", sym, 100*top/total)
		}},
	}
}

// PreFooter is the plain-English legend block appended to the trader
// footer. Statements of measurement only (scope law).
const PreFooter = `pre_vol           = dollars traded in this basket's stocks 04:00-09:30 ET (extended-hours prints; a separate lens — no regular-session number includes them)
pre_share         = % of all premarket dollars across the tracked universe that went through this basket
pre_conc          = the single stock carrying the largest share of this basket's premarket dollars
premarket caveat  = raw magnitudes only — no 20-day "typical" exists premarket; volumes are thin, lumpy, and heavily off-exchange. Until the open completes its first minute, rows sort by pre_share.
`

// ExtendTrader appends the premarket columns to the trader-view set, adds
// the footer block, and installs the pre-open rank: before the first
// completed session minute rows sort by pre_share (the premarket money
// story); from then on the given rank (cum z) takes over. The fallback
// keys on the CLOCK, never on gaps, so the two scales cannot mix.
func (c *Calc) ExtendTrader(cols []devview.Column, rank func(*devview.RowCtx) (float64, bool), footer string) ([]devview.Column, func(*devview.RowCtx) (float64, bool), string) {
	cols = append(cols, c.Columns()...)
	wrapped := func(rc *devview.RowCtx) (float64, bool) {
		if c.preOpen(rc.AtSec) {
			return c.share(rc)
		}
		return rank(rc)
	}
	return cols, wrapped, footer + PreFooter
}

// preOpen reports whether atSec is before the first completed session
// minute of its date (i.e. before 09:31:00 ET). Unresolvable dates fall
// through to the regular rank.
func (c *Calc) preOpen(atSec int64) bool {
	open, err := session.BucketStart(session.Date(atSec*1e9), session.OpenMinute)
	if err != nil {
		return false
	}
	return session.MinuteStart(atSec*1e9) < open+60
}
