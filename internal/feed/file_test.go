package feed

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"buddy-flow/internal/ingest"
)

func TestParseConditions(t *testing.T) {
	var overflow atomic.Int64
	var out [ingest.MaxConditions]int32
	cases := []struct {
		in   string
		want []int32
	}{
		{"", nil},
		{"14", []int32{14}},
		{"14,12,37,41", []int32{14, 12, 37, 41}},
		{" 14, 12 ", []int32{14, 12}},
		{"14,x,37", []int32{14, -1, 37}}, // malformed token surfaces as -1, never vanishes
	}
	for _, c := range cases {
		n := parseConditions(c.in, &out, &overflow)
		if n != len(c.want) {
			t.Fatalf("%q: n = %d, want %d", c.in, n, len(c.want))
		}
		for i, w := range c.want {
			if out[i] != w {
				t.Fatalf("%q: out[%d] = %d, want %d", c.in, i, out[i], w)
			}
		}
	}
	if overflow.Load() != 0 {
		t.Fatalf("unexpected overflow count %d", overflow.Load())
	}
	// 10 IDs into an 8-slot array: 8 kept, counter bumps ONCE (it counts
	// overflowing messages, not surplus tokens — review finding #9).
	n := parseConditions("1,2,3,4,5,6,7,8,9,10", &out, &overflow)
	if n != ingest.MaxConditions || overflow.Load() != 1 {
		t.Fatalf("overflow: n=%d count=%d, want %d and 1", n, overflow.Load(), ingest.MaxConditions)
	}
}

func TestStreamFileMalformedCounter(t *testing.T) {
	// Malformed numerics must be counted, not silently zeroed: a mangled
	// ask_price would otherwise install ask 0.0 as a legal book. Empty
	// fields are legal (vendor convention) and must NOT count.
	path := writeCSV(t, "quotes.csv", []string{
		"ticker,ask_exchange,ask_price,ask_size,bid_exchange,bid_price,bid_size,conditions,participant_timestamp,sequence_number,sip_timestamp,tape",
		`SPY,11,50x.02,3,12,500.01,5,1,900,1,1000,1`, // mangled ask_price → 1 malformed
		`SPY,11,500.04,,12,500.03,4,1,900,2,2000,1`,  // empty ask_size → legal, no count
	})
	table := ingest.NewTable([]string{"SPY"})
	p := ingest.NewPipeline(table, 16)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	st, err := StreamFile(path, ingest.KindQuote, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	<-done

	if st.Malformed != 1 {
		t.Fatalf("Malformed = %d, want 1 (mangled counts, empty does not)", st.Malformed)
	}
	if st.Submitted != 2 {
		t.Fatalf("Submitted = %d, want 2 (malformed rows still flow, visibly)", st.Submitted)
	}
}

// writeCSV drops a small flat-file fixture with vendor-realistic columns in a
// deliberately different order than the header map expects nothing of —
// column order is not guaranteed (handoff), so the fixture shuffles it.
func writeCSV(t *testing.T, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStreamFileTrades(t *testing.T) {
	// sip_timestamps: 1000ns and 2000ns; MSFT is outside the 2-symbol universe.
	path := writeCSV(t, "trades.csv", []string{
		"price,ticker,conditions,sip_timestamp,participant_timestamp,exchange,sequence_number,size,tape,trf_id",
		`100.50,NVDA,"14,37",1000,900,4,1,152.000000,1,`,
		`100.51,NVDA,,2000,1900,4,2,3.000000,1,`,
		`430.10,MSFT,,1500,1400,11,7,10.000000,1,`,
	})
	table := ingest.NewTable([]string{"NVDA", "SPY"})
	p := ingest.NewPipeline(table, 16)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	st, err := StreamFile(path, ingest.KindTrade, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	<-done

	if st.Rows != 3 || st.Submitted != 2 || st.Filtered != 1 {
		t.Fatalf("stats = %+v, want rows=3 submitted=2 filtered=1", st)
	}
	if got := table.Lookup("NVDA").Trades.Load(); got != 2 {
		t.Fatalf("NVDA trades = %d, want 2", got)
	}
}

func TestStreamFileQuotesWindowAndNBBO(t *testing.T) {
	path := writeCSV(t, "quotes.csv", []string{
		"ticker,ask_exchange,ask_price,ask_size,bid_exchange,bid_price,bid_size,conditions,participant_timestamp,sequence_number,sip_timestamp,tape",
		`SPY,11,500.02,3,12,500.01,5,1,900,1,1000,1`,
		`SPY,11,500.04,2,12,500.03,4,1,1900,2,2000,1`,
		`SPY,11,500.99,9,12,500.98,9,1,2900,3,3000,1`, // outside window
	})
	table := ingest.NewTable([]string{"SPY"})
	p := ingest.NewPipeline(table, 16)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	st, err := StreamFile(path, ingest.KindQuote, p, Options{FromNs: 1000, ToNs: 3000})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	<-done

	if st.Rows != 3 || st.Submitted != 2 || st.Windowed != 1 {
		t.Fatalf("stats = %+v, want rows=3 submitted=2 windowed=1", st)
	}
	n := table.Lookup("SPY").NBBO()
	if n.BidPrice != 500.03 || n.AskPrice != 500.04 || n.BidSize != 4 || n.AskSize != 2 || n.Ts != 2000 {
		t.Fatalf("NBBO as of window end = %+v, want the seq-2 book", n)
	}
}
