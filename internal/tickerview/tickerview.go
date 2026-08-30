// Package tickerview is the explanation layer under the basket view
// (mini-spec docs/mini-specs/ticker-view-v0.md): one basket's members as
// rows under the basket line (drill-down) and, across all baskets, the
// crossings strip — tickers whose since-open dollars are beyond
// ±SignificantZ of their OWN 20-day matched-minute typical.
//
// Scope law, restated: every rendered string is a measurement of one ticker
// against its own history. No composite score, no ranked list whose key is
// not a named metric (V1: the drill-down sorts by cum_$_z; the strip is
// TIME-ordered — a strip sorted by z would be a screener), no
// price-change-as-signal, no FlowShare/breadth at ticker level (V3). A
// ticker row explains a basket row; it never competes with it. Per-ticker
// delta/class% (V5, revisited by MO-3) render with the D9 open window and
// the honesty number beside them — the same rules as the basket pair.
//
// Everything computes at read time from the 1-second bucket store, the
// per-ticker profiles (incl. the cum-dollar family) and the floors — no new
// stores. Renders are pure in (store state, second): the per-ticker
// history is memoized per render second, never across renders (late prints
// amend completed buckets).
package tickerview

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/delta"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/zguard"
)

const gap = "·"

// Threshold is the crossing threshold: trader-view T2's SignificantZ, by
// reference — the two panels can never disagree about "significant".
const Threshold = flowshare.SignificantZ

// StripCap is how many crossings the strip lists per sign before it
// collapses the rest into "+N more (broad)" (V2). Thirty names popping is a
// breadth statement, not a list.
const StripCap = 8

const (
	sgrGreen = "\x1b[1;32m"
	sgrRed   = "\x1b[1;31m"
)

// OptionsSource is the per-ticker options tape at read time (7.6 profiles
// + a minute reader). Nil means conv_z/net_z are not rendered at all.
type OptionsSource struct {
	// Minute returns the ticker's summed net_conviction and net_notional
	// for the minute starting at minuteSec; ok=false when the minute is
	// not measurable (unaligned, or the options tape has not reached its
	// end) — rendered as a gap, never a fabricated zero (MO-4).
	Minute   func(sym string, minuteSec int64) (netConv, netNotional float64, ok bool)
	Profiles map[string]*optprofile.Profile // per ticker; a missing symbol renders gaps
	Floors   *optprofile.Floors
}

// LoadOptions reads every symbol's 7.6 ticker profile and the floors
// (refusing a stale weights stamp) and binds a minute reader. A symbol
// with no profile file is tolerated as a permanent gap (the options
// universe may lag the equity one); a floors file is required.
func LoadOptions(dir string, symbols []string, stamp string, minute func(sym string, minuteSec int64) (netConv, netNotional float64, ok bool)) (*OptionsSource, error) {
	src := &OptionsSource{Minute: minute, Profiles: map[string]*optprofile.Profile{}}
	for _, sym := range symbols {
		p, err := optprofile.Read(dir, sym, stamp)
		if err != nil {
			continue
		}
		src.Profiles[sym] = p
	}
	fl, err := optprofile.ReadFloors(dir, stamp)
	if err != nil {
		return nil, err
	}
	src.Floors = fl
	return src, nil
}

// SessionMinutes adapts a read-back options bucket file (replay) to the
// OptionsSource minute reader; an unaligned minute is ok=false.
func SessionMinutes(sess *optbucket.Session) func(sym string, minuteSec int64) (float64, float64, bool) {
	return func(sym string, minuteSec int64) (float64, float64, bool) {
		b, err := sess.DeriveMinute(sym, minuteSec)
		if err != nil {
			return 0, 0, false
		}
		return b.NetConviction, b.SignedNotional(), true
	}
}

type basketRef struct {
	Name   string
	States []*ingest.SymbolState
}

// Calc holds the wiring and the per-render memo.
type Calc struct {
	store    *bucket.Store
	baskets  []basketRef // sorted by name
	byName   map[string]*basketRef
	union    []*ingest.SymbolState            // every distinct basket member, sorted
	inBasket map[*ingest.SymbolState][]string // sorted basket names holding the ticker
	profiles map[string]*profile.Profile
	floors   *profile.Floors
	opts     *OptionsSource

	memoAt int64
	memo   map[*ingest.SymbolState]*history
	frame  frame
}

// frame is the render second's session geometry.
type frame struct {
	openSec, closeSec int64
	cmEnd             int64 // end of the last completed minute, clamped to [open, close]
	n                 int   // completed session minutes
	ok                bool  // false when the date cannot be resolved
}

// history is one ticker's since-open series as of a render second.
type history struct {
	cum    []float64 // cumulative auction-inclusive dollars through completed minute i
	z      []float64 // cum_$_z at minute i (zok[i] false = defined null)
	zok    []bool
	crossS float64 // opening-cross shares/dollars over [open, this second]
	crossD float64
	minS   float64 // last completed minute, Counted slice
	minD   float64
	// MO-3 trailing delta window, computed lazily on first drill-down use
	// within the render second (the crossings strip never reads it) — one
	// store pass per ticker per second, never per row call.
	deltaOK bool
	delta   delta.Minute
}

// New resolves baskets against the symbol table; every member must have a
// per-ticker profile (the map devview.New already loaded — shared, not
// re-read). floors must be the same table the basket view uses.
func New(store *bucket.Store, table *ingest.Table, baskets []universe.Basket, profiles map[string]*profile.Profile, floors *profile.Floors, opts *OptionsSource) (*Calc, error) {
	c := &Calc{store: store, byName: map[string]*basketRef{}, inBasket: map[*ingest.SymbolState][]string{},
		profiles: profiles, floors: floors, opts: opts}
	seen := map[*ingest.SymbolState]bool{}
	for _, b := range baskets {
		ref := basketRef{Name: b.Name}
		for _, sym := range b.Members {
			st := table.Lookup(sym)
			if st == nil {
				return nil, fmt.Errorf("basket %s member %s not in symbol table", b.Name, sym)
			}
			if profiles[sym] == nil {
				return nil, fmt.Errorf("basket %s member %s has no profile", b.Name, sym)
			}
			ref.States = append(ref.States, st)
			c.inBasket[st] = append(c.inBasket[st], b.Name)
			if !seen[st] {
				seen[st] = true
				c.union = append(c.union, st)
			}
		}
		sort.Slice(ref.States, func(i, j int) bool { return ref.States[i].Symbol < ref.States[j].Symbol })
		c.baskets = append(c.baskets, ref)
	}
	sort.Slice(c.baskets, func(i, j int) bool { return c.baskets[i].Name < c.baskets[j].Name })
	for i := range c.baskets {
		c.byName[c.baskets[i].Name] = &c.baskets[i]
	}
	sort.Slice(c.union, func(i, j int) bool { return c.union[i].Symbol < c.union[j].Symbol })
	for _, names := range c.inBasket {
		sort.Strings(names)
	}
	return c, nil
}

// HasBasket reports whether name is a rendered basket.
func (c *Calc) HasBasket(name string) bool { return c.byName[name] != nil }

// prime computes every union member's since-open history for the render
// second — one store pass shared by the strip, the drill-down and the
// all-basket drill file within the same second.
func (c *Calc) prime(atSec int64) {
	if c.memoAt == atSec && c.memo != nil {
		return
	}
	c.memoAt, c.memo, c.frame = atSec, map[*ingest.SymbolState]*history{}, frame{}
	date := session.Date(atSec * 1e9)
	openSec, err1 := session.BucketStart(date, session.OpenMinute)
	closeSec, err2 := session.BucketStart(date, session.CloseMinute)
	if err1 != nil || err2 != nil {
		return
	}
	cmEnd := session.MinuteStart(atSec * 1e9)
	if cmEnd > closeSec {
		cmEnd = closeSec
	}
	if cmEnd < openSec {
		cmEnd = openSec
	}
	liveEnd := atSec + 1
	if liveEnd > closeSec {
		liveEnd = closeSec
	}
	n := int((cmEnd - openSec) / 60)
	c.frame = frame{openSec: openSec, closeSec: closeSec, cmEnd: cmEnd, n: n, ok: true}
	for _, st := range c.union {
		h := &history{cum: make([]float64, n), z: make([]float64, n), zok: make([]bool, n)}
		prof := c.profiles[st.Symbol]
		cum := 0.0
		for i := 0; i < n; i++ {
			from := openSec + int64(i)*60
			b := c.store.Window(st, from, from+60)
			_, ad := profile.CountedWithAuctions(b)
			cum += ad
			h.cum[i] = cum
			cs, cd := profile.OpeningCross(b)
			h.crossS += cs
			h.crossD += cd
			if i == n-1 {
				h.minS, h.minD = profile.Counted(b)
			}
			h.z[i], h.zok[i] = c.cumZ(prof, session.OpenMinute+i, cum)
		}
		if liveEnd > cmEnd {
			cs, cd := profile.OpeningCross(c.store.Window(st, cmEnd, liveEnd))
			h.crossS += cs
			h.crossD += cd
		}
		c.memo[st] = h
	}
}

// cumZ is the guarded cum-dollar z: gated on the family being present in
// both the profile and the floors, and on the ≥10-day rule; σ guard via
// zguard, the only z.
func (c *Calc) cumZ(prof *profile.Profile, minuteOfDay int, cum float64) (float64, bool) {
	if prof == nil || !prof.HasCumDollars || c.floors == nil || !c.floors.HasCumDollars {
		return 0, false
	}
	row, ok := prof.Minute(minuteOfDay)
	if !ok || row.Days < flowshare.MinProfiledDays {
		return 0, false
	}
	fr, ok := c.floors.Minute(minuteOfDay)
	if !ok {
		return 0, false
	}
	return zguard.Z(cum, row.MedianCumDollars, row.SigmaCumDollars, fr.SigmaFloorCumDollars)
}

// Row is one ticker's rendered measurements as of a render second. Every
// ok=false is a defined gap.
type Row struct {
	Symbol   string
	Last     float64
	LastOK   bool
	OpenPct  float64 // % from the opening-cross VWAP (V4: not prior close)
	OpenOK   bool
	Cum      float64 // auction-inclusive $ since open through the completed minute
	CumOK    bool
	Typ      float64
	TypOK    bool
	Z        float64
	ZOK      bool
	RVol     float64
	RVolOK   bool
	DollarZ  float64
	DollarOK bool
	OfBasket float64
	OfOK     bool
	Delta    float64 // MO-3: trailing 5 completed minutes, strong rules over counted dollars
	DeltaOK  bool
	DeltaSGR string  // MO-3: highlight prefix, "" when below ±DeltaHighlight or under the per-ticker floor
	Class    float64 // MO-3: share of those counted dollars the book could place
	ClassOK  bool
	ConvZ    float64
	ConvOK   bool
	NetZ     float64
	NetOK    bool
	Since    string // ET HH:MM cum_$_z first crossed ±Threshold today; "" if never
}

func (c *Calc) hhmm(minuteIdx int) string {
	return time.Unix(c.frame.openSec+int64(minuteIdx)*60, 0).In(session.ET()).Format("15:04")
}

// row computes one ticker's Row; basketCum is the basket's cum $ through
// the completed minute (0 renders of_basket as a gap).
func (c *Calc) row(st *ingest.SymbolState, atSec int64, basketCum float64) Row {
	r := Row{Symbol: st.Symbol}
	if !c.frame.ok {
		return r
	}
	h := c.memo[st]
	liveEnd := atSec + 1
	if liveEnd > c.frame.closeSec {
		liveEnd = c.frame.closeSec
	}
	if liveEnd > c.frame.openSec {
		r.Last, r.LastOK = c.store.LastTradePrice(st, c.frame.openSec, liveEnd)
	}
	if h.crossS > 0 && r.LastOK {
		vwap := h.crossD / h.crossS
		r.OpenPct, r.OpenOK = 100*(r.Last-vwap)/vwap, true
	}
	n := c.frame.n
	if n == 0 {
		return r
	}
	last := n - 1
	key := session.OpenMinute + last
	r.Cum, r.CumOK = h.cum[last], true
	prof := c.profiles[st.Symbol]
	if row, ok := prof.Minute(key); ok {
		if prof.HasCumDollars && row.Days > 0 {
			r.Typ, r.TypOK = row.MedianCumDollars, true
		}
		if row.MedianShares > 0 {
			r.RVol, r.RVolOK = h.minS/row.MedianShares, true
		}
		if row.Days >= flowshare.MinProfiledDays {
			if fr, ok := c.floors.Minute(key); ok {
				r.DollarZ, r.DollarOK = zguard.Z(h.minD, row.MedianDollars, row.SigmaDollars, fr.SigmaFloorDollars)
			}
		}
	}
	r.Z, r.ZOK = h.z[last], h.zok[last]
	if basketCum > 0 {
		r.OfBasket, r.OfOK = r.Cum/basketCum, true
	}
	for i := 0; i < n; i++ {
		if h.zok[i] && (h.z[i] >= Threshold || h.z[i] <= -Threshold) {
			r.Since = c.hhmm(i)
			break
		}
	}
	if c.opts != nil {
		if p := c.opts.Profiles[st.Symbol]; p != nil && c.opts.Floors != nil {
			i := key - session.OpenMinute
			if pr := p.Rows[i]; pr.Days >= optprofile.MinProfiledDays {
				if conv, net, ok := c.opts.Minute(st.Symbol, c.frame.cmEnd-60); ok {
					r.ConvZ, r.ConvOK = zguard.Z(conv, pr.MedianNetConv, pr.SigmaNetConv, c.opts.Floors.NetConv[i])
					r.NetZ, r.NetOK = zguard.Z(net, pr.MedianNetNotional, pr.SigmaNetNotional, c.opts.Floors.NetNotional[i])
				}
			}
		}
	}
	return r
}

// tickerDelta is the ticker's MO-3 trailing window for the render second,
// memoized in its history: the drill-down reads it, the strip does not.
func (c *Calc) tickerDelta(st *ingest.SymbolState, atSec int64) delta.Minute {
	h := c.memo[st]
	if h == nil || !c.frame.ok || c.frame.n == 0 {
		return delta.Minute{}
	}
	if !h.deltaOK {
		h.delta, h.deltaOK = delta.Trailing(c.store, []*ingest.SymbolState{st}, atSec), true
	}
	return h.delta
}

// Rows returns a basket's member rows sorted per V1/T1: cum_$_z
// descending, gaps last, ties by symbol. Nil for an unknown basket.
func (c *Calc) Rows(basket string, atSec int64) []Row {
	ref := c.byName[basket]
	if ref == nil {
		return nil
	}
	c.prime(atSec)
	var basketCum float64
	if c.frame.ok && c.frame.n > 0 {
		for _, st := range ref.States {
			basketCum += c.memo[st].cum[c.frame.n-1]
		}
	}
	rows := make([]Row, 0, len(ref.States))
	for _, st := range ref.States {
		r := c.row(st, atSec, basketCum)
		dm := c.tickerDelta(st, atSec)
		r.Delta, r.DeltaOK, r.Class, r.ClassOK = dm.Delta, dm.DeltaOK, dm.Class, dm.ClassOK
		r.DeltaSGR = delta.TickerStyle(dm)
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := rows[a], rows[b]
		if ra.ZOK != rb.ZOK {
			return ra.ZOK
		}
		if ra.ZOK && ra.Z != rb.Z {
			return ra.Z > rb.Z
		}
		return ra.Symbol < rb.Symbol
	})
	return rows
}

func fz(z float64, ok bool) string {
	if !ok {
		return gap
	}
	return fmt.Sprintf("%+.1f", z)
}

func fusd(v float64, ok bool) string {
	if !ok {
		return gap
	}
	return devview.FmtDollars(v)
}

func styleZ(z float64, ok bool) string {
	switch {
	case !ok || (z < Threshold && z > -Threshold):
		return ""
	case z > 0:
		return sgrGreen
	default:
		return sgrRed
	}
}

// Detail renders one basket's drill-down: a header and one row per member,
// indented under the basket line. Empty for an unknown basket. conv_z/net_z
// columns appear only when an OptionsSource is wired.
func (c *Calc) Detail(basket string, atSec int64) string {
	rows := c.Rows(basket, atSec)
	if rows == nil {
		return ""
	}
	var sb strings.Builder
	const ind = "    "
	fmt.Fprintf(&sb, "%s%-6s  %8s  %7s  %8s  %9s  %7s  %7s  %6s  %9s  %6s  %6s", ind, "TICKER", "last", "open%", "cum_$", "cum_$_typ", "cum_$_z", "rvol_sh", "$_z", "of_basket", "delta", "class%")
	if c.opts != nil {
		fmt.Fprintf(&sb, "  %6s  %6s", "conv_z", "net_z")
	}
	fmt.Fprintf(&sb, "  %5s\n", "since")
	for _, r := range rows {
		last, open, rvol, of := gap, gap, gap, gap
		if r.LastOK {
			last = fmt.Sprintf("%.2f", r.Last)
		}
		if r.OpenOK {
			open = fmt.Sprintf("%+.2f%%", r.OpenPct)
		}
		if r.RVolOK {
			rvol = fmt.Sprintf("%.2f", r.RVol)
		}
		if r.OfOK {
			of = fmt.Sprintf("%.0f%%", 100*r.OfBasket)
		}
		zcell := fmt.Sprintf("%7s", fz(r.Z, r.ZOK))
		if sgr := styleZ(r.Z, r.ZOK); sgr != "" {
			zcell = sgr + zcell + "\x1b[0m"
		}
		dcell := fmt.Sprintf("%6s", delta.FmtDelta(r.Delta, r.DeltaOK))
		if r.DeltaSGR != "" {
			dcell = r.DeltaSGR + dcell + "\x1b[0m"
		}
		fmt.Fprintf(&sb, "%s%-6s  %8s  %7s  %8s  %9s  %s  %7s  %6s  %9s  %s  %6s", ind, r.Symbol, last, open,
			fusd(r.Cum, r.CumOK), fusd(r.Typ, r.TypOK), zcell, rvol, fz(r.DollarZ, r.DollarOK), of, dcell, delta.FmtClass(r.Class, r.ClassOK))
		if c.opts != nil {
			fmt.Fprintf(&sb, "  %6s  %6s", fz(r.ConvZ, r.ConvOK), fz(r.NetZ, r.NetOK))
		}
		fmt.Fprintf(&sb, "  %5s\n", r.Since)
	}
	return sb.String()
}

// AllBaskets is the -basket / ?basket= value that expands every basket.
const AllBaskets = "all"

// DetailFor returns a devview.SetDetail function that expands exactly one
// basket, or every basket when basket == AllBaskets.
func (c *Calc) DetailFor(basket string) func(string, int64) string {
	return func(name string, atSec int64) string {
		if basket != AllBaskets && name != basket {
			return ""
		}
		return c.Detail(name, atSec)
	}
}

// DrillWriter rewrites the all-basket drill file (RenderAll) on an
// event-time cadence: tmp + rename, so the frame server never reads a torn
// file. Failures are reported to stderr and never stop the session — the
// drill file is derived, the capture/replay is the record. Nil-safe.
type DrillWriter struct {
	Calc  *Calc
	Path  string
	Every int64 // seconds of event time between rewrites
	last  int64
}

// MaybeWrite rewrites the file if Every seconds have passed since the last
// write (or on the first call).
func (d *DrillWriter) MaybeWrite(sec int64) {
	if d == nil || (d.last != 0 && sec-d.last < d.Every) {
		return
	}
	d.last = sec
	tmp := d.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(d.Calc.RenderAll(sec)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "drill write failed: %v\n", err)
		return
	}
	if err := os.Rename(tmp, d.Path); err != nil {
		fmt.Fprintf(os.Stderr, "drill write failed: %v\n", err)
	}
}

// crossing is one strip entry: a ticker currently beyond the threshold and
// the minute its current run began.
type crossing struct {
	sym     string
	minute  int // index from open
	z       float64
	cum     float64
	typ     float64
	typOK   bool
	baskets string
	convZ   float64
	convOK  bool
}

// crossings lists the union members whose completed-minute cum_$_z is at
// or beyond the threshold, per sign, newest run start first (ties by
// symbol). A ticker whose |z| fell back is not listed — the drill-down's
// `since` keeps the record.
func (c *Calc) crossings(atSec int64) (pos, neg []crossing) {
	c.prime(atSec)
	if !c.frame.ok || c.frame.n == 0 {
		return nil, nil
	}
	last := c.frame.n - 1
	for _, st := range c.union {
		h := c.memo[st]
		if !h.zok[last] || (h.z[last] < Threshold && h.z[last] > -Threshold) {
			continue
		}
		positive := h.z[last] > 0
		start := last
		for start > 0 && h.zok[start-1] && ((positive && h.z[start-1] >= Threshold) || (!positive && h.z[start-1] <= -Threshold)) {
			start--
		}
		r := c.row(st, atSec, 0)
		x := crossing{sym: st.Symbol, minute: start, z: h.z[last], cum: h.cum[last], typ: r.Typ, typOK: r.TypOK,
			baskets: strings.Join(c.inBasket[st], ","), convZ: r.ConvZ, convOK: r.ConvOK}
		if positive {
			pos = append(pos, x)
		} else {
			neg = append(neg, x)
		}
	}
	order := func(xs []crossing) {
		sort.SliceStable(xs, func(a, b int) bool {
			if xs[a].minute != xs[b].minute {
				return xs[a].minute > xs[b].minute // newest first — time order, never z order
			}
			return xs[a].sym < xs[b].sym
		})
	}
	order(pos)
	order(neg)
	return pos, neg
}

// Strip renders the crossings strip (spec §2): one block per sign, newest
// crossing first, capped at StripCap with "+N more (broad)". A tape with
// nothing beyond the threshold says so in one line.
func (c *Calc) Strip(atSec int64) string {
	pos, neg := c.crossings(atSec)
	if len(pos) == 0 && len(neg) == 0 {
		return fmt.Sprintf("crossed ±%.1fσ  %s\n", Threshold, gap)
	}
	var sb strings.Builder
	block := func(label string, xs []crossing, sgr string) {
		if len(xs) == 0 {
			return
		}
		shown := xs
		if len(shown) > StripCap {
			shown = shown[:StripCap]
		}
		bw := 0
		for _, x := range shown {
			if len(x.baskets) > bw {
				bw = len(x.baskets)
			}
		}
		pad := strings.Repeat(" ", utf8.RuneCountInString(label))
		for i, x := range shown {
			lead := pad
			if i == 0 {
				lead = label
			}
			typ := gap
			if x.typOK {
				typ = devview.FmtDollars(x.typ)
			}
			conv := "—"
			if x.convOK {
				conv = fmt.Sprintf("%+.1f", x.convZ)
			}
			z := fmt.Sprintf("%+.1fσ", x.z)
			fmt.Fprintf(&sb, "%s  %s %-5s  %s%s\x1b[0m  cum_$ %s (typ %s)  %-*s  conv_z %s\n",
				lead, c.hhmm(x.minute), x.sym, sgr, z, devview.FmtDollars(x.cum), typ, bw, x.baskets, conv)
		}
		if more := len(xs) - len(shown); more > 0 {
			fmt.Fprintf(&sb, "%s  +%d more (broad)\n", pad, more)
		}
	}
	block(fmt.Sprintf("crossed +%.1fσ", Threshold), pos, sgrGreen)
	block(fmt.Sprintf("crossed −%.1fσ", Threshold), neg, sgrRed)
	return sb.String()
}

// RenderAll renders every basket's drill-down, each under a "## basket
// <name>" marker line — the frame server's drill file (live: written by
// cmd/live -view -drill; the server is a read-only tail and never touches
// the capture process).
func (c *Calc) RenderAll(atSec int64) string {
	var sb strings.Builder
	for _, b := range c.baskets {
		fmt.Fprintf(&sb, "## basket %s\n", b.Name)
		sb.WriteString(c.Detail(b.Name, atSec))
	}
	return sb.String()
}

// Footer is the ticker-level legend — statements of measurement only —
// appended to the trader footer when the ticker view is wired.
const Footer = `--- ticker rows (drill-down) and the "crossed" strip: each number is ONE ticker against its OWN last-20-session history at this minute of day ---
last              = last trade price on the tape (raw prints, to this second)
open%             = % from the opening auction cross (cross VWAP = dollars/shares of the opening-cross prints); blank until the cross prints; NOT vs prior close (not in the store — deferred)
cum_$             = dollars traded since the open through the last completed minute, including the opening auction
cum_$_typ         = what cum_$ typically is by this minute for this ticker, median of the last 20 sessions
cum_$_z           = how unusual today's cum_$ is vs those 20 sessions, in σ — ±2 highlighted (green above typical, red below); a measurement of dollars, not of direction
rvol_sh           = last full minute's shares (not dollars — the basket columns are dollars) vs the typical for that exact minute — 1.0 = normal pace (excludes auction crosses; cum_$ includes them — two slices on one screen, deliberately)
$_z               = last full minute's dollars vs the typical for that exact minute, in σ (excludes crosses)
of_basket         = this ticker's share of its basket's cum_$ — the concentration number from the ticker's side
delta / class%    = same as the basket pair, for this ticker alone: trailing 5 completed minutes' dollars printed at/above the ask or above the midpoint minus at/below the bid or below the midpoint, over all its counted dollars, and the % of those dollars the book could place; bold at/beyond ±0.20, not highlighted under $500k / 20 placed prints in the window; · when the day's store carries no classification or nothing counted; 09:30's first 30 s excluded; from 09:33 (through 09:35 the window is shorter than 5 minutes)
conv_z / net_z    = last completed minute's options premium (conviction-weighted / unweighted; ask-side minus bid-side, calls positive puts negative) vs this ticker's own 20d matched-minute median/MAD; · until 10 profiled days (options profile gate) or when no options tape is wired
since             = the ET minute cum_$_z first went beyond ±2.0σ today; blank if never
crossed ±2.0σ     = tickers across all baskets whose cum_$_z is beyond ±2.0σ right now, newest crossing first (time order, not size order); at most 8 listed per sign — "+N more (broad)" means the tape is broad, which the breadth column already says; a name drops off when its |z| falls back
`
