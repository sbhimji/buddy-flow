// Package breadth is the 3.2 runtime metric (mini-spec
// docs/mini-specs/3.2-breadth.md): per basket, how many members are
// persistently outperforming SPY since the open — three-state per member
// (↑/·/↓) with a ±10 bps dead-band (D7 default) and a 3-minute persistence
// filter, rendered as the raw fraction so small-basket evidence is visibly
// weaker. Computed at read time from the 1-second bucket store; never on
// the ingest hot path.
//
// Correlation-blind, stated (F18): ETF arbitrage moves members
// mechanically — breadth cannot tell a crowd from an index print. Per the
// dev plan it is one Glow input, never standalone; its trader-view seat is
// observational. Prices are the raw tape's last print (bucket.LastPrice,
// unfiltered by class) at 1-second resolution — a late NON_PRICE_FORMING
// print can briefly misprice a member; the dead-band and
// fraction-over-members damp that to at most one count (mini-spec B2; a
// price-forming "last" is 3.3's concern).
package breadth

import (
	"fmt"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

// ReferenceSymbol is the benchmark every member's since-open return is
// measured against (§2.5).
const ReferenceSymbol = "SPY"

// DeadBand is the in-line half-width on the return difference (D7 default
// ±10 bps). A default like every other — tunable at the nightly ledger
// review, not config yet.
const DeadBand = 0.0010

// PersistMinutes is how many consecutive completed minutes a member must
// hold one side before it counts (dev-plan default 3). Before this many
// completed session minutes the column gaps — never a fake 0/N.
const PersistMinutes = 3

const gap = "·"

// Calc computes breadth cells from the store. Per-render price reads are
// memoized (members repeat across overlapping baskets); same posture as
// flowshare's winSnap: cells run on the single render goroutine — the
// render loop, then the final render — never concurrently.
type Calc struct {
	store *bucket.Store
	spy   *ingest.SymbolState

	memoAt  int64 // render second the memos are valid for
	anchors map[*ingest.SymbolState]price
	lasts   map[priceKey]price
}

type price struct {
	v  float64
	ok bool
}

type priceKey struct {
	st     *ingest.SymbolState
	endSec int64
}

// New resolves SPY against the symbol table. A missing reference symbol is
// a config error, loud at construction — never a silently gapped column.
func New(store *bucket.Store, table *ingest.Table) (*Calc, error) {
	spy := table.Lookup(ReferenceSymbol)
	if spy == nil {
		return nil, fmt.Errorf("breadth reference symbol %s not in symbol table", ReferenceSymbol)
	}
	return &Calc{store: store, spy: spy}, nil
}

// window returns the since-open span: open through the end of the last
// completed minute, clamped at the close (post-close renders keep the
// whole-day read). ok=false before minMinutes completed session minutes —
// breadth needs PersistMinutes of them, the SPY status just one.
func window(atSec int64, minMinutes int) (openSec, endSec int64, ok bool) {
	date := session.Date(atSec * 1e9)
	openSec, err1 := session.BucketStart(date, session.OpenMinute)
	closeSec, err2 := session.BucketStart(date, session.CloseMinute)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	end := session.MinuteStart(atSec * 1e9)
	if end > closeSec {
		end = closeSec
	}
	if end < openSec+int64(minMinutes)*60 {
		return 0, 0, false
	}
	return openSec, end, true
}

func (c *Calc) memo(atSec int64) {
	if c.memoAt == atSec && c.anchors != nil {
		return
	}
	c.memoAt = atSec
	c.anchors = map[*ingest.SymbolState]price{}
	c.lasts = map[priceKey]price{}
}

func (c *Calc) anchor(st *ingest.SymbolState, openSec, endSec int64) price {
	p, seen := c.anchors[st]
	if !seen {
		p.v, p.ok = c.store.FirstTradePrice(st, openSec, endSec)
		p.ok = p.ok && p.v > 0
		c.anchors[st] = p
	}
	return p
}

func (c *Calc) last(st *ingest.SymbolState, openSec, endSec int64) price {
	k := priceKey{st, endSec}
	p, seen := c.lasts[k]
	if !seen {
		p.v, p.ok = c.store.LastTradePrice(st, openSec, endSec)
		p.ok = p.ok && p.v > 0
		c.lasts[k] = p
	}
	return p
}

// state is one member's three-state at one window end: +1 ↑, −1 ↓, 0 ·.
// ok=false when the member (or the window's SPY read, checked by the
// caller) is unmeasurable there.
func (c *Calc) state(st *ingest.SymbolState, openSec, endSec int64, spyRet float64) (int, bool) {
	a := c.anchor(st, openSec, endSec)
	p := c.last(st, openSec, endSec)
	if !a.ok || !p.ok {
		return 0, false
	}
	diff := (p.v/a.v - 1) - spyRet
	switch {
	case diff > DeadBand:
		return 1, true
	case diff < -DeadBand:
		return -1, true
	}
	return 0, true
}

// memberSide is one member's persistent read: side ∈ {+1, 0, −1};
// side folds a member without a defined state in every persistence window
// to 0 (the in-line middle) — persistence never demonstrated is never a
// partial ↑/↓.
type memberSide struct {
	side int
}

// sides computes every member's persistent side over the PersistMinutes
// windows ending at the last completed minute — the per-member tally count
// aggregates and the 3.2b volume conjunction reuses (one computation, two
// consumers). ok=false before the persistence horizon, when SPY is
// unmeasurable in any needed window, or when NO member measured — the
// zero-measured gate lives here, its single owner, so the breadth cell and
// the 3.2b conjunction can never disagree on it (VB4).
func (c *Calc) sides(members []*ingest.SymbolState, atSec int64) ([]memberSide, bool) {
	openSec, end, ok := window(atSec, PersistMinutes)
	if !ok {
		return nil, false
	}
	c.memo(atSec)
	spyRets := make([]float64, PersistMinutes)
	for j := 0; j < PersistMinutes; j++ {
		endJ := end - int64(j)*60
		sa := c.anchor(c.spy, openSec, endJ)
		sp := c.last(c.spy, openSec, endJ)
		if !sa.ok || !sp.ok {
			return nil, false
		}
		spyRets[j] = sp.v/sa.v - 1
	}
	out := make([]memberSide, len(members))
	measured := 0
	for i, st := range members {
		side, sideOK := 0, true
		for j := 0; j < PersistMinutes; j++ {
			s, sOK := c.state(st, openSec, end-int64(j)*60, spyRets[j])
			if !sOK {
				sideOK = false
				break
			}
			if j == 0 {
				side = s
			} else if s != side {
				side = 0
			}
		}
		if !sideOK {
			side = 0
		} else {
			measured++
		}
		out[i] = memberSide{side: side}
	}
	if measured == 0 {
		return nil, false // "0/9" over nothing measured would be a fake statement
	}
	return out, true
}

// count tallies a basket: up/inline/down over full membership. A member
// must hold one non-inline side across ALL PersistMinutes windows to count
// ↑/↓; everything else — in-line, flapping, unmeasured — is the middle.
// ok=false comes entirely from sides (SPY unmeasurable, horizon, or no
// member measured).
func (c *Calc) count(members []*ingest.SymbolState, atSec int64) (up, inline, down int, ok bool) {
	sides, ok := c.sides(members, atSec)
	if !ok {
		return 0, 0, 0, false
	}
	for _, m := range sides {
		switch {
		case m.side > 0:
			up++
		case m.side < 0:
			down++
		default:
			inline++
		}
	}
	return up, inline, down, true
}

const (
	sgrGreen  = "\x1b[1;32m"
	sgrRed    = "\x1b[1;31m"
	sgrYellow = "\x1b[1;33m"
	sgrReset  = "\x1b[0m"
)

// Status renders the clock-line SPY read (trader-view-v0 T9): the index's
// own price change since the open, through the end of the last completed
// minute — the reference every breadth state is measured against, so the
// two reconcile by construction. Colored by the SAME ±DeadBand the member
// states use: green above it, red below it, yellow inside — a statement of
// measurement (the index's own move), never a recommendation. Gap before
// the first completed minute or when SPY is unmeasurable; colored bytes
// are still deterministic bytes (devview.SetStatus purity contract).
func (c *Calc) Status(atSec int64) string {
	openSec, end, ok := window(atSec, 1)
	if !ok {
		return "SPY " + gap + " since open"
	}
	c.memo(atSec)
	a := c.anchor(c.spy, openSec, end)
	p := c.last(c.spy, openSec, end)
	if !a.ok || !p.ok {
		return "SPY " + gap + " since open"
	}
	ret := p.v/a.v - 1
	sgr := sgrYellow
	switch {
	case ret > DeadBand:
		sgr = sgrGreen
	case ret < -DeadBand:
		sgr = sgrRed
	}
	return fmt.Sprintf("SPY %s%+.2f%%%s since open", sgr, 100*ret, sgrReset)
}

// HighlightFrac is the breadth highlight threshold (trader-view-v0 T7):
// when MORE than this fraction of full membership sits persistently on one
// side, the cell renders bold — green for outperformers, red for
// underperformers (a broad down-side is the de-grossing fingerprint). A
// default like every other — tunable at the nightly ledger review.
const HighlightFrac = 0.70

// Column is the raw-fraction cell (trader + dev): persistent outperformers
// over FULL membership — `8/9` reads strong, `2/3` reads weak, which is
// the point (F17 small-basket display). styled adds the HighlightFrac
// coloring (trader view); the dev view renders plain like its other
// columns.
func (c *Calc) Column(styled bool) devview.Column {
	col := devview.Column{Name: "breadth", Width: 7, Cell: func(rc *devview.RowCtx) string {
		up, _, _, ok := c.count(rc.Basket.States, rc.AtSec)
		if !ok {
			return gap
		}
		return fmt.Sprintf("%d/%d", up, len(rc.Basket.States))
	}}
	if styled {
		col.Style = func(rc *devview.RowCtx) string {
			up, _, down, ok := c.count(rc.Basket.States, rc.AtSec)
			if !ok {
				return ""
			}
			n := float64(len(rc.Basket.States))
			switch {
			case float64(up)/n > HighlightFrac:
				return sgrGreen
			case float64(down)/n > HighlightFrac:
				return sgrRed
			}
			return ""
		}
	}
	return col
}

// DetailColumn is the three-state detail (dev view only — the dev plan's
// hover/detail surface, terminal edition): `6↑ 1· 2↓`.
func (c *Calc) DetailColumn() devview.Column {
	return devview.Column{Name: "breadth_detail", Width: 12,
		Legend: "breadth* = members persistently out/underperforming SPY since open (3 min beyond ±10 bps)",
		Cell: func(rc *devview.RowCtx) string {
			up, inline, down, ok := c.count(rc.Basket.States, rc.AtSec)
			if !ok {
				return gap
			}
			return fmt.Sprintf("%d↑ %d· %d↓", up, inline, down)
		}}
}
