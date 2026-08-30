// Package bucket is the 1.4 bucket store (mini-spec:
// docs/mini-specs/1.4-bucket-store.md): per-ticker, per-1-second aggregation
// of every processed print and quote, RAM-resident for the whole session,
// persisted as one CSV per session.
//
// One resolution — 1 second, all day; coarser views are derived by the
// reader (DeriveMinute), never stored. Buckets key on SIP timestamp (event
// time), so live, instant replay, and paced replay produce identical stores.
// Late prints amend their true bucket in RAM — no close watermark, no seam.
// The store is derived data: the capture is the durable record, and crash
// recovery is replay-and-regenerate, not file recovery.
//
// This package is the first pipeline consumer of the 0.3 print-inclusion
// policy: every trade is classified and its bucket carries per-class splits,
// so each downstream metric selects its counting slice without re-deriving
// policy. Top-level totals include every class — storage records the tape;
// metrics select.
package bucket

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"buddy-flow/internal/aggressor"
	"buddy-flow/internal/classify"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

// NumClasses is the number of 0.3 classes (classify.Class values are dense
// from Unknown=0 to Continuous).
const NumClasses = int(classify.Continuous) + 1

// ClassAgg is the per-class slice of a bucket.
type ClassAgg struct {
	Trades  int64
	Shares  float64
	Dollars float64
}

// Bucket is one (ticker, second) aggregate. Top-level fields sum every class
// — including DUPLICATE/NON_FLOW, which no metric may count; that selection
// is the reader's job via the per-class fields. LastPrice/LastSipTs are the
// raw tape's last print in the bucket (by SIP ts, arrival order breaking
// ties), unfiltered by class — a price-forming "last" is a Phase 3 concern.
//
// Signed volume (MO-2 / story 3.3) lives behind the Signed pointer: nil
// means NOT RECORDED — a store fed by a source that is not time-ordered
// (flat-file replay), or a bucket file written before MO-2. Absence
// propagates through add/Window/DeriveMinute, so no consumer can sum an
// unrecorded day as zero (review S7); consumers must nil-check, and a
// derived Unclassified reports ok=false. These sums are stored, not
// recomputed, because the book they were classified against is gone by
// read time (1.4 "store what cannot be derived").
type Bucket struct {
	Trades    int64
	Shares    float64
	Dollars   float64
	LastPrice float64
	LastSipTs int64
	Quotes    int64
	Class     [NumClasses]ClassAgg
	Signed    *SignedAgg
}

// SignedAgg is the aggressor cascade's output for one bucket. AskSide /
// BidSide are the totals over every rule; QuoteAsk / QuoteBid the subset
// decided by the quote or midpoint rule (the strong classifiers), so the
// tick-rule share is total − quote. TickRule counts eligible prints that
// reached the tick rule whichever way they landed (the F2 data-quality
// number); Late counts eligible prints skipped as late. Unclassified is
// derived (Eligible − AskSide − BidSide), never stored.
type SignedAgg struct {
	AskSide  ClassAgg
	BidSide  ClassAgg
	QuoteAsk ClassAgg
	QuoteBid ClassAgg
	TickRule int64
	Late     int64
}

func (s *SignedAgg) add(o *SignedAgg) {
	s.AskSide.add(&o.AskSide)
	s.BidSide.add(&o.BidSide)
	s.QuoteAsk.add(&o.QuoteAsk)
	s.QuoteBid.add(&o.QuoteBid)
	s.TickRule += o.TickRule
	s.Late += o.Late
}

// add sums o into b. Signed sums only when BOTH sides carry it: an
// accumulator that was started without Signed (a store or session that
// did not record it) stays nil however many recorded buckets are added,
// and vice versa a recorded accumulator ignores nothing — o.Signed is
// never nil when b.Signed is non-nil within one store/session, since
// recording is a per-source property, not per-bucket.
func (b *Bucket) add(o *Bucket) {
	b.Trades += o.Trades
	b.Shares += o.Shares
	b.Dollars += o.Dollars
	b.Quotes += o.Quotes
	if o.LastSipTs >= b.LastSipTs {
		b.LastPrice, b.LastSipTs = o.LastPrice, o.LastSipTs
	}
	for i := range b.Class {
		b.Class[i].add(&o.Class[i])
	}
	if b.Signed != nil && o.Signed != nil {
		b.Signed.add(o.Signed)
	}
}

func (c *ClassAgg) add(o *ClassAgg) {
	c.Trades += o.Trades
	c.Shares += o.Shares
	c.Dollars += o.Dollars
}

// Eligible is the aggressor cascade's denominator: every print whose class
// enters the cascade (aggressor.Eligible — CONTINUOUS only), regardless of
// how it landed. BLOCK prints are not here: they execute outside the quote
// (print-inclusion.md) and are never signed, though their dollars remain in
// any downstream Counted denominator that includes BLOCK.
func (b *Bucket) Eligible() ClassAgg {
	var out ClassAgg
	for c := range b.Class {
		if aggressor.Eligible(classify.Class(c)) {
			out.add(&b.Class[c])
		}
	}
	return out
}

// Unclassified is derived at read time, never stored: Eligible − AskSide −
// BidSide (late prints and invalid-book prints included — denominator only,
// the D15 posture). ok=false when signed volume was not recorded for this
// bucket — render a gap, never a zero.
func (b *Bucket) Unclassified() (ClassAgg, bool) {
	if b.Signed == nil {
		return ClassAgg{}, false
	}
	e := b.Eligible()
	return ClassAgg{
		Trades:  e.Trades - b.Signed.AskSide.Trades - b.Signed.BidSide.Trades,
		Shares:  e.Shares - b.Signed.AskSide.Shares - b.Signed.BidSide.Shares,
		Dollars: e.Dollars - b.Signed.AskSide.Dollars - b.Signed.BidSide.Dollars,
	}, true
}

// Store implements ingest.Observer. The single pipeline goroutine writes;
// concurrent readers (snapshots, the future dev view) take the mutex. Sparse
// by construction: a (ticker, second) entry exists only if a message hit it.
type Store struct {
	mu      sync.Mutex
	buckets map[*ingest.SymbolState]map[int64]*Bucket

	// timeOrdered: the feeding source delivers trades and quotes in one
	// time-ordered stream (live websocket, capture replay), so the NBBO at
	// ObserveTrade is the book as of arrival and the aggressor cascade may
	// run. False for flat-file replays (ticker-sorted files streamed
	// concurrently — the book at classification time would be
	// scheduler-dependent): no classification, no Signed on any bucket, no
	// signed columns in the file (review B1).
	timeOrdered bool
	// Per-symbol aggressor state (tick reference). Written only from the
	// pipeline goroutine under mu; see aggressor.State for why it lives here
	// and not in SymbolState.
	agg map[*ingest.SymbolState]*aggressor.State

	// Unknown-condition tripwire (0.3: quarantined + tripwired). Guarded by
	// mu — the slow path only.
	unknownPrints int64
	unknownIDs    map[int32]int64

	// Amendment ring (MO-8 review): every trade whose second is behind the
	// latest second seen amends a bucket an incremental reader may already
	// have summed. Each such write takes the next generation number and
	// records its second in the ring; AmendedSince lets a reader learn the
	// earliest amended second since the generation it last consumed, or
	// that the ring has wrapped past it (full rebuild). Only trade writes
	// matter — quotes carry no dollars. Guarded by mu.
	maxSeenSec int64
	amendGen   uint64
	amendRing  [AmendRing]int64
}

// AmendRing is the amendment ring's capacity: generations older than the
// current minus AmendRing are gone and AmendedSince reports ok=false. Late
// prints run ~1% of the tape (MO-2), so a once-a-second reader sees tens
// of amendments per render at the open — 4096 keeps a wide margin.
const AmendRing = 4096

// NewStore returns an empty store for a source that is NOT time-ordered
// (flat-file replays, unit fixtures): no signed volume is recorded. Live and
// capture-replay callers use NewTimeOrderedStore.
func NewStore() *Store {
	return &Store{
		buckets:    map[*ingest.SymbolState]map[int64]*Bucket{},
		agg:        map[*ingest.SymbolState]*aggressor.State{},
		unknownIDs: map[int32]int64{},
	}
}

// NewTimeOrderedStore returns an empty store for a time-ordered source
// (live websocket, capture replay): every bucket records signed volume and
// the file carries the signed columns.
func NewTimeOrderedStore() *Store {
	s := NewStore()
	s.timeOrdered = true
	return s
}

// TimeOrdered reports whether this store records signed volume.
func (s *Store) TimeOrdered() bool { return s.timeOrdered }

func (s *Store) aggFor(st *ingest.SymbolState) *aggressor.State {
	a := s.agg[st]
	if a == nil {
		a = &aggressor.State{}
		s.agg[st] = a
	}
	return a
}

func (s *Store) bucketFor(st *ingest.SymbolState, sipNs int64) *Bucket {
	sec := sipNs / 1e9
	m := s.buckets[st]
	if m == nil {
		m = map[int64]*Bucket{}
		s.buckets[st] = m
	}
	b := m[sec]
	if b == nil {
		b = &Bucket{}
		if s.timeOrdered {
			b.Signed = &SignedAgg{}
		}
		m[sec] = b
	}
	return b
}

// ObserveTrade aggregates one print into its SIP-second bucket and, for
// eligible prints, classifies it against the book as of arrival (the
// pipeline applied every earlier quote before calling here).
func (s *Store) ObserveTrade(t *ingest.Trade) {
	class, unknown := classify.Classify(t.Cond[:t.NCond])
	// The explicit conversion forces the product to round to float64 before
	// the += below. Without it Go may fuse multiply+add into an FMA on arm64,
	// making bucket files differ across architectures — replay determinism is
	// per-capture, not per-CPU.
	dollars := float64(t.Price * t.Size)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(unknown) > 0 {
		s.unknownPrints++
		for _, id := range unknown {
			s.unknownIDs[id]++
		}
	}
	sec := t.SipTs / 1e9
	switch {
	case sec > s.maxSeenSec:
		s.maxSeenSec = sec
	case sec < s.maxSeenSec:
		s.amendGen++
		s.amendRing[s.amendGen%AmendRing] = sec
	}
	b := s.bucketFor(t.State, t.SipTs)
	b.Trades++
	b.Shares += t.Size
	b.Dollars += dollars
	if t.SipTs >= b.LastSipTs {
		b.LastPrice, b.LastSipTs = t.Price, t.SipTs
	}
	c := &b.Class[class]
	c.Trades++
	c.Shares += t.Size
	c.Dollars += dollars

	if !s.timeOrdered {
		return
	}
	r := s.aggFor(t.State).Classify(t, class)
	if !r.Eligible {
		return
	}
	sg := b.Signed
	if r.Late {
		sg.Late++
	}
	if r.Rule == aggressor.RuleTick {
		sg.TickRule++
	}
	var side, quote *ClassAgg
	switch r.Side {
	case aggressor.AskSide:
		side, quote = &sg.AskSide, &sg.QuoteAsk
	case aggressor.BidSide:
		side, quote = &sg.BidSide, &sg.QuoteBid
	default:
		return
	}
	one := ClassAgg{Trades: 1, Shares: t.Size, Dollars: dollars}
	side.add(&one)
	if r.Rule == aggressor.RuleQuote || r.Rule == aggressor.RuleMidpoint {
		quote.add(&one)
	}
}

// AmendedSince reports the buckets amended by trades written behind the
// store's latest second since generation gen (a value a previous call
// returned; 0 = the beginning): minSec is the earliest amended second and
// newGen the generation to pass next time. minSec is meaningful only when
// newGen != gen (nothing amended otherwise). ok=false when more than
// AmendRing amendments have landed since gen — the ring has wrapped and
// the reader cannot know how far back they reach: rebuild in full and
// resume from newGen. A reader takes newGen BEFORE it reads the buckets
// so a write landing during the read is caught by its next call.
func (s *Store) AmendedSince(gen uint64) (minSec int64, newGen uint64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newGen = s.amendGen
	if gen > newGen {
		return 0, newGen, false // a generation from another store: rebuild
	}
	if newGen-gen > AmendRing {
		return 0, newGen, false
	}
	for g := gen + 1; g <= newGen; g++ {
		if sec := s.amendRing[g%AmendRing]; minSec == 0 || sec < minSec {
			minSec = sec
		}
	}
	return minSec, newGen, true
}

// ObserveQuote counts one NBBO update in its SIP-second bucket (D6: count
// only — book state is reconstructable from capture).
func (s *Store) ObserveQuote(q *ingest.Quote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bucketFor(q.State, q.SipTs).Quotes++
}

// Aggressor sums the store-wide honesty tally (MO-2 S5) from the buckets —
// the same numbers a reader derives from the file, so the printed line and
// the persisted columns cannot disagree. ok=false for a store that did not
// record signed volume.
func (s *Store) Aggressor() (aggressor.Stats, bool) {
	var out aggressor.Stats
	if !s.timeOrdered {
		return out, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.buckets {
		for _, b := range m {
			out.Eligible += b.Eligible().Trades
			out.Ask += b.Signed.AskSide.Trades
			out.Bid += b.Signed.BidSide.Trades
			out.Quote += b.Signed.QuoteAsk.Trades + b.Signed.QuoteBid.Trades
			out.Tick += b.Signed.TickRule
			out.Late += b.Signed.Late
		}
	}
	return out, true
}

// Report writes the end-of-session store summary — the 0.3 unknown-condition
// tripwire and the MO-2 aggressor honesty line — to w. The ONE path both
// cmd/live and cmd/replay call (review S9), so neither binary can omit a
// line. condOverflow is the pipeline's counter (ingest-cap note). Nil-safe.
func Report(w io.Writer, s *Store, condOverflow int64) {
	if s == nil {
		return
	}
	if n, ids := s.Unknown(); n > 0 {
		fmt.Fprintf(w, "!! tripwire: %d prints carried condition IDs missing from the 0.3 table: %v\n", n, ids)
	}
	if st, ok := s.Aggressor(); ok {
		fmt.Fprintln(w, st.Line(condOverflow))
	} else {
		fmt.Fprintln(w, aggressor.NotTimeOrderedLine())
	}
}

// Window sums one symbol's buckets over [fromSec, toSec) into a value copy
// — the dev-view read path promised in the Store doc. Seconds with no entry
// contribute nothing (the zero they are). LastPrice/LastSipTs follow the
// same latest-SIP-ts rule as live aggregation.
func (s *Store) Window(st *ingest.SymbolState, fromSec, toSec int64) Bucket {
	var out Bucket
	if s.timeOrdered {
		out.Signed = &SignedAgg{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.buckets[st]
	for sec := fromSec; sec < toSec; sec++ {
		if b := m[sec]; b != nil {
			out.add(b)
		}
	}
	return out
}

// FirstTradePrice returns the last-print price of the EARLIEST second in
// [fromSec, toSec) holding at least one print — the 3.2 since-open anchor.
// 1-second resolution: within that second it is the second's last print by
// SIP ts, not the day's literal first (for liquid names at the open, the
// official-open admin print duplicates the auction price in the same
// second, so the anchor lands on the cross). Raw tape, unfiltered by class
// (mini-spec 3.2 B2). ok=false when no second in the window traded.
func (s *Store) FirstTradePrice(st *ingest.SymbolState, fromSec, toSec int64) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.buckets[st]
	for sec := fromSec; sec < toSec; sec++ {
		if b := m[sec]; b != nil && b.Trades > 0 {
			return b.LastPrice, true
		}
	}
	return 0, false
}

// LastTradePrice returns the last-print price of the LATEST second in
// [fromSec, toSec) holding at least one print — "price as of" the window
// end, at 1-second resolution, raw tape (see FirstTradePrice). ok=false
// when no second in the window traded.
func (s *Store) LastTradePrice(st *ingest.SymbolState, fromSec, toSec int64) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.buckets[st]
	for sec := toSec - 1; sec >= fromSec; sec-- {
		if b := m[sec]; b != nil && b.Trades > 0 {
			return b.LastPrice, true
		}
	}
	return 0, false
}

// Unknown reports the tripwire: total prints carrying an unrecognized
// condition ID, and per-ID counts (sorted by ID for deterministic output).
func (s *Store) Unknown() (prints int64, ids []UnknownID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, n := range s.unknownIDs {
		ids = append(ids, UnknownID{ID: id, Prints: n})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
	return s.unknownPrints, ids
}

// UnknownID is one unrecognized condition ID and how many prints carried it.
type UnknownID struct {
	ID     int32
	Prints int64
}

// Row is one persisted (second, symbol) bucket.
type Row struct {
	Sec    int64
	Symbol string
	Bucket Bucket
}

// Rows snapshots the store as value copies, sorted (second, symbol) —
// second-major per D4. The lock is held only for the copy, so a mid-session
// snapshot stalls the pipeline for the memcpy, not the sort or the disk.
func (s *Store) Rows() []Row {
	s.mu.Lock()
	var rows []Row
	for st, m := range s.buckets {
		for sec, b := range m {
			r := Row{Sec: sec, Symbol: st.Symbol, Bucket: *b}
			if b.Signed != nil {
				sg := *b.Signed // value copy: the store's pointer stays private to the pipeline goroutine
				r.Bucket.Signed = &sg
			}
			rows = append(rows, r)
		}
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sec != rows[j].Sec {
			return rows[i].Sec < rows[j].Sec
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return rows
}

// baseHeader returns the 1.4 columns — the permanent required base. Column
// names for classes come from the classify table (lowercased), so the file
// documents the policy vocabulary it was written under.
func baseHeader() []string {
	cols := []string{"second", "symbol", "trades", "shares", "dollars", "last_price", "last_sip_ns", "quotes"}
	for c := 0; c < NumClasses; c++ {
		name := strings.ToLower(classify.Class(c).String())
		cols = append(cols, name+"_trades", name+"_shares", name+"_dollars")
	}
	return cols
}

// signedHeader returns the MO-2 signed-volume column family (S4, review
// S2) — additive and OPTIONAL on read as an all-or-none set: a pre-MO-2 or
// flat-file-derived file lacks them and loads with Session.HasSigned=false.
func signedHeader() []string {
	return []string{
		"ask_trades", "ask_shares", "ask_dollars",
		"bid_trades", "bid_shares", "bid_dollars",
		"quote_ask_trades", "quote_ask_shares", "quote_ask_dollars",
		"quote_bid_trades", "quote_bid_shares", "quote_bid_dollars",
		"tick_rule", "late",
	}
}

// header returns the self-describing CSV header for this store: the base
// columns, plus the signed family only when the store recorded it. The
// reader maps columns by name, never by position.
func (s *Store) header() []string {
	if s.timeOrdered {
		return append(baseHeader(), signedHeader()...)
	}
	return baseHeader()
}

// fnum formats a float with the shortest representation that round-trips
// exactly — determinism criterion 3 depends on formatting being a pure
// function of the value.
func fnum(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// WriteCSV persists the current store atomically: full serialize to
// <path>.tmp in the same directory, then rename. A reader (or a crash) can
// never see a half-written file; periodic live snapshots simply overwrite.
// Returns the number of (second, symbol) rows written.
func (s *Store) WriteCSV(path string) (int, error) {
	rows := s.Rows()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	write := func(cols []string) error {
		_, err := w.WriteString(strings.Join(cols, ",") + "\n")
		return err
	}
	if err := write(s.header()); err != nil {
		f.Close()
		return 0, err
	}
	cols := make([]string, 0, 8+3*NumClasses+len(signedHeader()))
	for i := range rows {
		r := &rows[i]
		b := &r.Bucket
		cols = cols[:0]
		cols = append(cols,
			strconv.FormatInt(r.Sec, 10), r.Symbol,
			strconv.FormatInt(b.Trades, 10), fnum(b.Shares), fnum(b.Dollars),
			fnum(b.LastPrice), strconv.FormatInt(b.LastSipTs, 10),
			strconv.FormatInt(b.Quotes, 10))
		for c := range b.Class {
			ca := &b.Class[c]
			cols = append(cols, strconv.FormatInt(ca.Trades, 10), fnum(ca.Shares), fnum(ca.Dollars))
		}
		if s.timeOrdered {
			sg := b.Signed
			for _, sa := range []*ClassAgg{&sg.AskSide, &sg.BidSide, &sg.QuoteAsk, &sg.QuoteBid} {
				cols = append(cols, strconv.FormatInt(sa.Trades, 10), fnum(sa.Shares), fnum(sa.Dollars))
			}
			cols = append(cols, strconv.FormatInt(sg.TickRule, 10), strconv.FormatInt(sg.Late, 10))
		}
		if err := write(cols); err != nil {
			f.Close()
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	return len(rows), os.Rename(tmp, path)
}

// Bucket-file naming (mini-spec 2.1 D3): the filename declares session
// coverage. Only full-session files (<date>.csv) and trades-only bootstrap
// files may enter baselines; partial capture-derived files must say so.
// These constants are the single spelling — cmd/replay validates against
// them, cmd/profiles discovers by them.
const (
	TradesOnlySuffix = ".trades-only.csv" // trades-feed-only bootstrap file (quote counts honestly zero)
	PartialSuffix    = ".partial.csv"     // capture-derived file that does not span the regular session
)

// Path returns the D4 session-file location: <base>/<date>.csv.
func Path(baseDir, date string) string {
	return filepath.Join(baseDir, date+".csv")
}

// PartialPath returns the D3 location for a store that does not span the
// regular session: <base>/<date>.partial.csv — never discovered into
// baselines.
func PartialPath(baseDir, date string) string {
	return filepath.Join(baseDir, date+PartialSuffix)
}

// bounds scans a two-level map for its min/max inner (epoch-second) keys —
// shared by Store.Bounds and Session.Bounds, whose maps differ only in
// key/value types.
func bounds[K comparable, V any](outer map[K]map[int64]V) (minSec, maxSec int64, ok bool) {
	for _, m := range outer {
		for sec := range m {
			if !ok || sec < minSec {
				minSec = sec
			}
			if !ok || sec > maxSec {
				maxSec = sec
			}
			ok = true
		}
	}
	return minSec, maxSec, ok
}

// Bounds returns the min and max epoch-second keys present in the store,
// ok=false when the store is empty. Used to decide whether a capture-derived
// file spans the regular session (D3 coverage naming).
func (s *Store) Bounds() (minSec, maxSec int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bounds(s.buckets)
}

// Totals returns the store-wide trade and quote counts. A spanning store
// with zero on either side means a feed was silently missing — not a full
// session record, however wide its time span.
func (s *Store) Totals() (trades, quotes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.buckets {
		for _, b := range m {
			trades += b.Trades
			quotes += b.Quotes
		}
	}
	return trades, quotes
}

// SpansRegularSession reports whether [minSec, maxSec] covers the regular
// session of the ET date containing minSec, and returns that date. Coverage
// requires data at/before 09:30:00 and at/after 16:00:00 — a record missing
// the open or the closing cross is not a full session.
func SpansRegularSession(minSec, maxSec int64) (date string, spans bool, err error) {
	date = session.Date(minSec * 1e9)
	openSec, err := session.BucketStart(date, session.OpenMinute)
	if err != nil {
		return date, false, err
	}
	closeSec, err := session.BucketStart(date, session.CloseMinute)
	if err != nil {
		return date, false, err
	}
	return date, minSec <= openSec && maxSec >= closeSec, nil
}

var _ ingest.Observer = (*Store)(nil)
