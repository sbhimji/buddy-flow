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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

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
type Bucket struct {
	Trades    int64
	Shares    float64
	Dollars   float64
	LastPrice float64
	LastSipTs int64
	Quotes    int64
	Class     [NumClasses]ClassAgg
}

func (b *Bucket) add(o *Bucket) {
	b.Trades += o.Trades
	b.Shares += o.Shares
	b.Dollars += o.Dollars
	b.Quotes += o.Quotes
	if o.LastSipTs >= b.LastSipTs {
		b.LastPrice, b.LastSipTs = o.LastPrice, o.LastSipTs
	}
	for i := range b.Class {
		b.Class[i].Trades += o.Class[i].Trades
		b.Class[i].Shares += o.Class[i].Shares
		b.Class[i].Dollars += o.Class[i].Dollars
	}
}

// Store implements ingest.Observer. The single pipeline goroutine writes;
// concurrent readers (snapshots, the future dev view) take the mutex. Sparse
// by construction: a (ticker, second) entry exists only if a message hit it.
type Store struct {
	mu      sync.Mutex
	buckets map[*ingest.SymbolState]map[int64]*Bucket

	// Unknown-condition tripwire (0.3: quarantined + tripwired). Guarded by
	// mu — the slow path only.
	unknownPrints int64
	unknownIDs    map[int32]int64
}

// NewStore returns an empty store, ready to register as pipeline observer.
func NewStore() *Store {
	return &Store{
		buckets:    map[*ingest.SymbolState]map[int64]*Bucket{},
		unknownIDs: map[int32]int64{},
	}
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
		m[sec] = b
	}
	return b
}

// ObserveTrade aggregates one print into its SIP-second bucket.
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
}

// ObserveQuote counts one NBBO update in its SIP-second bucket (D6: count
// only — book state is reconstructable from capture).
func (s *Store) ObserveQuote(q *ingest.Quote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bucketFor(q.State, q.SipTs).Quotes++
}

// Window sums one symbol's buckets over [fromSec, toSec) into a value copy
// — the dev-view read path promised in the Store doc. Seconds with no entry
// contribute nothing (the zero they are). LastPrice/LastSipTs follow the
// same latest-SIP-ts rule as live aggregation.
func (s *Store) Window(st *ingest.SymbolState, fromSec, toSec int64) Bucket {
	var out Bucket
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
			rows = append(rows, Row{Sec: sec, Symbol: st.Symbol, Bucket: *b})
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

// header returns the self-describing CSV header. Column names for classes
// come from the classify table (lowercased), so the file documents the
// policy vocabulary it was written under; 3.3 adds columns without breaking
// old files — the reader maps columns by name.
func header() []string {
	cols := []string{"second", "symbol", "trades", "shares", "dollars", "last_price", "last_sip_ns", "quotes"}
	for c := 0; c < NumClasses; c++ {
		name := strings.ToLower(classify.Class(c).String())
		cols = append(cols, name+"_trades", name+"_shares", name+"_dollars")
	}
	return cols
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
	if err := write(header()); err != nil {
		f.Close()
		return 0, err
	}
	cols := make([]string, 0, 8+3*NumClasses)
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
