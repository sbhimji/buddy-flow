package ingest

import (
	"sort"
	"sync"
	"sync/atomic"
)

// SymbolState is all per-symbol state the core maintains: the current NBBO
// (the Lee-Ready lookup) plus ingest counters. One instance per symbol,
// created at table construction; feeders resolve symbol → *SymbolState once
// per message so the core never touches strings.
//
// Concurrency model: exactly one pipeline goroutine writes; readers (the
// future Phase 3 consumers, the 3.0 dev view) take the mutex for a coherent
// bid/ask snapshot. Counters are atomics so monitoring can read them without
// blocking the writer. 189 independent mutexes — no shared contention point.
type SymbolState struct {
	Symbol string

	mu sync.Mutex // guards the NBBO fields below
	// Current NBBO. Zero prices mean "no quote seen yet" (or an explicitly
	// zeroed book, which the wire also sends — e.g. pre-market one-sided
	// books print bid/ask 0 with size 0; see the 2026-08-11 quotes head).
	bidPrice, askPrice float64
	bidSize, askSize   int64
	bidExch, askExch   int32
	quoteTs            int64 // SipTs of the quote that set the NBBO
	quoteSeq           int64

	// Counters. Trades/Quotes are the reconciliation numbers (mini-spec
	// done-when #1). SeqRegress counts sequence_number going backwards per
	// feed — an out-of-order indicator, deliberately not called a "gap":
	// whether vendor sequence numbers are per-symbol-dense (making true
	// gap detection possible) is unconfirmed on the wire; 1.5 owns that.
	Trades           atomic.Int64
	Quotes           atomic.Int64
	SeqRegressTrades atomic.Int64
	SeqRegressQuotes atomic.Int64
	lastTradeSeq     int64 // pipeline-goroutine-only, no lock needed
	lastQuoteSeq     int64
}

// NBBO is a coherent snapshot of the current book for one symbol.
type NBBO struct {
	BidPrice, AskPrice float64
	BidSize, AskSize   int64
	BidExch, AskExch   int32
	Ts                 int64
	Seq                int64
}

// NBBO returns the current book under the symbol's lock.
func (s *SymbolState) NBBO() NBBO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return NBBO{
		BidPrice: s.bidPrice, AskPrice: s.askPrice,
		BidSize: s.bidSize, AskSize: s.askSize,
		BidExch: s.bidExch, AskExch: s.askExch,
		Ts: s.quoteTs, Seq: s.quoteSeq,
	}
}

// applyQuote installs a new NBBO. Called only from the pipeline goroutine.
// Every quote replaces the book — the SIP consolidated feed sends the full
// NBBO per update, not deltas. Arrival order wins: every quote installs,
// including sequence regressions, which are counted but never skipped.
// (Post-review policy, 2026-08-13: an earlier stale-skip guard could freeze
// a symbol's book for the rest of the session on one corrupt-high sequence
// value or a vendor sequence reset at reconnect. Per-symbol sequence
// semantics are unconfirmed on the wire, so the code must not enforce an
// invariant we can't evidence; "last received" is also the honest meaning of
// "current book" on a live feed. Measured on 2026-08-11: zero quote
// regressions in 45.5M quotes, so both policies agree on clean data.)
func (s *SymbolState) applyQuote(q *Quote) {
	s.Quotes.Add(1)
	if q.Seq < s.lastQuoteSeq {
		s.SeqRegressQuotes.Add(1) // monitoring signal (1.5), not a filter
	} else {
		s.lastQuoteSeq = q.Seq
	}
	s.mu.Lock()
	s.bidPrice, s.askPrice = q.BidPrice, q.AskPrice
	s.bidSize, s.askSize = q.BidSize, q.AskSize
	s.bidExch, s.askExch = q.BidExch, q.AskExch
	s.quoteTs, s.quoteSeq = q.SipTs, q.Seq
	s.mu.Unlock()
}

// applyTrade records a print. NBBO is untouched — trades only count here;
// classification against the book is story 3.3.
func (s *SymbolState) applyTrade(t *Trade) {
	if t.Seq < s.lastTradeSeq {
		s.SeqRegressTrades.Add(1)
	} else {
		s.lastTradeSeq = t.Seq
	}
	s.Trades.Add(1)
}

// Table maps symbols to their state. Built once at startup from the universe;
// the map is read-only afterwards, so lookups need no lock. In stress mode
// (--full) feeders encounter non-universe symbols; they call LookupOrCreate
// under its own lock — never on the live path, where the subscription list
// already bounds the symbol set.
type Table struct {
	states map[string]*SymbolState
	all    []*SymbolState

	extraMu sync.Mutex
	extra   map[string]*SymbolState // stress-mode symbols outside the universe
}

// NewTable builds the read-only state table for the given universe.
func NewTable(symbols []string) *Table {
	t := &Table{
		states: make(map[string]*SymbolState, len(symbols)),
		extra:  map[string]*SymbolState{},
	}
	for _, sym := range symbols {
		st := &SymbolState{Symbol: sym}
		t.states[sym] = st
		t.all = append(t.all, st)
	}
	return t
}

// Lookup returns the state for a universe symbol, nil otherwise. The
// map[string] lookup with a []byte-converted key does not allocate.
func (t *Table) Lookup(sym string) *SymbolState { return t.states[sym] }

// LookupOrCreate returns existing state or creates it (stress mode only).
func (t *Table) LookupOrCreate(sym string) *SymbolState {
	if st := t.states[sym]; st != nil {
		return st
	}
	t.extraMu.Lock()
	defer t.extraMu.Unlock()
	st := t.extra[sym]
	if st == nil {
		st = &SymbolState{Symbol: sym}
		t.extra[sym] = st // string key is copied by the map insert
	}
	return st
}

// All returns the universe states in deterministic (input) order.
func (t *Table) All() []*SymbolState { return t.all }

// Extras returns the stress-mode states (symbols outside the universe seen
// under -full), sorted by symbol for deterministic reporting. Kept separate
// from All so universe reporting and the read-only hot path are unaffected;
// callers aggregate these so stress runs don't hide 80% of their load.
func (t *Table) Extras() []*SymbolState {
	t.extraMu.Lock()
	defer t.extraMu.Unlock()
	out := make([]*SymbolState, 0, len(t.extra))
	for _, st := range t.extra {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// Len returns the universe size (stress-mode extras excluded).
func (t *Table) Len() int { return len(t.all) }
