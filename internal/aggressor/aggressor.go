// Package aggressor is the one policy source for aggressor-side
// classification of prints (dev-plan story 3.3 part 1, mini-spec
// docs/mini-specs/metric-overload/MO-2-signed-volume-bucket.md): which
// prints are eligible, when a print is too late to classify, when the book
// is usable, and the quote → midpoint → tick cascade (Lee-Ready). The
// bucket store calls it from ObserveTrade and stores the result at 1-second
// resolution; nothing here is a metric.
//
// Honesty (verbatim posture from the dev plan, F2): only exact-midpoint
// prints fall to the tick rule — the weakest classifier — and are reported
// as such (RuleTick); price-improved retail executions inside the spread are
// classified by the midpoint rule, which is an aggregate approximation, not
// print-level truth; Lee-Ready degrades at the open while quotes are wide or
// crossed (the D9 suppression window in MO-3 is the mitigation, not this
// package). The vendor's SIP consolidated feed carries no venue-supplied
// side, so the cascade starts at quote-relative classification (vendor note
// 2026-08-11). On-time prints only (2026-08-14 amendment): a late print's
// book is not the book it executed against, so it is counted (Late) and left
// unclassified rather than classified against the wrong book.
//
// Determinism: pure functions of (trade, book, per-symbol state); the
// per-symbol state is written only from the single pipeline goroutine, in
// arrival order — identical in live and capture replay. Sources that are
// not time-ordered (ticker-sorted flat files streamed concurrently) must not
// run this cascade at all; the bucket store gates on that.
package aggressor

import (
	"fmt"

	"buddy-flow/internal/classify"
	"buddy-flow/internal/ingest"
)

// LateTolerance is the maximum SipTs − PartTs (ns) for a print to be
// classified against the current book. 1s is the owner default (MO-2
// step 2, review S6); the flat-file lateness study (backlog) sizes it.
const LateTolerance int64 = 1_000_000_000

// Book choice (review S3, measured 2026-08-24, recorded in the story close
// notes): a two-slot quote ring — classify against the newest quote whose
// SipTs is strictly before the print's — moved the quote-rule share by
// 0.1 point and the ask/bid split by 0.1 point against the book as of
// arrival, below the keep thresholds (≥2 / ≥1). The cascade classifies
// against the book as of arrival: the NBBO ingest has installed when the
// print reaches the observer.

// Side is the classified aggressor side of a print. Identifiers are
// measurements of where the print landed relative to the book (S0).
type Side uint8

const (
	Unclassified Side = iota
	AskSide           // above the ask / at the ask on an unlocked book / above the midpoint / up-tick at the midpoint
	BidSide           // the mirror
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

// Rule is the cascade step that decided a print.
type Rule uint8

const (
	RuleNone     Rule = iota // ineligible, late, or no usable book
	RuleQuote                // at/through the quote
	RuleMidpoint             // strictly inside the spread, off the midpoint
	RuleTick                 // exactly at the midpoint: tick rule (side may still be Unclassified)
)

// Result is the cascade's disposition of one print.
type Result struct {
	Eligible bool // Class == CONTINUOUS; false → nothing below applies
	Late     bool // skipped as late (denominator-only)
	Rule     Rule
	Side     Side
}

// Eligible reports whether a print's 0.3 class enters the cascade at all
// (MO-2 step 1): CONTINUOUS only. NON_PRICE_FORMING is excluded from
// Lee-Ready by policy; BLOCK prints (Rule 127, crossing session) execute
// outside the quote — no open-market aggression occurred, and the quote
// rule would read them as maximally aggressive (print-inclusion.md, review
// S1); crosses hit no prevailing NBBO; DUPLICATE/NON_FLOW/UNKNOWN are never
// counted anywhere. BLOCK dollars stay in the Counted denominator
// downstream; they are simply never signed.
func Eligible(c classify.Class) bool {
	return c == classify.Continuous
}

// Book is an NBBO snapshot the cascade classifies against: prices, the SIP
// ts of the quote that set it, and whether that quote's conditions leave
// the book usable (ingest stamps classify.QuoteUsable into NBBO.Usable).
type Book struct {
	Bid, Ask float64
	Ts       int64
	Usable   bool
}

// Valid is MO-2 step 3: both sides quoted, not crossed, and a usable quote
// condition. A locked book (Bid == Ask) is valid; Classify treats its one
// price as the midpoint (review S4).
func (b Book) Valid() bool {
	return b.Bid > 0 && b.Ask > 0 && b.Bid <= b.Ask && b.Usable
}

// Classify runs the cascade for one print. tickRef is the symbol's last
// eligible on-time print price DIFFERENT from the current price sequence
// (see State); hasTickRef=false means no such prior price exists. Every
// eligible print lands in exactly one of: late, no-book, ask/bid by quote,
// ask/bid by midpoint, ask/bid/unclassified by tick.
//
// Order (MO-2 step 4, review B2/S4): with mid = (Bid+Ask)/2 computed once —
// above the ask, or at the ask on an unlocked book → AskSide (quote);
// below the bid, or at the bid on an unlocked book → BidSide (quote);
// above mid → AskSide, below mid → BidSide (midpoint); exactly mid → tick
// rule. On a locked book the single price is the midpoint, so a print at it
// goes to the tick rule and only prints through the lock are quote-ruled.
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
	unlocked := book.Bid < book.Ask
	mid := (book.Bid + book.Ask) / 2
	switch {
	case price > book.Ask || (unlocked && price == book.Ask):
		r.Rule, r.Side = RuleQuote, AskSide
	case price < book.Bid || (unlocked && price == book.Bid):
		r.Rule, r.Side = RuleQuote, BidSide
	case price > mid:
		r.Rule, r.Side = RuleMidpoint, AskSide
	case price < mid:
		r.Rule, r.Side = RuleMidpoint, BidSide
	default: // exactly the midpoint
		r.Rule = RuleTick
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

// State is the per-symbol memory the cascade needs beyond the current NBBO:
// the tick-rule reference. The observer (not ingest) owns it:
// ingest.Pipeline.Run applies each message to SymbolState BEFORE the
// observer sees it, so a reference kept in SymbolState and updated in
// applyTrade would already hold the current print when the tick rule needs
// the previous one. The book itself is read from SymbolState.NBBO() — one
// snapshot of prices, ts and usability (review S8).
type State struct {
	// lastPrice is the most recent eligible on-time print price; tickRef is
	// the most recent eligible on-time price that differed from lastPrice.
	lastPrice, tickRef  float64
	hasLast, hasTickRef bool
}

// BookOf is the book a print is classified against: the NBBO ingest has
// installed as of the print's arrival.
func BookOf(st *ingest.SymbolState) Book {
	nb := st.NBBO()
	return Book{Bid: nb.BidPrice, Ask: nb.AskPrice, Ts: nb.Ts, Usable: nb.Usable}
}

// Classify classifies one print against the symbol's book and this state,
// then advances the tick reference (eligible on-time prints only — late
// prints carry stale prices, and ineligible ones are not part of the
// price-forming sequence). The tick reference for a print is the last
// different price BEFORE it: same-price runs look back to the price that
// preceded the run.
func (s *State) Classify(t *ingest.Trade, class classify.Class) Result {
	ref, hasRef := s.tickRef, s.hasTickRef
	if s.hasLast && t.Price != s.lastPrice {
		ref, hasRef = s.lastPrice, true
	}
	r := Classify(class, t.SipTs, t.PartTs, t.Price, BookOf(t.State), ref, hasRef)
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
// prints. Quote = classified by the quote or midpoint rule (the strong
// classifiers); Ask/Bid are totals including tick-rule prints. Derived,
// never stored: Unclassified = Eligible − Ask − Bid (late included) and
// NoBook = Unclassified − Late − TickUnclassified... reported simply as
// eligible − ask − bid − late, which also folds in the rare tick-rule
// print with no prior different price.
type Stats struct {
	Eligible, Ask, Bid, Quote, Tick, Late int64
}

// Unclassified returns the eligible prints that landed on neither side,
// late prints included.
func (s Stats) Unclassified() int64 { return s.Eligible - s.Ask - s.Bid }

// NoBook returns the on-time eligible prints that landed on neither side:
// invalid/absent book, plus tick-rule prints with no prior different price.
func (s Stats) NoBook() int64 { return s.Unclassified() - s.Late }

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
	return fmt.Sprintf("%saggressor: eligible=%d ask=%s bid=%s quote=%s tick=%s late=%s nobook=%s unclassified(incl late)=%s",
		prefix, s.Eligible, pct(s.Ask), pct(s.Bid), pct(s.Quote), pct(s.Tick), pct(s.Late), pct(s.NoBook()), pct(s.Unclassified()))
}

// NotTimeOrderedLine is what a store that never ran the cascade prints in
// place of Line: the source (a flat-file replay) is not time-ordered, so
// there is no split to report — and no signed columns in its file.
func NotTimeOrderedLine() string {
	return "aggressor: not time-ordered source — no classification run, no signed columns written"
}
