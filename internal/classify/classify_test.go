package classify

import (
	"reflect"
	"testing"
)

// Mirrors the normative table in docs/foundations/print-inclusion.md row by row.
// A failure here means code and policy doc have diverged.
var tableCases = []struct {
	id   int32
	want Class
}{
	{1, Continuous}, {2, Duplicate}, {3, Continuous}, {4, Continuous},
	{5, NonPriceForming}, {6, Continuous}, {7, NonFlow}, {8, CrossClose},
	{9, Block}, {10, NonPriceForming}, {11, Continuous}, {12, NonFlow},
	{13, NonFlow}, {14, Continuous}, {15, NonFlow}, {16, NonFlow},
	{17, CrossOpen}, {18, CrossReopen}, {19, CrossClose}, {20, NonFlow},
	{21, NonPriceForming}, {22, NonPriceForming}, {23, Continuous}, {24, Block},
	{25, CrossOpen}, {27, Continuous}, {28, CrossReopen}, {29, NonFlow},
	{30, Continuous}, {31, Continuous}, {32, NonPriceForming}, {33, NonPriceForming},
	{34, Continuous}, {35, Continuous}, {36, Continuous}, {37, Continuous},
	{38, NonFlow}, {52, NonPriceForming}, {53, NonPriceForming}, {55, CrossOpen},
}

func TestNormativeTable(t *testing.T) {
	for _, tc := range tableCases {
		got, unknown := Classify([]int32{tc.id})
		if got != tc.want {
			t.Errorf("id %d: got %v, want %v", tc.id, got, tc.want)
		}
		if unknown != nil {
			t.Errorf("id %d: unexpectedly reported unknown %v", tc.id, unknown)
		}
	}
	if len(tableCases) != len(saleConditions) {
		t.Errorf("test table has %d rows, saleConditions has %d — keep both in lockstep with the policy doc",
			len(tableCases), len(saleConditions))
	}
}

func TestRegularSaleDefault(t *testing.T) {
	for _, conds := range [][]int32{nil, {}} {
		if got, _ := Classify(conds); got != Continuous {
			t.Errorf("empty c[] = Regular Sale: got %v, want CONTINUOUS", got)
		}
	}
}

func TestNeutralConditionsIgnored(t *testing.T) {
	// Neutral-only print behaves as a regular sale.
	if got, unknown := Classify([]int32{41, 60, 62}); got != Continuous || unknown != nil {
		t.Errorf("neutral-only: got %v (unknown %v), want CONTINUOUS with no unknowns", got, unknown)
	}
	// Neutral codes never change a sale condition's class.
	if got, _ := Classify([]int32{32, 41}); got != NonPriceForming {
		t.Errorf("32+41: got %v, want NON_PRICE_FORMING", got)
	}
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		conds []int32
		want  Class
	}{
		{[]int32{37, 32}, NonPriceForming}, // odd lot + sold OOS: exclusion dominates
		{[]int32{14, 2}, Duplicate},        // ISO + average price
		{[]int32{9, 7}, NonFlow},           // block cross + cash sale
		{[]int32{25, 37}, CrossOpen},       // opening print + odd lot
		{[]int32{9, 37}, Block},            // cross trade + odd lot
		{[]int32{25, 32}, NonPriceForming}, // cross + sold OOS: exclusion beats cross
		{[]int32{18, 25}, CrossReopen},     // reopen beats open (deterministic cross order)
		{[]int32{12, 2}, NonFlow},          // NON_FLOW is the strongest sale class
	}
	for _, tc := range cases {
		if got, _ := Classify(tc.conds); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.conds, got, tc.want)
		}
	}
}

func TestUnknownQuarantine(t *testing.T) {
	// Unknown alone.
	got, unknown := Classify([]int32{40})
	if got != Unknown || !reflect.DeepEqual(unknown, []int32{40}) {
		t.Errorf("id 40 (Held, not in table): got %v (unknown %v), want UNKNOWN [40]", got, unknown)
	}
	// Unknown dominates everything, even NON_FLOW.
	if got, _ := Classify([]int32{12, 46}); got != Unknown {
		t.Errorf("12+46: got %v, want UNKNOWN (quarantine dominates)", got)
	}
	// All unknown IDs are reported for the tripwire.
	_, unknown = Classify([]int32{40, 14, 46})
	if !reflect.DeepEqual(unknown, []int32{40, 46}) {
		t.Errorf("tripwire IDs: got %v, want [40 46]", unknown)
	}
}

func TestMonitorLateness(t *testing.T) {
	cases := []struct {
		conds []int32
		want  bool
	}{
		{[]int32{32}, true},      // sold OOS: NON_PRICE_FORMING, logged
		{[]int32{22}, true},      // prior reference: logged (lateness ≈ 0, still recorded)
		{[]int32{30}, true},      // sold last: CONTINUOUS but monitored
		{[]int32{31, 41}, true},  // sold last + stopped, neutral alongside
		{[]int32{14}, false},     // plain ISO
		{nil, false},             // regular sale
		{[]int32{9}, false},      // block: block log, not lateness log
		{[]int32{30, 32}, true},  // both flavors: NON_PRICE_FORMING wins, still logged
		{[]int32{30, 12}, false}, // sold last + Form T: NON_FLOW, out of session, not logged
	}
	for _, tc := range cases {
		class, _ := Classify(tc.conds)
		if got := MonitorLateness(class, tc.conds); got != tc.want {
			t.Errorf("%v (class %v): MonitorLateness=%v, want %v", tc.conds, class, got, tc.want)
		}
	}
}
