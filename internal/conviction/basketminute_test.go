package conviction

import (
	"testing"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
)

// BasketMinute sums members' buckets and derives net_notional from the
// summed slices — hand-computed against a two-member basket, with a
// member absent from the store contributing a legitimate zero, and any
// member reporting "not measurable" gapping the whole basket.
func TestBasketMinuteHandComputed(t *testing.T) {
	const m = int64(1_787_578_200) // any minute-aligned second
	buckets := map[string]optbucket.Bucket{
		"A": {PremCallAsk: 100, PremCallBid: 30, PremPutAsk: 20, PremPutBid: 5, NetConviction: 250},
		"B": {PremCallAsk: 10, PremCallBid: 40, PremPutAsk: 60, PremPutBid: 15, NetConviction: -75},
	}
	read := func(sym string, minuteSec int64) (optbucket.Bucket, bool) {
		if minuteSec != m {
			t.Fatalf("read at %d, want %d", minuteSec, m)
		}
		if sym == "INCOMPLETE" {
			return optbucket.Bucket{}, false
		}
		return buckets[sym], true
	}
	conv, net, ok := BasketMinute(read, []string{"A", "MISSING", "B"}, m)
	// Σ conviction = 250 − 75; net = (110 − 70) − (80 − 20) = 40 − 60.
	if !ok || conv != 175 || net != -20 {
		t.Fatalf("BasketMinute = %v / %v ok=%v, want 175 / -20 ok", conv, net, ok)
	}
	if conv, net, ok := BasketMinute(read, nil, m); !ok || conv != 0 || net != 0 {
		t.Fatalf("empty basket = %v / %v ok=%v, want 0 / 0 ok", conv, net, ok)
	}
	if _, _, ok := BasketMinute(read, []string{"A", "INCOMPLETE"}, m); ok {
		t.Fatal("a member's incomplete minute must gap the basket, never a partial sum")
	}
}

// The two adapters gate on alignment; the session adapter is ok=false on
// DeriveMinute's refusal instead of a fabricated bucket.
func TestAdaptersUnalignedIsNotOK(t *testing.T) {
	sess := &optbucket.Session{Buckets: map[string]map[int64]*optbucket.Bucket{
		"A": {60: {Prints: 1, NetConviction: 5}},
	}}
	if b, ok := SessionMinutes(sess)("A", 60); !ok || b.Prints != 1 || b.NetConviction != 5 {
		t.Fatalf("aligned minute = %+v ok=%v", b, ok)
	}
	if _, ok := SessionMinutes(sess)("A", 61); ok {
		t.Fatal("unaligned session minute must be ok=false")
	}
	if _, ok := SessionMinutes(sess)("ABSENT", 60); !ok {
		t.Fatal("a symbol absent from the file is a legitimate empty minute")
	}
	w, hash, err := optclassify.LoadWeights("../../docs/foundations/options-weights-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	store, err := optbucket.NewStore(w, "options-weights-v1@"+hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := StoreMinutes(store)("A", 61); ok {
		t.Fatal("unaligned store minute must be ok=false")
	}
	if b, ok := StoreMinutes(store)("A", 60); !ok || b != (optbucket.Bucket{}) {
		t.Fatalf("aligned empty store minute = %+v ok=%v, want empty ok", b, ok)
	}
}
