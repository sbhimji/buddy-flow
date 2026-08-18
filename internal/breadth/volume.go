// Volume breadth (mini-spec docs/mini-specs/3.2b-volume-breadth.md): the
// 3.2 extension counting members ON VOLUME — §2.5's "one stock up is news;
// nine up on volume is money" made literal. Per member, counted $vol per
// completed minute against the SAME ticker's matched-minute 20-day median
// (2.1 profiles), persistent over the same windows as the price side; the
// conjunction separates a theme-wide bid (`7/9`) from index drift (`1/9`).
//
// Correlation-blindness (F18) is WORSE here than for price breadth: ETF
// arbitrage prints real volume in every member mechanically, so on macro
// days these counts saturate universe-wide and read as broad accumulation.
// Read relative to what every basket shows, never in isolation.
package breadth

import (
	"fmt"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// VolK is the unusual-volume threshold: a member's minute is hot when its
// counted $vol exceeds VolK × its matched-minute median. Lenient end of the
// backlog's 1.5–2.0 because persistence already applies (VB1). A default
// like every other — tunable at the nightly ledger review.
const VolK = 1.5

// Volume persistence deliberately shares PersistMinutes: both gates
// describe the same last-P-completed-minutes span, and per-minute rvol
// against per-minute medians avoids the medians-don't-commute trap a
// trailing-sum baseline would step in (VB1).

// volState is one member's volume read: unusual ($), normal, or
// unmeasured (no basis — thin name or missing profile, VB3).
type volState int8

const (
	volNormal volState = iota
	volUnusual
	volUnmeasured
)

// VolCalc computes volume-breadth cells: the 3.2 Calc supplies price
// sides (one computation, two consumers), the store supplies live minutes,
// profiles supply matched-minute medians. Per-render member memoization,
// same single-render-goroutine posture as the price memos.
type VolCalc struct {
	price    *Calc
	store    *bucket.Store
	profiles map[string]*profile.Profile

	memoAt int64
	vols   map[*ingest.SymbolState]volState
}

// NewVol wires the volume calc. profiles is read-only shared state (the
// map devview already loaded); a member absent from it is unmeasured.
func NewVol(price *Calc, store *bucket.Store, profiles map[string]*profile.Profile) *VolCalc {
	return &VolCalc{price: price, store: store, profiles: profiles}
}

func (vc *VolCalc) memo(atSec int64) {
	if vc.memoAt == atSec && vc.vols != nil {
		return
	}
	vc.memoAt = atSec
	vc.vols = map[*ingest.SymbolState]volState{}
}

// vol is one member's volume state over the PersistMinutes completed
// minutes ending at end: unusual only when EVERY minute's counted $vol
// beats VolK × that minute's median (strict >, VB4); any minute without a
// basis (median 0, no profile) makes the member unmeasured — never a fake
// "unusual" (VB3). Memoized per render second (members repeat across
// overlapping baskets).
func (vc *VolCalc) vol(st *ingest.SymbolState, end int64) volState {
	if s, seen := vc.vols[st]; seen {
		return s
	}
	s := volUnusual
	p := vc.profiles[st.Symbol]
	for j := 0; j < PersistMinutes; j++ {
		minStart := end - int64(j+1)*60
		if p == nil {
			s = volUnmeasured
			break
		}
		row, ok := p.Minute(session.MinuteOfDay(minStart * 1e9))
		if !ok || row.MedianDollars == 0 {
			s = volUnmeasured
			break
		}
		_, dollars := profile.Counted(vc.store.Window(st, minStart, minStart+60))
		if dollars <= VolK*row.MedianDollars {
			s = volNormal
			break
		}
	}
	vc.vols[st] = s
	return s
}

// counts tallies a basket at atSec: unusual/unmeasured over full
// membership (direction-blind — needs no SPY), and the conjunction
// upOnVol/up against the 3.2 price sides. priceOK=false when the price
// side gaps (SPY unmeasurable, zero price-measured members, VB4);
// ok=false before the persistence horizon or with zero volume-measured
// members.
func (vc *VolCalc) counts(members []*ingest.SymbolState, atSec int64) (unusual, unmeasured, up, upOnVol int, priceOK, ok bool) {
	_, end, ok := window(atSec, PersistMinutes)
	if !ok {
		return 0, 0, 0, 0, false, false
	}
	vc.memo(atSec)
	for _, st := range members {
		switch vc.vol(st, end) {
		case volUnusual:
			unusual++
		case volUnmeasured:
			unmeasured++
		}
	}
	if unmeasured == len(members) {
		return 0, 0, 0, 0, false, false // all-unmeasured basket: a count would be a fake statement
	}
	sides, priceOK := vc.price.sides(members, atSec)
	if priceOK {
		measured := 0
		for i, m := range sides {
			if m.measured {
				measured++
			}
			if m.side > 0 {
				up++
				// A price-↑ member that is volume-unmeasured counts in
				// the denominator, never as on-volume (VB4).
				if vc.vol(members[i], end) == volUnusual {
					upOnVol++
				}
			}
		}
		priceOK = measured > 0 // reconcile with count's zero-measured gap
	}
	return unusual, unmeasured, up, upOnVol, priceOK, true
}

// UpOnVolColumn is the conjunction cell (dev view): of the members 3.2
// counts persistently ↑, how many are also on unusual volume — `7/9` is
// money, `1/9` is drift. Gaps whenever the 3.2 breadth cell gaps, so the
// two columns reconcile on screen.
func (vc *VolCalc) UpOnVolColumn() devview.Column {
	return devview.Column{Name: "up_on_vol", Width: 9,
		Legend: fmt.Sprintf("up_on_vol = of breadth's ↑ members, those also >%.1fx their matched-minute median $vol for %d min", VolK, PersistMinutes),
		Cell: func(rc *devview.RowCtx) string {
			_, _, upN, upVol, priceOK, ok := vc.counts(rc.Basket.States, rc.AtSec)
			if !ok || !priceOK {
				return gap
			}
			return fmt.Sprintf("%d/%d", upVol, upN)
		}}
}

// VolDetailColumn is the direction-blind detail (dev view): members on
// unusual volume / unmeasured (`7$ 2·`) over full membership — high `$`
// with mixed price breadth reads as a two-sided fight, not a bid. Needs no
// SPY, so it still renders on price-gapped rows.
func (vc *VolCalc) VolDetailColumn() devview.Column {
	return devview.Column{Name: "vol_detail", Width: 9,
		Legend: "vol_detail = members on unusual volume $ / unmeasured ·",
		Cell: func(rc *devview.RowCtx) string {
			unusual, unmeasured, _, _, _, ok := vc.counts(rc.Basket.States, rc.AtSec)
			if !ok {
				return gap
			}
			return fmt.Sprintf("%d$ %d·", unusual, unmeasured)
		}}
}
