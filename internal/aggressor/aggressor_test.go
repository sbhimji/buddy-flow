package aggressor

import (
	"strings"
	"testing"

	"buddy-flow/internal/classify"
	"buddy-flow/internal/ingest"
)

const (
	sip  = int64(1_000_000_000_000)
	onTm = sip - 500_000_000 // 0.5s early: on time
)

// TestCascadeBranches: every branch the story lists (plus the midpoint and
// locked-book rules from review B2/S4) lands in exactly one bin (MO-2
// done-when #1).
func TestCascadeBranches(t *testing.T) {
	good := Book{Bid: 100.00, Ask: 100.10, Usable: true}   // mid 100.05
	locked := Book{Bid: 100.00, Ask: 100.00, Usable: true} // mid 100.00
	cases := []struct {
		name   string
		class  classify.Class
		partTs int64
		price  float64
		book   Book
		ref    float64
		hasRef bool
		want   Result
	}{
		{"at ask", classify.Continuous, onTm, 100.10, good, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: AskSide}},
		{"above ask", classify.Continuous, onTm, 100.25, good, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: AskSide}},
		{"at bid", classify.Continuous, onTm, 100.00, good, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: BidSide}},
		{"below bid", classify.Continuous, onTm, 99.90, good, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: BidSide}},
		{"inside above mid (sub-penny)", classify.Continuous, onTm, 100.0799, good, 100.09, true, Result{Eligible: true, Rule: RuleMidpoint, Side: AskSide}},
		{"inside below mid", classify.Continuous, onTm, 100.01, good, 100.00, true, Result{Eligible: true, Rule: RuleMidpoint, Side: BidSide}},
		{"at mid, tick up", classify.Continuous, onTm, 100.05, good, 100.03, true, Result{Eligible: true, Rule: RuleTick, Side: AskSide}},
		{"at mid, tick down", classify.Continuous, onTm, 100.05, good, 100.07, true, Result{Eligible: true, Rule: RuleTick, Side: BidSide}},
		{"at mid, no prior price", classify.Continuous, onTm, 100.05, good, 0, false, Result{Eligible: true, Rule: RuleTick}},
		{"at mid, equal to ref (State never produces this; defined anyway)", classify.Continuous, onTm, 100.05, good, 100.05, true, Result{Eligible: true, Rule: RuleTick}},
		{"crossed book", classify.Continuous, onTm, 100.05, Book{Bid: 100.10, Ask: 100.00, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"zero bid", classify.Continuous, onTm, 100.05, Book{Bid: 0, Ask: 100.10, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"zero ask", classify.Continuous, onTm, 100.05, Book{Bid: 100.00, Ask: 0, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"unusable quote condition", classify.Continuous, onTm, 100.10, Book{Bid: 100.00, Ask: 100.10, Usable: false}, 100.03, true, Result{Eligible: true}},
		{"late by 1ns over tolerance", classify.Continuous, sip - LateTolerance - 1, 100.10, good, 0, false, Result{Eligible: true, Late: true}},
		{"exactly at tolerance is on time", classify.Continuous, sip - LateTolerance, 100.10, good, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: AskSide}},
		{"PartTs=0", classify.Continuous, 0, 100.10, good, 0, false, Result{Eligible: true, Late: true}},
		{"ineligible BLOCK (print-inclusion: outside the quote)", classify.Block, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible NON_PRICE_FORMING", classify.NonPriceForming, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible CROSS_OPEN", classify.CrossOpen, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible DUPLICATE", classify.Duplicate, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible NON_FLOW", classify.NonFlow, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible UNKNOWN", classify.Unknown, onTm, 100.10, good, 0, false, Result{}},
		{"locked, above", classify.Continuous, onTm, 100.01, locked, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: AskSide}},
		{"locked, below", classify.Continuous, onTm, 99.99, locked, 0, false, Result{Eligible: true, Rule: RuleQuote, Side: BidSide}},
		{"locked, at price = midpoint → tick up", classify.Continuous, onTm, 100.00, locked, 99.98, true, Result{Eligible: true, Rule: RuleTick, Side: AskSide}},
		{"locked, at price, no prior", classify.Continuous, onTm, 100.00, locked, 0, false, Result{Eligible: true, Rule: RuleTick}},
	}
	for _, c := range cases {
		got := Classify(c.class, sip, c.partTs, c.price, c.book, c.ref, c.hasRef)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if !got.Eligible {
			if got.Late || got.Rule != RuleNone || got.Side != Unclassified {
				t.Errorf("%s: ineligible print carried flags: %+v", c.name, got)
			}
			continue
		}
		// Exactly one bin: late | side (quote/mid/tick) | no-book | tick-unclassified.
		bins := 0
		if got.Late {
			bins++
		}
		if got.Side != Unclassified {
			bins++
		}
		if !got.Late && got.Side == Unclassified {
			bins++
		}
		if bins != 1 {
			t.Errorf("%s: landed in %d bins: %+v", c.name, bins, got)
		}
		if got.Side != Unclassified && got.Rule == RuleNone {
			t.Errorf("%s: side without a rule: %+v", c.name, got)
		}
		if got.Late && got.Rule != RuleNone {
			t.Errorf("%s: late print reached a rule: %+v", c.name, got)
		}
	}
}

// pipe runs one quote through the real pipeline so ingest stamps the book
// (prices, ts, usability) that the cascade reads.
func pipe(t *testing.T, table *ingest.Table, st *ingest.SymbolState, bid, ask float64, ts int64, conds ...int32) {
	t.Helper()
	p := ingest.NewPipeline(table, 16)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	q := ingest.Msg{Kind: ingest.KindQuote}
	q.Quote = ingest.Quote{State: st, BidPrice: bid, AskPrice: ask, SipTs: ts}
	copy(q.Quote.Cond[:], conds)
	q.Quote.NCond = len(conds)
	p.Submit(q)
	p.Close()
	<-done
}

// TestStateTickReference: the reference is the last DIFFERENT eligible
// on-time price before the print; same-price runs look back; late and
// ineligible prints do not advance it; quote-classified prints do.
func TestStateTickReference(t *testing.T) {
	table := ingest.NewTable([]string{"X"})
	st := table.Lookup("X")
	var s State
	tr := func(price float64, partTs int64, conds ...int32) *ingest.Trade {
		x := &ingest.Trade{State: st, Price: price, Size: 1, SipTs: sip, PartTs: partTs}
		copy(x.Cond[:], conds)
		x.NCond = len(conds)
		return x
	}
	// No quote yet: book invalid → unclassified, but the reference advances.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); r.Side != Unclassified || r.Rule != RuleNone {
		t.Fatalf("no book: %+v", r)
	}
	if !s.hasLast || s.lastPrice != 10.10 || s.hasTickRef {
		t.Fatalf("state after first print: %+v", s)
	}
	pipe(t, table, st, 10.00, 10.20, sip-1) // mid 10.10
	if !BookOf(st).Usable {
		t.Fatal("quote with no conditions must be usable")
	}
	// 10.10 = mid, same price as last: no prior different price → tick, unclassified.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); r.Rule != RuleTick || r.Side != Unclassified {
		t.Errorf("same-price run with no prior different price: %+v", r)
	}
	// 10.12 inside above mid → midpoint rule, AskSide; reference advances.
	if r := s.Classify(tr(10.12, onTm), classify.Continuous); r.Rule != RuleMidpoint || r.Side != AskSide {
		t.Errorf("midpoint up: %+v", r)
	}
	// 10.10 at mid: ref = 10.12 → down → BidSide by tick.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); r.Rule != RuleTick || r.Side != BidSide {
		t.Errorf("tick down: %+v", r)
	}
	// 10.10 again: ref = 10.12 (look back past the run) → down.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); r.Rule != RuleTick || r.Side != BidSide {
		t.Errorf("tick down via look-back: %+v", r)
	}
	// Late print at 10.02 does not move the reference.
	if r := s.Classify(tr(10.02, 0), classify.Continuous); !r.Late {
		t.Errorf("late: %+v", r)
	}
	// Ineligible print at 10.02 does not move the reference either.
	if r := s.Classify(tr(10.02, onTm, 22), classify.NonPriceForming); r.Eligible {
		t.Errorf("ineligible: %+v", r)
	}
	if s.lastPrice != 10.10 || s.tickRef != 10.12 {
		t.Errorf("reference after late/ineligible: %+v", s)
	}
	// At the ask: quote rule — and the reference still advances.
	if r := s.Classify(tr(10.20, onTm), classify.Continuous); r.Rule != RuleQuote || r.Side != AskSide {
		t.Errorf("at ask: %+v", r)
	}
	if s.lastPrice != 10.20 || s.tickRef != 10.10 {
		t.Errorf("reference after quote-classified print: %+v", s)
	}
}

// TestBookAsOfArrival: the print is classified against the NBBO ingest has
// installed when it arrives — the latest quote, whatever its ts — and the
// book's usability is the one ingest stamped (review S8).
func TestBookAsOfArrival(t *testing.T) {
	table := ingest.NewTable([]string{"X"})
	st := table.Lookup("X")
	var s State
	tr := &ingest.Trade{State: st, Price: 10.20, Size: 1, SipTs: sip, PartTs: onTm}
	pipe(t, table, st, 10.10, 10.30, sip-1) // 10.20 is this book's mid
	pipe(t, table, st, 10.00, 10.20, sip)   // latest: 10.20 is the ask, even at the print's own ts
	if r := s.Classify(tr, classify.Continuous); r.Rule != RuleQuote || r.Side != AskSide {
		t.Errorf("latest book: %+v (book %+v)", r, BookOf(st))
	}
	// Unusable latest quote (Non-Firm) as ingest stamped it → no-book.
	s = State{}
	pipe(t, table, st, 10.00, 10.20, sip+1, 20)
	if r := s.Classify(tr, classify.Continuous); r.Rule != RuleNone || r.Side != Unclassified {
		t.Errorf("unusable book via NBBO.Usable: %+v", r)
	}
}

func TestStatsLine(t *testing.T) {
	s := Stats{Eligible: 1000, Ask: 400, Bid: 350, Quote: 700, Tick: 120, Late: 60}
	got := s.Line(0)
	want := "aggressor: eligible=1000 ask=40.0% bid=35.0% quote=70.0% tick=12.0% late=6.0% nobook=19.0% unclassified(incl late)=25.0%"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := s.Line(3); !strings.HasPrefix(got, "!! aggressor:") {
		t.Errorf("cond-overflow prefix missing: %q", got)
	}
	if got := (Stats{}).Line(0); !strings.HasPrefix(got, "aggressor: eligible=0 ask=n/a") {
		t.Errorf("empty: %q", got)
	}
	// Scope law: every rendered string is a measurement.
	rendered := got + NotTimeOrderedLine() + AskSide.String() + BidSide.String() + Unclassified.String()
	for _, banned := range []string{"buy", "sell", "Buy", "Sell"} {
		if strings.Contains(rendered, banned) {
			t.Errorf("rendered string contains %q — scope law", banned)
		}
	}
}
