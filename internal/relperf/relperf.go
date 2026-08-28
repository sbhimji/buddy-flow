// Package relperf is dev-plan story 3.6 RelPerf as MO-8's vs_SPY column
// (mini-spec docs/mini-specs/metric-overload/MO-8-run-metrics.md, R4): the
// price response beside the share run —
//
//	vs_SPY = mean over measured members of (member since-open return) − SPY since-open return
//
// equal-weighted, so the term measures the sector and not its largest
// member (3.6). The reference point is D8, since open, IDENTICAL to
// breadth's: every return comes from breadth's own anchors and last prices
// (breadth.Calc.Returns) — this package owns no price math, so vs_SPY and
// the breadth cell can never disagree about a member's return.
//
// Gap rules: SPY unmeasurable or no completed minute → gap. A member with
// no anchor or no price (breadth's "unmeasured") is EXCLUDED — an
// equal-weighted mean has no in-line middle to fold it into — and the
// dev view discloses the count (vs_SPY_n = measured/members). The mean
// renders only over enough of the basket to speak for it: measured ≥
// MinMeasured(N) = max(min(MinMeasuredAbs, N), ceil(N × MinMeasuredFrac))
// (review 2026-08-27), else the gap — two of nine names are not the
// sector. The trader cell is unstyled (review 2026-08-27: at ±10 bps the
// colour lit 19–21 of 22 rows on a −0.3% SPY morning — weight, not
// information); Flag keeps the dead-band predicate for MO-6 and the
// ledger. Renders are pure in (store state, second); every rendered
// string is a statement of price measurement, never a recommendation.
package relperf

import (
	"fmt"
	"math"
	"strings"

	"buddy-flow/internal/breadth"
	"buddy-flow/internal/devview"
)

// DeadBand is the colour half-width on vs_SPY — the SAME constant the SPY
// status line and the breadth states use (D7 default ±10 bps), so the
// three cannot disagree about "in line with the index".
const DeadBand = breadth.DeadBand

// Partial-membership floor: the mean renders only when at least
// MinMeasured(N) members have an anchor and a price.
const (
	MinMeasuredAbs  = 3
	MinMeasuredFrac = 0.5
)

// MinMeasured is the floor for a basket of n members:
// max(min(MinMeasuredAbs, n), ceil(n × MinMeasuredFrac)).
func MinMeasured(n int) int {
	abs := MinMeasuredAbs
	if n < abs {
		abs = n
	}
	frac := int(math.Ceil(float64(n) * MinMeasuredFrac))
	if frac > abs {
		return frac
	}
	return abs
}

const gap = "·"

// Calc is the per-render vs_SPY calc over a breadth.Calc. One per
// process; the memo is per render second on the single render goroutine.
type Calc struct {
	breadth *breadth.Calc

	memoAt int64
	memo   map[*devview.BasketRow]cell
}

// Cell is one basket's measurement: the equal-weighted mean, how many
// members it averages, and the membership size. OK=false is a defined gap.
type Cell struct {
	Mean     float64
	Measured int
	Members  int
	OK       bool
}

type cell = Cell

// New binds the breadth calc whose anchors and prices supply every return.
func New(bc *breadth.Calc) *Calc { return &Calc{breadth: bc} }

// At computes (once per basket per render) the basket's cell. The sum runs
// in member order with explicit float64 conversions (determinism).
func (c *Calc) At(rc *devview.RowCtx) Cell {
	if c.memoAt != rc.AtSec || c.memo == nil {
		c.memoAt, c.memo = rc.AtSec, map[*devview.BasketRow]cell{}
	}
	if v, ok := c.memo[rc.Basket]; ok {
		return v
	}
	v := Cell{Members: len(rc.Basket.States)}
	if rets, spy, ok := c.breadth.Returns(rc.Basket.States, rc.AtSec); ok {
		var sum float64
		for _, r := range rets {
			if !r.OK {
				continue
			}
			sum += float64(r.Ret) - float64(spy)
			v.Measured++
		}
		if v.Measured > 0 && v.Measured >= MinMeasured(v.Members) {
			v.Mean, v.OK = sum/float64(v.Measured), true
		}
	}
	c.memo[rc.Basket] = v
	return v
}

// Flag is the MO-6-style predicate: lit when the mean is beyond ±DeadBand,
// positive by sign, ok=false on a gap. The cell itself is unstyled; this
// is the predicate a glyph or the ledger reads.
func (c *Calc) Flag(rc *devview.RowCtx) (lit, positive, ok bool) {
	v := c.At(rc)
	if !v.OK {
		return false, false, false
	}
	return v.Mean > DeadBand || v.Mean < -DeadBand, v.Mean > 0, true
}

// Fmt renders a cell: "+0.92%", or the gap.
func Fmt(v Cell) string {
	if !v.OK {
		return gap
	}
	return fmt.Sprintf("%+.2f%%", 100*v.Mean)
}

// Column is the vs_SPY cell — unstyled on both views (see the package
// doc); the sign carries the read.
func (c *Calc) Column() devview.Column {
	return devview.Column{Name: "vs_SPY", Width: 7, Cell: func(rc *devview.RowCtx) string { return Fmt(c.At(rc)) }}
}

// DevColumns is the dev-view set: vs_SPY plus vs_SPY_n, the measured
// count over membership the mean averages (R4 disclosure).
func (c *Calc) DevColumns() []devview.Column {
	col := c.Column()
	col.Legend = "vs_SPY = equal-weighted mean of members' since-open return minus SPY's, completed minute; vs_SPY_n = members measured / members"
	return []devview.Column{col,
		{Name: "vs_SPY_n", Width: 8, Cell: func(rc *devview.RowCtx) string {
			v := c.At(rc)
			if !v.OK {
				return gap
			}
			return fmt.Sprintf("%d/%d", v.Measured, v.Members)
		}},
	}
}

// Footer is the trader legend for the column and the reading guide for
// the three run columns as a row sentence. Statements of measurement
// only; the dead-band text is built from the shared constant. Goes
// through the scanner test.
var Footer = fmt.Sprintf(`vs_SPY            = the basket's price move since the open minus SPY's, through the last completed minute: each stock's own %% change from its first print of the session, averaged with equal weight (the biggest name counts once, like the smallest), minus the index's — only stocks that have printed since the open are averaged, and the cell is · until at least %d of them (or half the basket, whichever is larger) have — a mean of two names is not the sector
reading the row   = "+2.4σ since 09:41, 5m +1.8σ, +0.9%% vs SPY" reads: share above typical since 09:41, still above typical in the last five minutes, price ahead of the index; "+2.4σ since 09:33, 5m −0.2σ, flat vs SPY" reads: share was above typical from 09:33, the last five minutes are at typical, price with the index — measurements of what the tape has done, nothing more
`, MinMeasuredAbs)

// ExtendTrader inserts vs_SPY after 5m_z (README column order:
// … since 5m_z vs_SPY breadth …) and the footer block before the
// gap-glyph line. Both anchors are flowshare's (Run's column, the trader
// footer's gap line); a miss is a wiring error and panics at composition
// — never a silent mis-ordered append (delta's posture).
func (c *Calc) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	at := -1
	for i, col := range cols {
		if col.Name == "5m_z" {
			at = i + 1
			break
		}
	}
	if at < 0 {
		panic("relperf.ExtendTrader: no 5m_z column to anchor vs_SPY after")
	}
	out := make([]devview.Column, 0, len(cols)+1)
	out = append(out, cols[:at]...)
	out = append(out, c.Column())
	out = append(out, cols[at:]...)
	i := strings.Index(footer, "\n·  ")
	if i < 0 {
		panic("relperf.ExtendTrader: trader footer has no gap-glyph line to anchor the vs_SPY block before")
	}
	return out, footer[:i+1] + Footer + footer[i+1:]
}
