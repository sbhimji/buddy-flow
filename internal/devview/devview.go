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
	// Style, when set, returns an ANSI SGR prefix (or "") for the cell.
	// The renderer wraps the ALREADY-PADDED cell (ANSI bytes are invisible
	// to width, so styling after padding is what keeps columns aligned).
	// Colored bytes are still deterministic bytes — Render stays a pure
	// function of (store state, second).
	Style func(rc *RowCtx) string
}

// View wraps the bucket store as the pipeline observer and renders the
// basket × column table on demand.
type View struct {
	store      *bucket.Store
	baskets    []BasketRow
	profiles   map[string]*profile.Profile
	columns    []Column
	rank       func(rc *RowCtx) (float64, bool) // optional row order (SetRank)
	footer     string                           // optional fixed block below the table (SetFooter)
	status     func(atSec int64) string         // optional clock-line status (SetStatus)
	baseLegend bool                             // clock line carries the default columns' legend
	clockNs    atomic.Int64                     // max SIP ts observed; the replayed session clock
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
	v.columns, v.baseLegend = defaultColumns(), true
	return v, nil
}

// Register appends a metric column (the Phase 3 extension point).
func (v *View) Register(c Column) { v.columns = append(v.columns, c) }

// Profiles returns the per-ticker profiles New already loaded (every
// basket member, guaranteed present) so metric calcs outside the view can
// share them instead of re-reading the directory. The map is a fresh copy
// on each call — a caller cannot resize or repoint the view's own index
// (wiring-time only, so the copy costs nothing per render). The *Profile
// values are shared: profiles are immutable after load by contract.
func (v *View) Profiles() map[string]*profile.Profile {
	out := make(map[string]*profile.Profile, len(v.profiles))
	for sym, p := range v.profiles {
		out[sym] = p
	}
	return out
}

// SetColumns replaces the column set entirely — the trader view composes
// its own set instead of extending the dev defaults. The default columns'
// base legend goes with them; the new columns document themselves via
// Column.Legend.
func (v *View) SetColumns(cols []Column) { v.columns, v.baseLegend = cols, false }

// SetRank installs a row-ordering key: rows sort by descending key each
// render; rows with ok=false (no defined key — e.g. a gapped z) sort last;
// ties break by basket name. Nil (the default) keeps name order.
func (v *View) SetRank(rank func(rc *RowCtx) (float64, bool)) { v.rank = rank }

// SetFooter installs a fixed block rendered below the table, separated by a
// blank line — the trader view's plain-English column definitions travel
// with its composed column set; the dev view sets none (the default, empty,
// renders nothing). A fixed string keeps Render a pure function of
// (store state, second).
func (v *View) SetFooter(footer string) { v.footer = footer }

// SetStatus installs a clock-line status: the returned string (ANSI
// allowed; empty renders nothing) is appended to the clock line each
// render. The function must be pure in (store state, atSec) — Render's
// determinism contract extends to it. Nil (the default) renders nothing.
func (v *View) SetStatus(status func(atSec int64) string) { v.status = status }

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
	// Name the minutes on screen. The base legend describes the DEFAULT
	// column set and is cleared by SetColumns — a composed view (trader)
	// must not inherit a legend for columns it doesn't render; its columns
	// carry their own Legend entries.
	lastMin := time.Unix(session.MinuteStart(atSec*1e9)-60, 0).In(session.ET())
	fmt.Fprintf(&sb, "%s ET  %s  ", et.Format("15:04:05"), et.Format("2006-01-02"))
	if v.baseLegend {
		fmt.Fprintf(&sb, " last_minute/20d/relative_vol = %s minute (counted $vol vs its 20d matched-minute baseline); current_minute = %s so far",
			lastMin.Format("15:04"), et.Format("15:04"))
	} else {
		fmt.Fprintf(&sb, " completed minute = %s; in-progress = %s", lastMin.Format("15:04"), et.Format("15:04"))
	}
	for _, c := range v.columns {
		if c.Legend != "" {
			sb.WriteString("; " + c.Legend)
		}
	}
	if v.status != nil {
		if s := v.status(atSec); s != "" {
			sb.WriteString("   " + s)
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
	order := make([]int, len(v.baskets))
	for i := range order {
		order[i] = i
	}
	if v.rank != nil {
		// Row order recomputes every render: baskets migrate as the story
		// develops. Keys are computed once per row (fresh RowCtx per rank
		// call would double store reads — reuse one per row is fine since
		// rank consumes only its own metric).
		type key struct {
			val float64
			ok  bool
		}
		keys := make([]key, len(v.baskets))
		for i := range v.baskets {
			val, ok := v.rank(&RowCtx{View: v, Basket: &v.baskets[i], AtSec: atSec})
			keys[i] = key{val, ok}
		}
		sort.SliceStable(order, func(a, b int) bool {
			ka, kb := keys[order[a]], keys[order[b]]
			if ka.ok != kb.ok {
				return ka.ok // defined keys before gaps
			}
			if ka.ok && ka.val != kb.val {
				return ka.val > kb.val // descending
			}
			return v.baskets[order[a]].Name < v.baskets[order[b]].Name
		})
	}
	for _, i := range order {
		b := &v.baskets[i]
		rc := &RowCtx{View: v, Basket: b, AtSec: atSec} // one ctx per row: cells share memoized reads
		fmt.Fprintf(&sb, "%-*s", nameW, b.Name)
		for _, c := range v.columns {
			cell := fmt.Sprintf("%*s", c.Width, c.Cell(rc))
			if c.Style != nil {
				if sgr := c.Style(rc); sgr != "" {
					cell = sgr + cell + "\x1b[0m"
				}
			}
			sb.WriteString("  " + cell)
		}
		sb.WriteByte('\n')
	}
	if v.footer != "" {
		sb.WriteByte('\n')
		sb.WriteString(v.footer)
	}
	return sb.String()
}

var _ ingest.Observer = (*View)(nil)
