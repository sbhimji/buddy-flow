package ingest

import "testing"

// NBBO.Usable is stamped by applyQuote from the quote's conditions
// (classify.QuoteUsable), so the book and its validity are one snapshot
// (MO-2 review S8).
func TestNBBOUsableStamp(t *testing.T) {
	st := &SymbolState{Symbol: "X"}
	q := &Quote{State: st, BidPrice: 1, AskPrice: 2, SipTs: 10}
	st.applyQuote(q)
	if nb := st.NBBO(); !nb.Usable || nb.Ts != 10 {
		t.Errorf("regular quote: %+v", nb)
	}
	q.Cond[0], q.NCond = 20, 1 // Non-Firm
	q.SipTs = 11
	st.applyQuote(q)
	if nb := st.NBBO(); nb.Usable || nb.Ts != 11 {
		t.Errorf("non-firm quote: %+v", nb)
	}
	q.Cond[0], q.NCond = 1, 1
	q.SipTs = 12
	st.applyQuote(q)
	if nb := st.NBBO(); !nb.Usable {
		t.Errorf("regular again: %+v", nb)
	}
}
