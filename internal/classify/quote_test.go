package classify

import "testing"

func TestQuoteUsable(t *testing.T) {
	cases := []struct {
		name  string
		conds []int32
		want  bool
	}{
		{"empty (regular, no c on wire)", nil, true},
		{"1 regular two-sided", []int32{1}, true},
		{"2 regular one-sided", []int32{2}, true},
		{"3 slow ask", []int32{3}, true},
		{"12 manual bid and ask", []int32{12}, true},
		{"13 opening", []int32{13}, true},
		{"82 sip generated", []int32{82}, true},
		{"15 closed", []int32{15}, false},
		{"17 fast trading", []int32{17}, false},
		{"18 trading range indication", []int32{18}, false},
		{"20 non-firm", []int32{20}, false},
		{"21 news dissemination", []int32{21}, false},
		{"27 news pending", []int32{27}, false},
		{"43 luld pause", []int32{43}, false},
		{"84 crossed", []int32{84}, false},
		{"85 locked", []int32{85}, false},
		{"unknown id 34 (seen on 08-24) quarantines", []int32{34}, false},
		{"usable + unusable → unusable", []int32{1, 20}, false},
	}
	for _, c := range cases {
		if got := QuoteUsable(c.conds); got != c.want {
			t.Errorf("%s: QuoteUsable(%v) = %v, want %v", c.name, c.conds, got, c.want)
		}
	}
	// Every ID in the unusable table must also be a known ID? No — the two
	// tables partition the vendor list: an unusable ID is known-unusable, a
	// known ID is known-usable, and neither may appear in both.
	for id := range quoteUnusable {
		if quoteKnown[id] {
			t.Errorf("id %d is in both quoteUnusable and quoteKnown", id)
		}
	}
}
