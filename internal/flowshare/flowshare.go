// Package flowshare is the 3.1 runtime metric (mini-spec
// docs/mini-specs/3.1-flowshare.md): FlowShare, FlowShareZ, and the A8
// concentration stat, computed at read time from the 1-second bucket store,
// the nightly share profiles, and the σ floors — never on the ingest hot
// path.
//
// Zero-sum caveat (D1): shares are zero-sum only INSIDE the tracked
// universe — flow leaving the universe entirely redistributes shares
// misleadingly (mitigated by 3.7's de-grossing precedence). The denominator
// is the union of distinct basket members (benchmarks excluded); numerators
// keep full membership, so overlapping baskets' shares can sum >100%.
package flowshare

import (
	"fmt"
	"sort"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/zguard"
)

// Share returns basket counted $vol / universe counted $vol over
// [fromSec, toSec). ok=false when the universe traded nothing — 0/0 is a
// gap, never 0% (D5). Single pass over the union, numerator summed from
// the SAME reads as the denominator: two separate passes would race the
// live pipeline writer between them and could render a share above 100% —
// a false statement of measurement. Basket ⊆ union by construction (D1),
// so one pass caps share at 100%.
func Share(store *bucket.Store, basket, union []*ingest.SymbolState, fromSec, toSec int64) (float64, bool) {
	in := make(map[*ingest.SymbolState]bool, len(basket))
	for _, st := range basket {
		in[st] = true
	}
	var uni, bd float64
	for _, st := range union {
		b := store.Window(st, fromSec, toSec)
		_, d := profile.Counted(b)
		uni += d
		if in[st] {
			bd += d
		}
	}
	if uni == 0 {
		return 0, false
	}
	return bd / uni, true
}

// MinProfiledDays gates z computation (2.1's rule at basket level, D3): a
// basket contributes a z only after this many sampled days at the minute.
const MinProfiledDays = 10

// ShareZ returns the guarded z of a completed minute's share against its
// matched-bucket baseline. ok=false outside the profiled session, below
// the day gate, on a 0/0 minute, or when zguard nulls (σ and floor both 0,
// NaN/Inf).
func ShareZ(share float64, shareOK bool, prof *profile.ShareProfile, floors *profile.Floors, minuteOfDay int) (float64, bool) {
	if !shareOK {
		return 0, false
	}
	row, ok := prof.Minute(minuteOfDay)
	if !ok || row.Days < MinProfiledDays {
		return 0, false
	}
	fr, ok := floors.Minute(minuteOfDay)
	if !ok {
		return 0, false
	}
	return zguard.Z(share, row.MedianShare, row.SigmaShare, fr.SigmaFloorFlowShare)
}

// CumShareZ returns the guarded z of a since-open cumulative share against
// its matched-minute cumulative baseline (D7). Same null posture as ShareZ,
// gated on CumDays and using the cumulative floor family.
func CumShareZ(share float64, shareOK bool, prof *profile.ShareProfile, floors *profile.Floors, minuteOfDay int) (float64, bool) {
	if !shareOK {
		return 0, false
	}
	row, ok := prof.Minute(minuteOfDay)
	if !ok || row.CumDays < MinProfiledDays {
		return 0, false
	}
	fr, ok := floors.Minute(minuteOfDay)
	if !ok {
		return 0, false
	}
	return zguard.Z(share, row.MedianCumShare, row.SigmaCumShare, fr.SigmaFloorCumShare)
}

// Concentration returns the basket's top member by counted $vol over
// [fromSec, toSec) and its fraction of the basket total. Ties break
// first-wins over the given order (members arrive sorted, so alphabetical
// — determinism, D5). ok=false when the basket traded nothing: "X 0%"
// would be a fake statement.
func Concentration(store *bucket.Store, basket []*ingest.SymbolState, fromSec, toSec int64) (sym string, frac float64, ok bool) {
	var total, top float64
	for _, st := range basket {
		b := store.Window(st, fromSec, toSec)
		_, d := profile.Counted(b)
		total += d
		if d > top {
			top, sym = d, st.Symbol
		}
	}
	if total == 0 {
		return "", 0, false
	}
	return sym, top / total, true
}

const gap = "·"

// SignificantZ is the trader-view highlight threshold (trader-view-v0 T2):
// |cum_share_z| at or beyond it renders bold green/red by sign. A default
// like every other — tunable at the nightly ledger review, not config yet.
const SignificantZ = 2.0

const (
	sgrGreen = "\x1b[1;32m"
	sgrRed   = "\x1b[1;31m"
)

// Union resolves the D1 denominator — every distinct basket member, sorted
// — against the symbol table. Errors on a member missing from the table
// (universe.Load includes all members, so that means mismatched configs).
func Union(baskets []universe.Basket, table *ingest.Table) ([]*ingest.SymbolState, error) {
	set := map[string]bool{}
	for _, b := range baskets {
		for _, m := range b.Members {
			set[m] = true
		}
	}
	syms := make([]string, 0, len(set))
	for s := range set {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	out := make([]*ingest.SymbolState, 0, len(syms))
	for _, s := range syms {
		st := table.Lookup(s)
		if st == nil {
			return nil, fmt.Errorf("basket member %s not in symbol table", s)
		}
		out = append(out, st)
	}
	return out, nil
}

// LoadBaselines reads every basket's share profile (enforcing the D2a
// membership stamp) and the floors — the hard prerequisites for any share
// column. A missing or stale baseline errors loudly; fake zeros are not an
// option.
func LoadBaselines(profilesDir string, baskets []universe.Basket) (map[string]*profile.ShareProfile, *profile.Floors, error) {
	shares := map[string]*profile.ShareProfile{}
	for _, b := range baskets {
		sp, err := profile.ReadShares(profilesDir, b.Name, b.Members)
		if err != nil {
			return nil, nil, err
		}
		shares[b.Name] = sp
	}
	floors, err := profile.ReadFloors(profilesDir)
	if err != nil {
		return nil, nil, err
	}
	return shares, floors, nil
}

// winSnap is one window's union snapshot: every union member's counted $vol
// read in a single pass. All rows of a render divide by the SAME reads, so
// (a) no basket can exceed 100% (its numerator sums a subset of the same
// snapshot) and (b) shares across rows are mutually comparable — two
// desynchronized passes against the live writer would guarantee neither.
type winSnap struct {
	fromSec, toSec int64
	atSec          int64 // render second: memoization is per RENDER, never
	// across renders — late prints amend completed buckets, so a snapshot
	// held across renders would go stale for up to a minute.
	dollars map[*ingest.SymbolState]float64
	uni     float64
	valid   bool
}

func (w *winSnap) get(store *bucket.Store, union []*ingest.SymbolState, fromSec, toSec, atSec int64) {
	if w.valid && w.fromSec == fromSec && w.toSec == toSec && w.atSec == atSec {
		return
	}
	w.fromSec, w.toSec, w.atSec, w.uni, w.valid = fromSec, toSec, atSec, 0, true
	if w.dollars == nil {
		w.dollars = make(map[*ingest.SymbolState]float64, len(union))
	}
	for _, st := range union {
		b := store.Window(st, fromSec, toSec)
		_, d := profile.Counted(b)
		w.dollars[st] = d
		w.uni += d
	}
}

func (w *winSnap) share(basket []*ingest.SymbolState) (float64, bool) {
	if w.uni == 0 {
		return 0, false
	}
	var bd float64
	for _, st := range basket {
		bd += w.dollars[st]
	}
	return bd / w.uni, true
}

// Columns builds the three 3.1 dev-view columns (registered by the command
// — mini-spec D6: registry only, no renderer changes). flow_share describes
// the in-progress minute (a ratio is meaningful intra-minute); flow_share_z
// and concentration describe the last completed minute, like the other
// completed-minute columns.
//
// The two window snapshots are memoized across rows of a render (same
// posture as RowCtx memoization: cells run on the single render goroutine
// — the render loop, then the final render — never concurrently).
// cells is the shared plumbing behind both column sets: the window
// snapshots plus the D7 cumulative-window logic. One instance per composed
// view (dev or trader) — snapshots memoize per render (see winSnap).
type cells struct {
	store  *bucket.Store
	union  []*ingest.SymbolState
	shares map[string]*profile.ShareProfile
	floors *profile.Floors

	curWin, prevWin, cumWin winSnap
}

func (c *cells) prevShare(rc *devview.RowCtx) (float64, bool) {
	cur := session.MinuteStart(rc.AtSec * 1e9)
	c.prevWin.get(c.store, c.union, cur-60, cur, rc.AtSec)
	return c.prevWin.share(rc.Basket.States)
}

// cumWindow is the D7 window: open through the end of the last completed
// minute, clamped at the close so post-close renders show the whole-day
// share. key is the matched-bucket minute for the baseline. ok=false
// before the first completed session minute.
func (c *cells) cumWindow(atSec int64) (fromSec, toSec int64, key int, ok bool) {
	date := session.Date(atSec * 1e9)
	openSec, err1 := session.BucketStart(date, session.OpenMinute)
	closeSec, err2 := session.BucketStart(date, session.CloseMinute)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	end := session.MinuteStart(atSec * 1e9)
	if end > closeSec {
		end = closeSec
	}
	if end <= openSec {
		return 0, 0, 0, false
	}
	return openSec, end, session.MinuteOfDay((end - 60) * 1e9), true
}

func (c *cells) cumShare(rc *devview.RowCtx) (float64, int, bool) {
	from, to, key, ok := c.cumWindow(rc.AtSec)
	if !ok {
		return 0, 0, false
	}
	c.cumWin.get(c.store, c.union, from, to, rc.AtSec)
	s, ok := c.cumWin.share(rc.Basket.States)
	return s, key, ok
}

// cumZ computes the guarded cumulative z for a row, all gates applied.
func (c *cells) cumZ(rc *devview.RowCtx) (float64, bool) {
	prof := c.shares[rc.Basket.Name]
	if prof == nil {
		return 0, false
	}
	s, key, sOK := c.cumShare(rc)
	return CumShareZ(s, sOK, prof, c.floors, key)
}

func (c *cells) cumShareCol() devview.Column {
	return devview.Column{Name: "cum_share", Width: 9,
		Legend: "cum_share* = since open through completed minute",
		Cell: func(rc *devview.RowCtx) string {
			s, _, ok := c.cumShare(rc)
			if !ok {
				return gap
			}
			return fmt.Sprintf("%.2f%%", 100*s)
		}}
}

func (c *cells) cumShareTypCol() devview.Column {
	return devview.Column{Name: "cum_share_typ", Width: 13, Cell: func(rc *devview.RowCtx) string {
		prof := c.shares[rc.Basket.Name]
		if prof == nil {
			return gap
		}
		_, key, ok := c.cumShare(rc)
		if !ok {
			return gap
		}
		// The typical is a measurement of history: shown whenever any
		// day sampled this minute; only the z carries the 10-day gate.
		row, ok := prof.Minute(key)
		if !ok || row.CumDays == 0 {
			return gap
		}
		return fmt.Sprintf("%.2f%%", 100*row.MedianCumShare)
	}}
}

func (c *cells) cumShareZCol(style bool) devview.Column {
	col := devview.Column{Name: "cum_share_z", Width: 11, Cell: func(rc *devview.RowCtx) string {
		z, ok := c.cumZ(rc)
		if !ok {
			return gap
		}
		return fmt.Sprintf("%+.1f", z)
	}}
	if style {
		// T2: |z| ≥ SignificantZ renders bold green/red by SIGN — a
		// statement of measurement (share above/below its own typical),
		// never buy/sell language.
		col.Style = func(rc *devview.RowCtx) string {
			z, ok := c.cumZ(rc)
			switch {
			case !ok || z < SignificantZ && z > -SignificantZ:
				return ""
			case z > 0:
				return sgrGreen
			default:
				return sgrRed
			}
		}
	}
	return col
}

func (c *cells) concentrationCol() devview.Column {
	return devview.Column{Name: "concentration", Width: 13, Cell: func(rc *devview.RowCtx) string {
		cur := session.MinuteStart(rc.AtSec * 1e9)
		sym, frac, ok := Concentration(c.store, rc.Basket.States, cur-60, cur)
		if !ok {
			return gap
		}
		return fmt.Sprintf("%s %.0f%%", sym, 100*frac)
	}}
}

// Columns builds the three 3.1 dev-view columns (registered by the command
// — mini-spec D6: registry only, no renderer changes). flow_share describes
// the in-progress minute (a ratio is meaningful intra-minute); flow_share_z
// and concentration describe the last completed minute, like the other
// completed-minute columns.
//
// The window snapshots are memoized across rows of a render (same posture
// as RowCtx memoization: cells run on the single render goroutine — the
// render loop, then the final render — never concurrently).
func Columns(store *bucket.Store, union []*ingest.SymbolState, shares map[string]*profile.ShareProfile, floors *profile.Floors) []devview.Column {
	c := &cells{store: store, union: union, shares: shares, floors: floors}
	return []devview.Column{
		{Name: "flow_share", Width: 10,
			Legend: "flow_share = in-progress minute; flow_share_z/concentration = completed minute",
			Cell: func(rc *devview.RowCtx) string {
				cur := session.MinuteStart(rc.AtSec * 1e9)
				c.curWin.get(c.store, c.union, cur, rc.AtSec+1, rc.AtSec)
				s, ok := c.curWin.share(rc.Basket.States)
				if !ok {
					return gap
				}
				return fmt.Sprintf("%.2f%%", 100*s)
			}},
		{Name: "flow_share_z", Width: 12, Cell: func(rc *devview.RowCtx) string {
			prof := c.shares[rc.Basket.Name]
			if prof == nil {
				return gap
			}
			s, sOK := c.prevShare(rc)
			cur := session.MinuteStart(rc.AtSec * 1e9)
			z, ok := ShareZ(s, sOK, prof, c.floors, session.MinuteOfDay((cur-60)*1e9))
			if !ok {
				return gap
			}
			return fmt.Sprintf("%+.1f", z)
		}},
		c.concentrationCol(),
		c.cumShareCol(),
		c.cumShareTypCol(),
		c.cumShareZCol(false),
	}
}

// TraderColumns composes the trader-view-v0 set: the since-open story,
// one instantaneous pulse (relative_vol), and concentration — sorted by
// the returned rank (cum z descending, gaps last). No per-minute share z:
// it flickers; the cumulative z carries the story.
func TraderColumns(store *bucket.Store, union []*ingest.SymbolState, shares map[string]*profile.ShareProfile, floors *profile.Floors) (cols []devview.Column, rank func(rc *devview.RowCtx) (float64, bool)) {
	c := &cells{store: store, union: union, shares: shares, floors: floors}
	relVol := devview.Column{Name: "relative_vol", Width: 12,
		Legend: "relative_vol = completed minute's $vol vs its 20d matched-minute baseline",
		Cell: func(rc *devview.RowCtx) string {
			base, ok := rc.PrevMinuteBaseline()
			if !ok || base == 0 {
				return gap
			}
			return fmt.Sprintf("%.2f", rc.PrevMinuteCounted()/base)
		}}
	cum := c.cumShareCol()
	cum.Legend = "cum_share* = share of tracked tape since open, through completed minute; bold = |z| ≥ 2"
	return []devview.Column{
		cum,
		c.cumShareTypCol(),
		c.cumShareZCol(true),
		relVol,
		c.concentrationCol(),
	}, c.cumZ
}
