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

// TestCascadeBranches: every branch the story lists lands in exactly one
// counter (MO-2 done-when #1).
func TestCascadeBranches(t *testing.T) {
	good := Book{Bid: 100.00, Ask: 100.10, Usable: true}
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
		{"at ask", classify.Continuous, onTm, 100.10, good, 0, false, Result{Eligible: true, Side: AskSide}},
		{"above ask", classify.Continuous, onTm, 100.25, good, 0, false, Result{Eligible: true, Side: AskSide}},
		{"at bid", classify.Continuous, onTm, 100.00, good, 0, false, Result{Eligible: true, Side: BidSide}},
		{"below bid", classify.Continuous, onTm, 99.90, good, 0, false, Result{Eligible: true, Side: BidSide}},
		{"inside, tick up", classify.Continuous, onTm, 100.05, good, 100.03, true, Result{Eligible: true, TickRule: true, Side: AskSide}},
		{"inside, tick down", classify.Continuous, onTm, 100.05, good, 100.07, true, Result{Eligible: true, TickRule: true, Side: BidSide}},
		{"inside, no prior price", classify.Continuous, onTm, 100.05, good, 0, false, Result{Eligible: true, TickRule: true}},
		{"inside, equal to ref (State never produces this; defined anyway)", classify.Continuous, onTm, 100.05, good, 100.05, true, Result{Eligible: true, TickRule: true}},
		{"crossed book", classify.Continuous, onTm, 100.05, Book{Bid: 100.10, Ask: 100.00, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"zero bid", classify.Continuous, onTm, 100.05, Book{Bid: 0, Ask: 100.10, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"zero ask", classify.Continuous, onTm, 100.05, Book{Bid: 100.00, Ask: 0, Usable: true}, 100.03, true, Result{Eligible: true}},
		{"unusable quote condition", classify.Continuous, onTm, 100.10, Book{Bid: 100.00, Ask: 100.10, Usable: false}, 100.03, true, Result{Eligible: true}},
		{"late by 1ns over tolerance", classify.Continuous, sip - LateTolerance - 1, 100.10, good, 0, false, Result{Eligible: true, Late: true}},
		{"exactly at tolerance is on time", classify.Continuous, sip - LateTolerance, 100.10, good, 0, false, Result{Eligible: true, Side: AskSide}},
		{"PartTs=0", classify.Continuous, 0, 100.10, good, 0, false, Result{Eligible: true, Late: true}},
		{"BLOCK is eligible", classify.Block, onTm, 100.10, good, 0, false, Result{Eligible: true, Side: AskSide}},
		{"ineligible NON_PRICE_FORMING", classify.NonPriceForming, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible CROSS_OPEN", classify.CrossOpen, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible DUPLICATE", classify.Duplicate, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible NON_FLOW", classify.NonFlow, onTm, 100.10, good, 0, false, Result{}},
		{"ineligible UNKNOWN", classify.Unknown, onTm, 100.10, good, 0, false, Result{}},
		{"locked book, at price: ask wins (spec rule 4 order)", classify.Continuous, onTm, 100.00, Book{Bid: 100.00, Ask: 100.00, Usable: true}, 0, false, Result{Eligible: true, Side: AskSide}},
	}
	for _, c := range cases {
		got := Classify(c.class, sip, c.partTs, c.price, c.book, c.ref, c.hasRef)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if !got.Eligible {
			if got.Late || got.TickRule || got.Side != Unclassified {
				t.Errorf("%s: ineligible print carried flags: %+v", c.name, got)
			}
			continue
		}
		// Exactly one bin: late | ask | bid | unclassified-not-late.
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
		if got.Late && got.TickRule {
			t.Errorf("%s: late print reached the tick rule: %+v", c.name, got)
		}
	}
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
	if r := s.Classify(tr(10.05, onTm), classify.Continuous); r.Side != Unclassified || r.TickRule {
		t.Fatalf("no book: %+v", r)
	}
	if !s.hasLast || s.lastPrice != 10.05 || s.hasTickRef {
		t.Fatalf("state after first print: %+v", s)
	}
	// Install a wide book through the real pipeline path (applyQuote is
	// unexported) so the observer ordering contract is what is tested.
	p := ingest.NewPipeline(table, 16)
	p.SetObserver(&recorder{s: &s})
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	q := ingest.Msg{Kind: ingest.KindQuote}
	q.Quote = ingest.Quote{State: st, BidPrice: 10.00, AskPrice: 10.20, SipTs: sip}
	p.Submit(q)
	p.Close()
	<-done
	if !s.quoteUsable {
		t.Fatal("quote with no conditions must be usable")
	}
	// 10.05 again (same price as last): no prior different price → tick,
	// unclassified.
	if r := s.Classify(tr(10.05, onTm), classify.Continuous); !r.TickRule || r.Side != Unclassified {
		t.Errorf("same-price run with no prior different price: %+v", r)
	}
	// 10.10 inside: ref = 10.05 → up → AskSide.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); !r.TickRule || r.Side != AskSide {
		t.Errorf("tick up: %+v", r)
	}
	// 10.10 again: ref = 10.05 (look back past the run) → up → AskSide.
	if r := s.Classify(tr(10.10, onTm), classify.Continuous); !r.TickRule || r.Side != AskSide {
		t.Errorf("tick up via look-back: %+v", r)
	}
	// Late print at 10.02 does not move the reference.
	if r := s.Classify(tr(10.02, 0), classify.Continuous); !r.Late {
		t.Errorf("late: %+v", r)
	}
	// Ineligible print at 10.02 does not move the reference either.
	if r := s.Classify(tr(10.02, onTm, 22), classify.NonPriceForming); r.Eligible {
		t.Errorf("ineligible: %+v", r)
	}
	// 10.08 inside: ref is still 10.10 → down → BidSide.
	if r := s.Classify(tr(10.08, onTm), classify.Continuous); !r.TickRule || r.Side != BidSide {
		t.Errorf("tick down after late/ineligible: %+v", r)
	}
	// At the ask: quote rule, no tick — and the reference still advances.
	if r := s.Classify(tr(10.20, onTm), classify.Continuous); r.TickRule || r.Side != AskSide {
		t.Errorf("at ask: %+v", r)
	}
	if s.lastPrice != 10.20 || s.tickRef != 10.08 {
		t.Errorf("reference after quote-classified print: %+v", s)
	}
}

type recorder struct{ s *State }

func (r *recorder) ObserveTrade(*ingest.Trade)   {}
func (r *recorder) ObserveQuote(q *ingest.Quote) { r.s.ObserveQuote(q) }

func TestObserveQuoteUsability(t *testing.T) {
	var s State
	q := &ingest.Quote{}
	s.ObserveQuote(q)
	if !s.quoteUsable {
		t.Error("empty conditions: want usable")
	}
	q.Cond[0], q.NCond = 20, 1 // Non-Firm
	s.ObserveQuote(q)
	if s.quoteUsable {
		t.Error("non-firm: want unusable")
	}
	q.Cond[0], q.NCond = 1, 1
	s.ObserveQuote(q)
	if !s.quoteUsable {
		t.Error("regular: want usable again")
	}
}

func TestStatsLine(t *testing.T) {
	s := Stats{Eligible: 1000, Ask: 400, Bid: 350, Tick: 120, Late: 60}
	got := s.Line(0)
	want := "aggressor: eligible=1000 ask=40.0% bid=35.0% tick=12.0% late=6.0% unclassified=25.0%"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := s.Line(3); !strings.HasPrefix(got, "!! aggressor:") {
		t.Errorf("cond-overflow prefix missing: %q", got)
	}
	if got := (Stats{}).Line(0); got != "aggressor: eligible=0 ask=n/a bid=n/a tick=n/a late=n/a unclassified=n/a" {
		t.Errorf("empty: %q", got)
	}
	// Scope law: every rendered string is a measurement.
	rendered := got + AskSide.String() + BidSide.String() + Unclassified.String()
	for _, banned := range []string{"buy", "sell", "Buy", "Sell"} {
		if strings.Contains(rendered, banned) {
			t.Errorf("rendered string contains %q — scope law", banned)
		}
	}
}
