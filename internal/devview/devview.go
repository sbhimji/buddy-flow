// Package devview is the 3.0 developer view (mini-spec:
// docs/mini-specs/3.0-dev-view.md): a crude terminal table, one row per
// basket, one column per metric — the learning instrument every Phase 3
// story adds its column to the day the metric is written.
//
// Pull model: the View is an ingest.Observer only to delegate to the bucket
// store and track the replayed clock; all metric math happens at render
// time, reading 1-second buckets and 2.1 profiles. Render is pure —
// (state, second) in, string out, no wall clock, no ANSI — which is what
// makes the snapshot test byte-exact and paced/instant replay the same code
// path.
package devview

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// BasketRow is one render row: a basket resolved against the symbol table.
type BasketRow struct {
	Name   string
	States []*ingest.SymbolState
}

// RowCtx is what a column's cell function sees: the row, the render second,
// and the view for store/profile reads. Shared window/baseline reads are
// memoized per row, so columns that use the same quantity (e.g.
// last_minute_dollar_vol and relative_vol) see ONE store read — a row is
// internally consistent even while the
// pipeline is writing, and renders don't rescan windows per column.
type RowCtx struct {
	View   *View
	Basket *BasketRow
	AtSec  int64 // epoch second the table is rendered "as of"

	havePrev    bool
	prevCounted float64
	haveBase    bool
	baseDollars float64
	baseInProf  bool
}

// minuteStart returns the epoch second the render second's minute began.
func (rc *RowCtx) minuteStart() int64 { return session.MinuteStart(rc.AtSec * 1e9) }

// PrevMinuteCounted returns the basket's counted $vol over the last
// completed minute, computed once per row.
func (rc *RowCtx) PrevMinuteCounted() float64 {
	if !rc.havePrev {
		cur := rc.minuteStart()
		rc.prevCounted = rc.View.countedDollars(rc.Basket, cur-60, cur)
		rc.havePrev = true
	}
	return rc.prevCounted
}

// PrevMinuteBaseline returns the basket's matched-minute baseline for the
// last completed minute, computed once per row; ok=false outside the
// profiled session.
func (rc *RowCtx) PrevMinuteBaseline() (float64, bool) {
	if !rc.haveBase {
		rc.baseDollars, rc.baseInProf = rc.View.baselineDollars(
			rc.Basket, session.MinuteOfDay((rc.minuteStart()-60)*1e9))
		rc.haveBase = true
	}
	return rc.baseDollars, rc.baseInProf
}

// Column is one registered metric column. Cells are statements of
// measurement — numbers or a gap ("·"), never interpretation.
type Column struct {
	Name  string
	Width int
	Cell  func(rc *RowCtx) string
	// Legend, when set, is appended to the clock-line legend — registered
	// metrics document which minute they describe without renderer edits.
	Legend string
}

// View wraps the bucket store as the pipeline observer and renders the
// basket × column table on demand.
type View struct {
	store    *bucket.Store
	baskets  []BasketRow
	profiles map[string]*profile.Profile
	columns  []Column
	clockNs  atomic.Int64 // max SIP ts observed; the replayed session clock
}

// New resolves baskets against the symbol table and loads every member's
// profile from profDir. Every member must be in the table (universe.Load
// includes all basket members) and must have a profile — a silently absent
// baseline would render as a fake zero.
func New(store *bucket.Store, table *ingest.Table, baskets []universe.Basket, profDir string) (*View, error) {
	v := &View{store: store, profiles: map[string]*profile.Profile{}}
	for _, b := range baskets {
		row := BasketRow{Name: b.Name}
		for _, sym := range b.Members {
			st := table.Lookup(sym)
			if st == nil {
				return nil, fmt.Errorf("basket %s member %s not in symbol table", b.Name, sym)
			}
			row.States = append(row.States, st)
			if v.profiles[sym] == nil {
				p, err := profile.Read(profDir, sym)
				if err != nil {
					return nil, fmt.Errorf("basket %s: %w (build profiles first?)", b.Name, err)
				}
				v.profiles[sym] = p
			}
		}
		v.baskets = append(v.baskets, row)
	}
	sort.Slice(v.baskets, func(i, j int) bool { return v.baskets[i].Name < v.baskets[j].Name })
	v.columns = defaultColumns()
	return v, nil
}

// Register appends a metric column (the Phase 3 extension point).
func (v *View) Register(c Column) { v.columns = append(v.columns, c) }

// ObserveTrade delegates to the store and advances the session clock.
func (v *View) ObserveTrade(t *ingest.Trade) {
	v.store.ObserveTrade(t)
	v.advance(t.SipTs)
}

// ObserveQuote delegates to the store and advances the session clock.
func (v *View) ObserveQuote(q *ingest.Quote) {
	v.store.ObserveQuote(q)
	v.advance(q.SipTs)
}

// maxClockStepNs bounds a single forward clock jump. SIP timestamps arrive
// unvalidated from the feed (ms × 1e6); one corrupt far-future value must
// not pin a max-clock forever. No session spans a day, so a >24h jump is
// corruption, not a gap.
const maxClockStepNs = 24 * 60 * 60 * 1e9

// advance is called from the pipeline's single consumer goroutine; late
// prints may regress SIP ts, so the clock is a max, not a last.
func (v *View) advance(sipNs int64) {
	cur := v.clockNs.Load()
	if sipNs <= cur {
		return
	}
	if cur != 0 && sipNs-cur > maxClockStepNs {
		return
	}
	v.clockNs.Store(sipNs)
}

// ClockSec returns the replayed session clock as an epoch second (0 before
// any message).
func (v *View) ClockSec() int64 { return v.clockNs.Load() / 1e9 }

// countedDollars sums counted $vol (profile.Counted — the one policy
// source) over a basket's members for [fromSec, toSec).
func (v *View) countedDollars(b *BasketRow, fromSec, toSec int64) float64 {
	var dollars float64
	for _, st := range b.States {
		bk := v.store.Window(st, fromSec, toSec)
		_, d := profile.Counted(bk)
		dollars += d
	}
	return dollars
}

// baselineDollars sums member profile median dollars for an ET
// minute-of-day — basket baselines are read-time sums over per-ticker
// profiles, never stored. ok=false outside the profiled session.
func (v *View) baselineDollars(b *BasketRow, minuteOfDay int) (float64, bool) {
	var dollars float64
	for _, st := range b.States {
		row, ok := v.profiles[st.Symbol].Minute(minuteOfDay)
		if !ok {
			return 0, false
		}
		dollars += row.MedianDollars
	}
	return dollars, true
}

const gap = "·"

// defaultColumns is the day-one set (mini-spec D4). relative_vol computes on
// the last COMPLETED minute — a partial minute against a full-minute baseline
// reads falsely low — and reuses the SAME memoized last-minute and baseline
// reads its neighbor columns display, so the printed ratio always reconciles
// with the printed operands. No z column: basket σ cannot be summed from
// member σs; FlowShareZ registers here when 3.1 exists.
func defaultColumns() []Column {
	return []Column{
		{Name: "N", Width: 3, Cell: func(rc *RowCtx) string {
			return fmt.Sprintf("%d", len(rc.Basket.States))
		}},
		{Name: "last_minute_dollar_vol", Width: 22, Cell: func(rc *RowCtx) string {
			return fmtDollars(rc.PrevMinuteCounted())
		}},
		{Name: "20d_dollar_vol", Width: 14, Cell: func(rc *RowCtx) string {
			base, ok := rc.PrevMinuteBaseline()
			if !ok {
				return gap
			}
			return fmtDollars(base)
		}},
		{Name: "relative_vol", Width: 12, Cell: func(rc *RowCtx) string {
			base, ok := rc.PrevMinuteBaseline()
			if !ok || base == 0 {
				return gap
			}
			return fmt.Sprintf("%.2f", rc.PrevMinuteCounted()/base)
		}},
		{Name: "current_minute_dollar_vol", Width: 25, Cell: func(rc *RowCtx) string {
			return fmtDollars(rc.View.countedDollars(rc.Basket, rc.minuteStart(), rc.AtSec+1))
		}},
	}
}

// fmtDollars renders a dollar amount at three significant figures per unit
// (550, 1.50k, 53.3M, 371M, 1.23B) so displayed operands reconcile with
// displayed ratios. Pure function of the value — render determinism
// depends on it.
func fmtDollars(v float64) string {
	scaled := func(x float64, unit string) string {
		switch {
		case x < 10:
			return fmt.Sprintf("%.2f%s", x, unit)
		case x < 100:
			return fmt.Sprintf("%.1f%s", x, unit)
		default:
			return fmt.Sprintf("%.0f%s", x, unit)
		}
	}
	switch {
	case v >= 1e9:
		return scaled(v/1e9, "B")
	case v >= 1e6:
		return scaled(v/1e6, "M")
	case v >= 1e3:
		return scaled(v/1e3, "k")
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// Render returns the table as of an epoch second: a clock line, a header,
// one row per basket. Pure — same store state and second, same bytes.
func (v *View) Render(atSec int64) string {
	var sb strings.Builder
	et := time.Unix(atSec, 0).In(session.ET())
	// Name the minutes on screen: last_minute/20d/relative_vol all describe
	// the last COMPLETED minute; only current_minute is the in-progress one.
	lastMin := time.Unix(session.MinuteStart(atSec*1e9)-60, 0).In(session.ET())
	fmt.Fprintf(&sb, "%s ET  %s   last_minute/20d/relative_vol = %s minute (counted $vol vs its 20d matched-minute baseline); current_minute = %s so far",
		et.Format("15:04:05"), et.Format("2006-01-02"), lastMin.Format("15:04"), et.Format("15:04"))
	for _, c := range v.columns {
		if c.Legend != "" {
			sb.WriteString("; " + c.Legend)
		}
	}
	sb.WriteByte('\n')

	nameW := len("BASKET")
	for i := range v.baskets {
		if n := len(v.baskets[i].Name); n > nameW {
			nameW = n
		}
	}
	fmt.Fprintf(&sb, "%-*s", nameW, "BASKET")
	for _, c := range v.columns {
		fmt.Fprintf(&sb, "  %*s", c.Width, c.Name)
	}
	sb.WriteByte('\n')
	for i := range v.baskets {
		b := &v.baskets[i]
		rc := &RowCtx{View: v, Basket: b, AtSec: atSec} // one ctx per row: cells share memoized reads
		fmt.Fprintf(&sb, "%-*s", nameW, b.Name)
		for _, c := range v.columns {
			fmt.Fprintf(&sb, "  %*s", c.Width, c.Cell(rc))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

var _ ingest.Observer = (*View)(nil)
