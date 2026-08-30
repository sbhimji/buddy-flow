// Package classify implements the print-inclusion policy (story 0.3).
//
// Source of truth: docs/foundations/print-inclusion.md. The table and rules here
// are a direct transcription of that document's normative table; any policy change
// edits the document first, then this file to match. The table-driven tests in
// classify_test.go enforce the correspondence.
package classify

// Class is the disposition of a print with respect to flow metrics.
type Class int

// Ordered by precedence, highest first: a multi-condition print takes the
// highest-precedence class among its conditions (exclusion-dominant rule, plus
// Unknown quarantining above all — an unrecognized code means the print's
// semantics are unknown, so no metric may consume it).
const (
	Unknown         Class = iota // condition ID not in the table: stored, ignored by all metrics, tripwired
	NonFlow                      // admin non-trades, non-regular settlement, out-of-session: never counted
	Duplicate                    // dollars already on the tape as constituent prints: never counted
	NonPriceForming              // $vol counted; excluded from price-forming series and Lee-Ready; lateness-logged
	CrossReopen                  // post-halt reopening cross: cross ledger, flagged, no z, never resets since-open anchor
	CrossOpen                    // opening auction cross: anchor (listing exchange print only) + cross ledger
	CrossClose                   // closing auction cross: cross ledger
	Block                        // negotiated/crossed institutional print: $vol only, block-logged
	Continuous                   // counts everywhere; Lee-Ready eligible
)

var classNames = map[Class]string{
	Unknown:         "UNKNOWN",
	NonFlow:         "NON_FLOW",
	Duplicate:       "DUPLICATE",
	NonPriceForming: "NON_PRICE_FORMING",
	CrossReopen:     "CROSS_REOPEN",
	CrossOpen:       "CROSS_OPEN",
	CrossClose:      "CROSS_CLOSE",
	Block:           "BLOCK",
	Continuous:      "CONTINUOUS",
}

func (c Class) String() string { return classNames[c] }

// saleConditions is the normative per-condition table. Keys are the vendor's
// numeric condition IDs (never SIP characters — they collide across tapes).
var saleConditions = map[int32]Class{
	1:  Continuous,      // Acquisition (legacy)
	2:  Duplicate,       // Average Price Trade — double-counts constituent fills
	3:  Continuous,      // Automatic Execution
	4:  Continuous,      // Bunched Trade
	5:  NonPriceForming, // Bunched Sold Trade — reported late
	6:  Continuous,      // CAP Election (legacy)
	7:  NonFlow,         // Cash Sale — same-day settlement
	8:  CrossClose,      // Closing Prints (UTP)
	9:  Block,           // Cross Trade — crossing session, no aggression
	10: NonPriceForming, // Derivatively Priced
	11: Continuous,      // Distribution
	12: NonFlow,         // Form T / Extended Hours
	13: NonFlow,         // Extended Hours Sold OOS
	14: Continuous,      // Intermarket Sweep
	15: NonFlow,         // Official Close — admin, zero volume
	16: NonFlow,         // Official Open — admin, zero volume
	17: CrossOpen,       // Market Center Opening Trade (CTA)
	18: CrossReopen,     // Market Center Reopening Trade (CTA)
	19: CrossClose,      // Market Center Closing Trade (CTA)
	20: NonFlow,         // Next Day — settlement
	21: NonPriceForming, // Price Variation Trade
	22: NonPriceForming, // Prior Reference Price — >90s stale
	23: Continuous,      // Rule 155 (AMEX)
	24: Block,           // Rule 127 (NYSE) — outside-quote negotiated block
	25: CrossOpen,       // Opening Prints (UTP) — the 09:30 anchor print
	27: Continuous,      // Stopped Stock (legacy)
	28: CrossReopen,     // Re-Opening Prints (UTP)
	29: NonFlow,         // Seller — seller's-option settlement
	30: Continuous,      // Sold Last — in sequence, report late; lateness-logged
	31: Continuous,      // Sold Last + Stopped Stock (legacy) — rides with 30
	32: NonPriceForming, // Sold Out of Sequence — timestamp unreliable
	33: NonPriceForming, // Sold OOS + Stopped Stock (legacy) — rides with 32
	34: Continuous,      // Split Trade
	35: Continuous,      // Stock Option — option-MM delta-hedge stock leg (legacy)
	36: Continuous,      // Yellow Flag Regular Trade
	37: Continuous,      // Odd Lot — fully counted; retail flow is core signal
	38: NonFlow,         // Corrected Consolidated Close — admin, no volume
	52: NonPriceForming, // Contingent Trade — non-market price
	53: NonPriceForming, // Qualified Contingent Trade
	55: CrossOpen,       // Opening Reopening Trade Detail — PROVISIONAL, see docs/backlog.md (id 55)
}

// neutralConditions ride on trade messages but say nothing about the print's
// economics (ticker-state / regulatory flags); the classifier ignores them.
var neutralConditions = map[int32]bool{
	41: true,                               // Trade Thru Exempt
	57: true, 58: true, 59: true, 60: true, // Short Sale Restriction indicators
	62: true, 63: true, 64: true, 65: true, 66: true, // Financial Status indicators
	67: true, 68: true, 69: true, 70: true, 71: true,
}

// Classify maps a print's condition-code array to its class.
//
// An empty (or all-neutral) array is a Regular Sale: Continuous — the majority
// of the tape. unknownIDs is non-nil only when an unrecognized ID was seen; the
// caller feeds it to the data-quality tripwire. Sold Last prints (30, 31) are
// Continuous but must also be lateness-logged by the caller (see policy doc);
// MonitorLateness reports that.
func Classify(conditions []int32) (class Class, unknownIDs []int32) {
	class = Continuous
	for _, id := range conditions {
		if neutralConditions[id] {
			continue
		}
		c, ok := saleConditions[id]
		if !ok {
			unknownIDs = append(unknownIDs, id)
			c = Unknown
		}
		if c < class { // lower value = higher precedence
			class = c
		}
	}
	return class, unknownIDs
}

// MonitorLateness reports whether a print must be appended to the lateness log:
// every NonPriceForming print, plus Sold Last prints (30, 31), which classify
// Continuous because they print in sequence but whose report lateness (t − pt)
// the policy tracks for an evidence-based revisit.
func MonitorLateness(class Class, conditions []int32) bool {
	if class == NonPriceForming {
		return true
	}
	if class != Continuous {
		return false
	}
	for _, id := range conditions {
		if id == 30 || id == 31 {
			return true
		}
	}
	return false
}

// quoteUnusable is the story-3.3 (MO-2) NBBO-validity table: the quote
// conditions under which the consolidated NBBO is NOT a book a print can be
// classified against. The 0.3 policy doc deferred this table to 3.3's
// appendix, which was never written; MO-2 defines it here, keyed on the
// vendor's numeric quote-condition IDs (`type: quote_condition` /
// `market_condition` in docs/foundations/massive-conditions.json).
//
// Principle: a quote is usable when it is a firm two-sided (or one-sided —
// the price check handles a zero side) market. It is unusable when the SIP
// says the market is closed, halted/paused, non-firm, an indication rather
// than a quote, in an auction, or locked/crossed. Slow/manual quotes (3–12,
// 71) remain firm quotes and stay usable. Regulatory/informational riders
// (26, 28–30, 41, 42, 81, 82, 94) say nothing about firmness and stay usable.
var quoteUnusable = map[int32]bool{
	15: true, // Closed
	17: true, // Fast Trading (UTP F): quotes not firm at displayed size
	18: true, // Trading Range Indication: an indication, not a quote
	19: true, // Market Maker Quotes Closed
	20: true, // Non-Firm
	21: true, // News Dissemination (halt)
	22: true, // Order Influx (halt)
	23: true, // Order Imbalance (halt)
	27: true, // News Pending (halt)
	32: true, // No Open / No Resume
	40: true, // On Demand Auction
	43: true, // LULD Trading Pause
	84: true, // Crossed Market flag
	85: true, // Locked Market flag: at/above ask and at/below bid both fire
}

// quoteKnown lists every quote-riding condition ID in the vendor table; an
// ID outside it is UNKNOWN and, mirroring 0.3's quarantine, makes the book
// unusable rather than silently trusted.
var quoteKnown = map[int32]bool{
	1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 9: true,
	10: true, 11: true, 12: true, 13: true, 14: true, 16: true, 26: true,
	28: true, 29: true, 30: true, 41: true, 42: true, 71: true, 81: true,
	82: true, 94: true,
}

// QuoteUsable reports whether an NBBO update carrying these quote conditions
// leaves the book usable for aggressor classification (story 3.3 / MO-2).
// An empty list is a regular quote — the majority of the live tape carries
// no `c` at all — and is usable. Any unusable or unknown ID wins
// (exclusion-dominant, like Classify).
func QuoteUsable(conditions []int32) bool {
	for _, id := range conditions {
		if quoteUnusable[id] || !quoteKnown[id] {
			return false
		}
	}
	return true
}
