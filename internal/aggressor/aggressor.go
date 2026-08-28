// Package aggressor is the one policy source for aggressor-side
// classification of prints (dev-plan story 3.3 part 1, mini-spec
// docs/mini-specs/metric-overload/MO-2-signed-volume-bucket.md): which
// prints are eligible, when a print is too late to classify, when the book
// is usable, and the quote-relative → tick-rule cascade. The bucket store
// calls it from ObserveTrade and stores the result at 1-second resolution;
// nothing here is a metric.
//
// Honesty (verbatim posture from the dev plan, F2): midpoint / price-improved
// retail executions fall to the tick rule — the weakest classifier — and
// are reported as such (TickRule); DeltaRatio and every other consumer of
// AskSide/BidSide is an aggregate approximation, not print-level truth;
// Lee-Ready degrades at the open while quotes are wide or crossed (the D9
// suppression window in MO-3 is the mitigation, not this package). The
// vendor's SIP consolidated feed carries no venue-supplied side, so the
// cascade starts at quote-relative classification (vendor note 2026-08-11).
// On-time prints only (2026-08-14 amendment): a late print's book is not
// the book it executed against, so it is counted (Late) and left
// unclassified rather than classified against the wrong book.
//
// Determinism: pure functions of (trade, book, per-symbol tick state); the
// per-symbol state is written only from the single pipeline goroutine, in
// arrival order — identical in live and replay.
package aggressor

import (
	"fmt"

	"buddy-flow/internal/classify"
	"buddy-flow/internal/ingest"
)

// LateTolerance is the maximum SipTs − PartTs (ns) for a print to be
// classified against the current book. Default 1s (MO-2 step 2); the
// flat-file lateness study (backlog) sizes it properly.
const LateTolerance int64 = 1_000_000_000

// Side is the classified aggressor side of a print. Identifiers are
// measurements of where the print landed relative to the book (S0).
type Side uint8

const (
	Unclassified Side = iota
	AskSide           // at or above the ask, or an up-tick inside the spread
	BidSide           // at or below the bid, or a down-tick inside the spread
)

func (s Side) String() string {
	switch s {
	case AskSide:
		return "ask"
	case BidSide:
		return "bid"
	}
	return "unclassified"
}

// Result is the cascade's disposition of one print.
type Result struct {
	Eligible bool // Class ∈ {CONTINUOUS, BLOCK}; false → nothing below applies
	Late     bool // skipped as late (denominator-only)
	TickRule bool // reached the tick rule (whichever way it landed)
	Side     Side
}

// Eligible reports whether a print's 0.3 class enters the cascade at all
// (MO-2 step 1). NON_PRICE_FORMING is excluded from Lee-Ready by policy;
// crosses hit no prevailing NBBO; DUPLICATE/NON_FLOW/UNKNOWN are never
// counted anywhere. BLOCK is in per the MO-2 cascade as written — see the
// story's open questions: print-inclusion.md notes Rule 127 blocks execute
// outside the quote and would read as maximally aggressive.
func Eligible(c classify.Class) bool {
	return c == classify.Continuous || c == classify.Block
}

// Book is the NBBO snapshot the cascade classifies against, plus whether
// the quote that set it leaves the book usable (classify.QuoteUsable).
type Book struct {
	Bid, Ask float64
	Usable   bool
}

// Valid is MO-2 step 3: both sides quoted, not crossed, and a usable quote
// condition. A locked book (Bid == Ask) is valid by the spec's rule; the
// SIP's Locked Market flag (85) is what marks those unusable when present.
func (b Book) Valid() bool {
	return b.Bid > 0 && b.Ask > 0 && b.Bid <= b.Ask && b.Usable
}

// Classify runs the cascade for one print. tickRef is the symbol's last
// eligible on-time print price DIFFERENT from the current price sequence
// (see State); hasTickRef=false means no such prior price exists. Every
// eligible print lands in exactly one of: late, unclassified (invalid
// book), ask/bid by quote, ask/bid/unclassified by tick.
func Classify(class classify.Class, sipTs, partTs int64, price float64, book Book, tickRef float64, hasTickRef bool) Result {
	if !Eligible(class) {
		return Result{}
	}
	r := Result{Eligible: true}
	if partTs == 0 || sipTs-partTs > LateTolerance {
		r.Late = true
		return r
	}
	if !book.Valid() {
		return r
	}
	switch {
	case price >= book.Ask:
		r.Side = AskSide
	case price <= book.Bid:
		r.Side = BidSide
	default: // strictly inside the spread
		r.TickRule = true
		switch {
		case !hasTickRef:
		case price > tickRef:
			r.Side = AskSide
		case price < tickRef:
			r.Side = BidSide
		}
	}
	return r
}

// State is the per-symbol memory the cascade needs beyond the NBBO: the
// tick-rule reference and the usability of the quote that set the book.
// The observer (not ingest) owns it: ingest.Pipeline.Run applies each
// message to SymbolState BEFORE the observer sees it, so a reference kept
// in SymbolState and updated in applyTrade would already hold the current
// print when the tick rule needs the previous one.
type State struct {
	quoteUsable bool
	// lastPrice is the most recent eligible on-time print price; tickRef is
	// the most recent eligible on-time price that differed from lastPrice.
	lastPrice, tickRef  float64
	hasLast, hasTickRef bool
}

// ObserveQuote records whether the quote now installed as the NBBO is
// usable. Called after ingest has applied the same quote, so the flag and
// t.State.NBBO() describe the same book.
func (s *State) ObserveQuote(q *ingest.Quote) {
	s.quoteUsable = classify.QuoteUsable(q.Cond[:q.NCond])
}

// Classify classifies one print against the symbol's current book and this
// state, then advances the tick reference (eligible on-time prints only —
// late prints carry stale prices, and ineligible ones are not part of the
// price-forming sequence). The tick reference for a print is the last
// different price BEFORE it: same-price runs look back to the price that
// preceded the run.
func (s *State) Classify(t *ingest.Trade, class classify.Class) Result {
	nb := t.State.NBBO()
	book := Book{Bid: nb.BidPrice, Ask: nb.AskPrice, Usable: s.quoteUsable}
	ref, hasRef := s.tickRef, s.hasTickRef
	if s.hasLast && t.Price != s.lastPrice {
		ref, hasRef = s.lastPrice, true
	}
	r := Classify(class, t.SipTs, t.PartTs, t.Price, book, ref, hasRef)
	if r.Eligible && !r.Late {
		if !s.hasLast {
			s.lastPrice, s.hasLast = t.Price, true
		} else if t.Price != s.lastPrice {
			s.tickRef, s.hasTickRef = s.lastPrice, true
			s.lastPrice = t.Price
		}
	}
	return r
}

// Stats is the session-level honesty tally (MO-2 S5): counts over eligible
// prints. Unclassified is derived, never stored (Eligible − Ask − Bid).
type Stats struct {
	Eligible, Ask, Bid, Tick, Late int64
}

// Unclassified returns the eligible prints that landed on neither side
// (late prints included).
func (s Stats) Unclassified() int64 { return s.Eligible - s.Ask - s.Bid }

// Line renders the end-of-session stats line. condOverflow is the
// pipeline's CondOverflow counter: nonzero means some prints reached the
// classifier with truncated condition lists (ingest-cap note), so the line
// is prefixed `!!` — the rates are then not to be trusted until the cap is
// raised. Percentages are of eligible prints; with none, they print n/a.
func (s Stats) Line(condOverflow int64) string {
	pct := func(n int64) string {
		if s.Eligible == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(s.Eligible))
	}
	prefix := ""
	if condOverflow > 0 {
		prefix = "!! "
	}
	return fmt.Sprintf("%saggressor: eligible=%d ask=%s bid=%s tick=%s late=%s unclassified=%s",
		prefix, s.Eligible, pct(s.Ask), pct(s.Bid), pct(s.Tick), pct(s.Late), pct(s.Unclassified()))
}
