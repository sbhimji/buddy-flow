package ingest

import "testing"

func TestApplyQuoteArrivalOrderWins(t *testing.T) {
	table := NewTable([]string{"NVDA"})
	st := table.Lookup("NVDA")

	q1 := Quote{State: st, BidPrice: 100.01, AskPrice: 100.03, BidSize: 5, AskSize: 7, SipTs: 1000, Seq: 10}
	st.applyQuote(&q1)
	if n := st.NBBO(); n.BidPrice != 100.01 || n.AskPrice != 100.03 || n.Seq != 10 {
		t.Fatalf("first quote not installed: %+v", n)
	}

	// Sequence regression: counted for monitoring, but STILL INSTALLED —
	// arrival order wins (a stale-skip guard could freeze the book forever
	// on one corrupt-high sequence or a vendor reset; see applyQuote).
	regress := Quote{State: st, BidPrice: 99.00, AskPrice: 99.02, SipTs: 900, Seq: 9}
	st.applyQuote(&regress)
	if n := st.NBBO(); n.BidPrice != 99.00 || n.Seq != 9 {
		t.Fatalf("regressed quote must still install (arrival order wins): %+v", n)
	}
	if got := st.SeqRegressQuotes.Load(); got != 1 {
		t.Fatalf("SeqRegressQuotes = %d, want 1", got)
	}
	if got := st.Quotes.Load(); got != 2 {
		t.Fatalf("Quotes = %d, want 2", got)
	}

	// lastQuoteSeq tracked the max (10), so seq 11 is not a regression;
	// a corrupt-high value earlier can therefore never freeze the book.
	q2 := Quote{State: st, BidPrice: 100.02, AskPrice: 100.04, BidSize: 3, AskSize: 2, SipTs: 1100, Seq: 11}
	st.applyQuote(&q2)
	if n := st.NBBO(); n.BidPrice != 100.02 || n.AskPrice != 100.04 || n.Ts != 1100 {
		t.Fatalf("newer quote not installed: %+v", n)
	}
	if got := st.SeqRegressQuotes.Load(); got != 1 {
		t.Fatalf("SeqRegressQuotes = %d, want still 1", got)
	}
}

func TestApplyTradeCountsAndRegressions(t *testing.T) {
	table := NewTable([]string{"SPY"})
	st := table.Lookup("SPY")
	for _, seq := range []int64{1, 2, 5, 4, 6} { // 4 after 5 is a regression
		tr := Trade{State: st, Seq: seq}
		st.applyTrade(&tr)
	}
	if got := st.Trades.Load(); got != 5 {
		t.Fatalf("Trades = %d, want 5", got)
	}
	if got := st.SeqRegressTrades.Load(); got != 1 {
		t.Fatalf("SeqRegressTrades = %d, want 1", got)
	}
}

func TestPipelineProcessesEverything(t *testing.T) {
	table := NewTable([]string{"NVDA", "SPY"})
	p := NewPipeline(table, 16)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	const n = 1000
	nvda, spy := table.Lookup("NVDA"), table.Lookup("SPY")
	for i := 0; i < n; i++ {
		p.Submit(Msg{Kind: KindTrade, Trade: Trade{State: nvda, Seq: int64(i)}})
		p.Submit(Msg{Kind: KindQuote, Quote: Quote{State: spy, Seq: int64(i), BidPrice: 1, AskPrice: 2}})
	}
	p.Close()
	<-done

	if got := p.Processed.Load(); got != 2*n {
		t.Fatalf("Processed = %d, want %d", got, 2*n)
	}
	if got := nvda.Trades.Load(); got != n {
		t.Fatalf("NVDA trades = %d, want %d", got, n)
	}
	if got := spy.Quotes.Load(); got != n {
		t.Fatalf("SPY quotes = %d, want %d", got, n)
	}
	if got := p.MaxQueueDepth.Load(); got > 16 {
		t.Fatalf("MaxQueueDepth = %d exceeds queue size 16", got)
	}
}

func TestLookupOrCreateStressMode(t *testing.T) {
	table := NewTable([]string{"NVDA"})
	if table.Lookup("ZZZT") != nil {
		t.Fatal("non-universe symbol should Lookup to nil")
	}
	a := table.LookupOrCreate("ZZZT")
	b := table.LookupOrCreate("ZZZT")
	if a == nil || a != b {
		t.Fatal("LookupOrCreate must return one stable state per symbol")
	}
	if table.Len() != 1 {
		t.Fatalf("stress-mode extras must not join the universe; Len = %d", table.Len())
	}
}
