package flowshare

// MO-8 run metrics (mini-spec docs/mini-specs/metric-overload/MO-8-run-metrics.md):
// the two share-side run columns, `since` and `5m_z`, computed from the
// same per-minute atoms the cum and per-minute z cells read.
//
//	since = first ET minute today at which |cum_share_z| ≥ SignificantZ; blank if never
//	5m_z  = mean of flow_share_z over the last RunWindow completed minutes
//
// cum_share_z is a level: it integrates from the open, so a 09:35 burst
// holds a basket at +2.5σ at 10:15 after flow normalised. `since` says
// when the level was first reached (the record — a basket that crossed
// and fell back keeps it, as the ticker view's `since` does); `5m_z` says
// whether the last five minutes are still above typical. 5m_z is a mean
// of z's (R1): each minute's flow_share_z is already matched-bucket and
// σ-guarded, so averaging needs no new baseline and never commits the
// medians-don't-commute sin. It is 3.7's ShiftSlope INPUT, not the
// detector (R2): no CUSUM, no slope, no sentence here; display keeps full
// membership (A6(e)).
//
// Series posture: every render rebuilds the completed-minute series from
// the open (late prints amend completed buckets, so nothing is memoized
// across renders — the winSnap rule). The cumulative series accumulates
// per-minute window sums; the cum_share_z cell reads one whole-window
// sum. Both are sums of the same buckets in time order and differ by at
// most floating-point association (≈1e-16 relative) — far below the
// rendered digit, and never a different basis.

import (
	"fmt"
	"strings"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// RunWindow is the trader window for 5m_z: the last five completed
// session minutes, every one of which must carry a defined flow_share_z
// (a gap inside the window gaps the mean — never a mean over fewer). A
// code constant tuned at the nightly ledger review, not config.
const RunWindow = 5

// RunWindowLong is the 15-minute variant §2.4 also names; dev view only
// (15m_z), for comparison at the ledger.
const RunWindowLong = 15

// Run is the per-render minute-series calc behind since / 5m_z / 15m_z.
// One per process: the memo is per render second, on the single render
// goroutine (the render loop, then the final render — never concurrently).
type Run struct {
	store  *bucket.Store
	union  []*ingest.SymbolState
	shares map[string]*profile.ShareProfile
	floors *profile.Floors

	memoAt  int64
	series  *minuteSeries
	baskets map[*devview.BasketRow]basketSeries
}

// minuteSeries is the union's completed-minute history as of a render
// second: per member per minute, counted dollars (the per-minute family's
// slice) and auction-inclusive dollars (the cumulative family's slice),
// plus the union totals each share divides by.
type minuteSeries struct {
	openSec int64
	n       int // completed session minutes, clamped at the close
	minD    map[*ingest.SymbolState][]float64
	cumD    map[*ingest.SymbolState][]float64
	uniMin  []float64 // Σ union counted dollars, minute i
	uniCum  []float64 // Σ union auction-inclusive dollars, open through minute i
}

// basketSeries is one basket's z series over the completed minutes.
type basketSeries struct {
	z, cumZ     []float64
	zOK, cumZOK []bool
}

// NewRun binds the store, the D1 union, and the share baselines (the same
// maps TraderColumns takes).
func NewRun(store *bucket.Store, union []*ingest.SymbolState, shares map[string]*profile.ShareProfile, floors *profile.Floors) *Run {
	return &Run{store: store, union: union, shares: shares, floors: floors}
}

// prime rebuilds the union series for the render second: open through the
// end of the last completed minute, clamped at the close (post-close
// renders keep the whole day, the closing cross stays out — the cumWindow
// deferral). n = 0 before the first completed session minute.
func (r *Run) prime(atSec int64) *minuteSeries {
	if r.memoAt == atSec && r.series != nil {
		return r.series
	}
	r.memoAt, r.series, r.baskets = atSec, &minuteSeries{}, map[*devview.BasketRow]basketSeries{}
	s := r.series
	date := session.Date(atSec * 1e9)
	openSec, err1 := session.BucketStart(date, session.OpenMinute)
	closeSec, err2 := session.BucketStart(date, session.CloseMinute)
	if err1 != nil || err2 != nil {
		return s
	}
	cmEnd := session.MinuteStart(atSec * 1e9)
	if cmEnd > closeSec {
		cmEnd = closeSec
	}
	if cmEnd < openSec {
		cmEnd = openSec
	}
	n := int((cmEnd - openSec) / 60)
	s.openSec, s.n = openSec, n
	s.minD = make(map[*ingest.SymbolState][]float64, len(r.union))
	s.cumD = make(map[*ingest.SymbolState][]float64, len(r.union))
	s.uniMin, s.uniCum = make([]float64, n), make([]float64, n)
	// Sums run in union order per minute, member cumulatives in time
	// order — the same orders the cell snapshots use, so the same store
	// state yields the same bytes on every render.
	for _, st := range r.union {
		md, cd := make([]float64, n), make([]float64, n)
		cum := 0.0
		for i := 0; i < n; i++ {
			from := openSec + int64(i)*60
			b := r.store.Window(st, from, from+60)
			_, d := profile.Counted(b)
			_, ad := profile.CountedWithAuctions(b)
			md[i] = float64(d)
			cum += float64(ad)
			cd[i] = cum
			s.uniMin[i] += float64(d)
			s.uniCum[i] += cum
		}
		s.minD[st], s.cumD[st] = md, cd
	}
	return s
}

// basket computes (once per basket per render) the basket's per-minute
// flow_share_z and cum_share_z series — each minute through ShareZ /
// CumShareZ, the same gates and guard as the cells (a 0/0 minute, the
// 10-day gate, and zguard's null all read as ok=false).
func (r *Run) basket(rc *devview.RowCtx) (*minuteSeries, basketSeries) {
	s := r.prime(rc.AtSec)
	if bs, ok := r.baskets[rc.Basket]; ok {
		return s, bs
	}
	n := s.n
	bs := basketSeries{z: make([]float64, n), cumZ: make([]float64, n), zOK: make([]bool, n), cumZOK: make([]bool, n)}
	prof := r.shares[rc.Basket.Name]
	if prof != nil {
		for i := 0; i < n; i++ {
			var bd, bc float64
			for _, st := range rc.Basket.States {
				bd += s.minD[st][i]
				bc += s.cumD[st][i]
			}
			key := session.OpenMinute + i
			if s.uniMin[i] != 0 {
				bs.z[i], bs.zOK[i] = ShareZ(bd/s.uniMin[i], true, prof, r.floors, key)
			}
			if s.uniCum[i] != 0 {
				bs.cumZ[i], bs.cumZOK[i] = CumShareZ(bc/s.uniCum[i], true, prof, r.floors, key)
			}
		}
	}
	r.baskets[rc.Basket] = bs
	return s, bs
}

// WindowZ is the mean of the basket's flow_share_z over the last window
// completed session minutes. ok=false before window completed minutes,
// when any minute in the window has no defined z, or when the basket has
// no share baseline. The sum runs in minute order with explicit float64
// conversions (determinism).
func (r *Run) WindowZ(rc *devview.RowCtx, window int) (float64, bool) {
	s, bs := r.basket(rc)
	if window <= 0 || s.n < window {
		return 0, false
	}
	var sum float64
	for i := s.n - window; i < s.n; i++ {
		if !bs.zOK[i] {
			return 0, false
		}
		sum += float64(bs.z[i])
	}
	return sum / float64(window), true
}

// FiveMinuteZ is the trader cell: WindowZ over RunWindow.
func (r *Run) FiveMinuteZ(rc *devview.RowCtx) (float64, bool) { return r.WindowZ(rc, RunWindow) }

// Since returns the ET HH:MM of the first completed minute today whose
// |cum_share_z| ≥ SignificantZ — the record: a basket that crossed and
// fell back keeps it, and a negative crossing counts (the sign is on the
// z beside it). crossed=false with measured=true means every minute so
// far had a defined z below the threshold; measured=false means no
// minute had a defined cum_share_z at all (pre-open, no baseline).
func (r *Run) Since(rc *devview.RowCtx) (hhmm string, crossed, measured bool) {
	s, bs := r.basket(rc)
	for i := 0; i < s.n; i++ {
		if !bs.cumZOK[i] {
			continue
		}
		measured = true
		if bs.cumZ[i] >= SignificantZ || bs.cumZ[i] <= -SignificantZ {
			return time.Unix(s.openSec+int64(i)*60, 0).In(session.ET()).Format("15:04"), true, true
		}
	}
	return "", false, measured
}

// RunFlag is the MO-6 `R` glyph predicate: lit when |5m_z| ≥ SignificantZ,
// positive by sign, ok=false on a gap — exactly when the cell is coloured
// (G1: cell and glyph share one predicate).
func (r *Run) RunFlag(rc *devview.RowCtx) (lit, positive, ok bool) {
	z, ok := r.FiveMinuteZ(rc)
	if !ok {
		return false, false, false
	}
	return z >= SignificantZ || z <= -SignificantZ, z > 0, true
}

// runStyle is the T2 colour posture on 5m_z (R5): bold green at/beyond
// +SignificantZ, bold red at/beyond −SignificantZ, nothing on a gap.
func (r *Run) runStyle(rc *devview.RowCtx) string {
	lit, positive, ok := r.RunFlag(rc)
	switch {
	case !ok || !lit:
		return ""
	case positive:
		return sgrGreen
	default:
		return sgrRed
	}
}

func fmtZ(z float64, ok bool) string {
	if !ok {
		return gap
	}
	return fmt.Sprintf("%+.1f", z)
}

// sinceCol renders `since`: the HH:MM record, blank when every measured
// minute stayed inside ±SignificantZ (a measurement: no crossing yet),
// the gap glyph when no minute had a defined cum_share_z (no basis — the
// ticker view renders both as blank; the basket table distinguishes them
// so a blank never reads as a hole in a gapped row).
func (r *Run) sinceCol() devview.Column {
	return devview.Column{Name: "since", Width: 5, Cell: func(rc *devview.RowCtx) string {
		hhmm, _, measured := r.Since(rc)
		if !measured {
			return gap
		}
		return hhmm
	}}
}

func (r *Run) windowCol(name string, window int, styled bool) devview.Column {
	col := devview.Column{Name: name, Width: 5, Cell: func(rc *devview.RowCtx) string {
		return fmtZ(r.WindowZ(rc, window))
	}}
	if styled {
		col.Style = r.runStyle
	}
	return col
}

// Columns is the trader pair: since then 5m_z (coloured).
func (r *Run) Columns() []devview.Column {
	return []devview.Column{r.sinceCol(), r.windowCol("5m_z", RunWindow, true)}
}

// DevColumns is the dev-view set: since, 5m_z (unstyled like the dev
// view's other columns) and the 15-minute variant for comparison.
func (r *Run) DevColumns() []devview.Column {
	cols := []devview.Column{r.sinceCol(), r.windowCol("5m_z", RunWindow, false), r.windowCol("15m_z", RunWindowLong, false)}
	cols[0].Legend = "since = first completed minute |cum_share_z| ≥ 2.0; 5m_z/15m_z = mean flow_share_z over the last 5/15 completed minutes"
	return cols
}

// RunFooter is the trader legend for the pair plus the reading guide the
// three run columns (vs_SPY is relperf's, inserted after this block) form
// as a row sentence. Statements of measurement only; the threshold text
// is built from the shared constants so the legend can never drift from
// the colour rule. Goes through the scanner test.
var RunFooter = fmt.Sprintf(`since             = the ET minute cum_share_z first went beyond ±%.1fσ today (kept as the record even if it has since fallen back; a −%.1fσ crossing counts too — the sign is on the z beside it); blank = every completed minute so far stayed inside ±%.1fσ
5m_z              = the average of the last %d completed minutes' one-minute share z (each minute's share of universe dollars vs its own 20-day typical for that exact minute) — cum_share_z says how far today is from typical since the open, 5m_z says whether the last %d minutes are still there; bold at/beyond ±%.1fσ; · until %d completed minutes or when any of the %d has no baseline
`, SignificantZ, SignificantZ, SignificantZ, RunWindow, RunWindow, SignificantZ, RunWindow, RunWindow)

// ExtendTrader inserts the pair after cum_share_z (README column order:
// cum_share_z since 5m_z vs_SPY breadth …) and the footer block before the
// gap-glyph line. Both anchors are this package's own trader set; a miss
// is a wiring error and panics at composition — never a silent
// mis-ordered append (delta's posture).
func (r *Run) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	pair := r.Columns()
	at := -1
	for i, col := range cols {
		if col.Name == "cum_share_z" {
			at = i + 1
			break
		}
	}
	if at < 0 {
		panic("flowshare.Run.ExtendTrader: no cum_share_z column to anchor since/5m_z after")
	}
	out := make([]devview.Column, 0, len(cols)+len(pair))
	out = append(out, cols[:at]...)
	out = append(out, pair...)
	out = append(out, cols[at:]...)
	i := strings.Index(footer, "\n·  ")
	if i < 0 {
		panic("flowshare.Run.ExtendTrader: trader footer has no gap-glyph line to anchor the run block before")
	}
	return out, footer[:i+1] + RunFooter + footer[i+1:]
}
