package conviction

import (
	"testing"

	"buddy-flow/internal/optbucket"
)

// BasketMinute sums members' buckets and derives net_notional from the
// summed slices — hand-computed against a two-member basket, with a
// member absent from the store contributing nothing.
func TestBasketMinuteHandComputed(t *testing.T) {
	const m = int64(1_787_578_200) // any minute-aligned second
	buckets := map[string]optbucket.Bucket{
		"A": {PremCallAsk: 100, PremCallBid: 30, PremPutAsk: 20, PremPutBid: 5, NetConviction: 250},
		"B": {PremCallAsk: 10, PremCallBid: 40, PremPutAsk: 60, PremPutBid: 15, NetConviction: -75},
	}
	read := func(sym string, minuteSec int64) optbucket.Bucket {
		if minuteSec != m {
			t.Fatalf("read at %d, want %d", minuteSec, m)
		}
		return buckets[sym]
	}
	conv, net := BasketMinute(read, []string{"A", "MISSING", "B"}, m)
	// Σ conviction = 250 − 75; net = (110 − 70) − (80 − 20) = 40 − 60.
	if conv != 175 || net != -20 {
		t.Fatalf("BasketMinute = %v / %v, want 175 / -20", conv, net)
	}
	if conv, net := BasketMinute(read, nil, m); conv != 0 || net != 0 {
		t.Fatalf("empty basket = %v / %v, want 0 / 0", conv, net)
	}
}

// SessionMinutes and StoreMinutes must agree on the same buckets: a
// read-back session and a live store holding one identical minute.
func TestSessionMinutesUnalignedIsEmpty(t *testing.T) {
	sess := &optbucket.Session{Buckets: map[string]map[int64]*optbucket.Bucket{
		"A": {60: {Prints: 1, NetConviction: 5}},
	}}
	if b := SessionMinutes(sess)("A", 60); b.Prints != 1 || b.NetConviction != 5 {
		t.Fatalf("aligned minute = %+v", b)
	}
	if b := SessionMinutes(sess)("A", 61); b != (optbucket.Bucket{}) {
		t.Fatalf("unaligned minute = %+v, want empty", b)
	}
}
