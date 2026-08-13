// Package ingest is the source-agnostic core of the 1.1 ingest skeleton
// (mini-spec: docs/mini-specs/1.1-ingest-skeleton.md).
//
// Feeders (live websocket, file replay) decode vendor messages into the
// structs here and submit them to a Pipeline. The core updates per-symbol
// state (current NBBO — the Lee-Ready lookup — plus counters) and nothing
// else: no metrics math lives here; Phase 3 consumes this state downstream.
package ingest

// MaxConditions bounds the fixed-size condition arrays in Trade/Quote so the
// hot path never allocates per message. The largest combination observed on
// the 2026-08-11 session was 4 IDs; 8 leaves slack. Overflow is counted, not
// dropped (see Pipeline.CondOverflow).
const MaxConditions = 8

// Kind discriminates the Msg union.
type Kind uint8

const (
	KindTrade Kind = iota + 1
	KindQuote
)

// Timestamps are epoch nanoseconds UTC in int64 — Go's native representation
// (time.Duration/UnixNano are int64 ns). Nanoseconds are the canonical unit
// because the flat files already carry them and sub-ms ordering is real
// information (e.g. official open/close rows print ~1ms after the auction
// cross they duplicate). The websocket sends ms; the live feeder scales
// ms→ns (one integer multiply — negligible next to its JSON decode).

// Trade is one print, normalized from either source.
type Trade struct {
	State    *SymbolState // resolved by the feeder; never nil inside the core
	Price    float64
	Size     float64 // flat files carry fractional shares ("152.000000", "0.007145")
	SipTs    int64
	PartTs   int64 // participant timestamp; 0 when absent
	Seq      int64
	Exchange int32
	Tape     int32
	Cond     [MaxConditions]int32
	NCond    int
}

// Quote is one consolidated NBBO update.
type Quote struct {
	State    *SymbolState
	BidPrice float64
	AskPrice float64
	BidSize  int64
	AskSize  int64
	SipTs    int64
	Seq      int64
	BidExch  int32
	AskExch  int32
	Cond     [MaxConditions]int32
	NCond    int
}

// Msg is the union the pipeline queue carries. A struct copy (~250B) per
// message instead of an interface keeps the queue allocation-free; at the
// 120k msg/sec floor that is ~30MB/s of memcpy — noise.
//
// CONTRACT — reused/union struct, two rules for consumers:
//
//  1. Only Cond[0:NCond) is valid. Feeders reuse one Msg across rows and
//     parseConditions writes only the first NCond slots — entries past NCond
//     are the PREVIOUS message's IDs. Story 3.3's classifier is the first
//     real consumer; iterate by NCond, never over the whole array.
//  2. Only the arm named by Kind is valid. The other arm carries the
//     previous message of that kind, including a live *SymbolState pointer.
//
// Deliberate width tradeoff: both arms are embedded, so every future message
// kind widens every queue slot (queue capacity × sizeof(Msg) is reserved
// virtual memory; resident memory follows actual queue depth). Revisit the
// union encoding if a third kind lands.
type Msg struct {
	Kind  Kind
	Trade Trade
	Quote Quote
}
