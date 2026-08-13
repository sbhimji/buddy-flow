package feed

import (
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/ingest"
)

// realFrame is copied verbatim from the first live capture (2026-08-13
// 15:39 ET) — quote with indicators + pt, trade with the undocumented "ds"
// field and TRF attribution. The decoder must take the real wire shape,
// unknown fields and all.
const realFrame = `[{"ev":"Q","sym":"NVDA","i":[604],"bx":10,"ax":15,"bp":225.62,"ap":225.64,"bs":100,"as":500,"t":1786649956446,"pt":1786649956446,"q":106078192,"z":3},{"ev":"T","sym":"NVDA","i":"40161","x":4,"p":225.63,"s":129,"t":1786649956459,"pt":1786649956458,"q":9059424,"z":3,"trfi":202,"trft":1786649956459,"ds":"129.0"},{"ev":"status","status":"success","message":"subscribed"}]`

func newTestPipeline(t *testing.T, syms ...string) (*ingest.Table, *ingest.Pipeline, func() ingest.NBBO) {
	t.Helper()
	table := ingest.NewTable(syms)
	p := ingest.NewPipeline(table, 64)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	return table, p, func() ingest.NBBO {
		p.Close()
		<-done
		return table.Lookup(syms[0]).NBBO()
	}
}

func TestDecodeFrameRealWire(t *testing.T) {
	table, p, finish := newTestPipeline(t, "NVDA")
	var stats LiveStats
	decodeFrame([]byte(realFrame), table, p, &stats)
	nbbo := finish()

	if stats.Quotes != 1 || stats.Trades != 1 || stats.Status != 1 || stats.DecodeErrs != 0 {
		t.Fatalf("stats = %+v, want 1 quote, 1 trade, 1 status, 0 errs", stats)
	}
	if nbbo.BidPrice != 225.62 || nbbo.AskPrice != 225.64 || nbbo.BidSize != 100 || nbbo.AskSize != 500 {
		t.Fatalf("NBBO = %+v, want the frame's book", nbbo)
	}
	// ms → ns scaling (canonical unit; types.go).
	if nbbo.Ts != 1786649956446*1_000_000 {
		t.Fatalf("NBBO.Ts = %d, want ms-scaled ns", nbbo.Ts)
	}
	if got := table.Lookup("NVDA").Trades.Load(); got != 1 {
		t.Fatalf("trades = %d, want 1", got)
	}
}

func TestDecodeFrameUnknownSymbolAndGarbage(t *testing.T) {
	table, p, finish := newTestPipeline(t, "SPY")
	var stats LiveStats
	decodeFrame([]byte(`[{"ev":"T","sym":"MSFT","p":1,"s":1,"t":1,"q":1}]`), table, p, &stats)
	decodeFrame([]byte(`not json at all`), table, p, &stats)
	finish()
	if stats.Unknown != 1 {
		t.Fatalf("Unknown = %d, want 1 (MSFT outside table)", stats.Unknown)
	}
	if stats.DecodeErrs != 1 {
		t.Fatalf("DecodeErrs = %d, want 1 (garbage must count, not crash)", stats.DecodeErrs)
	}
}

func TestDecodeQuoteCondShapes(t *testing.T) {
	// The wire sends "c" as a bare int; flat files imply multiple conditions
	// exist, so an array shape must also decode. Absent "c" (the common case
	// on the real wire — see realFrame) means zero conditions.
	cases := []struct {
		name string
		raw  string
		want []int32
	}{
		{"absent", ``, nil},
		{"bare-int", `1`, []int32{1}},
		{"array", `[1,81]`, []int32{1, 81}},
		{"garbage", `"x"`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out [ingest.MaxConditions]int32
			n := decodeQuoteCond([]byte(c.raw), &out)
			if n != len(c.want) {
				t.Fatalf("n = %d, want %d", n, len(c.want))
			}
			for i, w := range c.want {
				if out[i] != w {
					t.Fatalf("out[%d] = %d, want %d", i, out[i], w)
				}
			}
		})
	}
	// And through the full decode path: a bare-int condition quote flows.
	table := ingest.NewTable([]string{"SPY"})
	p := ingest.NewPipeline(table, 4)
	var stats LiveStats
	decodeFrame([]byte(`[{"ev":"Q","sym":"SPY","c":1,"bp":1,"ap":2,"t":5,"q":2}]`), table, p, &stats)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	p.Close()
	<-done
	if stats.Quotes != 1 || stats.DecodeErrs != 0 || table.Lookup("SPY").Quotes.Load() != 1 {
		t.Fatalf("bare-int-cond quote did not flow: stats=%+v", stats)
	}
}

func TestSubscribeMessagesChunking(t *testing.T) {
	// 3 symbols → 6 topics; chunk 4 → [4, 2]. A boundary bug here silently
	// drops the tail symbols' subscriptions on the live feed.
	msgs := subscribeMessages([]string{"A", "B", "C"}, 4)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %v", len(msgs), msgs)
	}
	want0 := `{"action":"subscribe","params":"T.A,Q.A,T.B,Q.B"}`
	want1 := `{"action":"subscribe","params":"T.C,Q.C"}`
	if msgs[0] != want0 || msgs[1] != want1 {
		t.Fatalf("got %v, want [%s %s]", msgs, want0, want1)
	}
}

func TestCaptureRoundtripAndTornTail(t *testing.T) {
	dir := t.TempDir()
	w, err := capture.NewWriter(dir, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Control("start", "test"); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(123456789, []byte(realFrame)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.Manifest{Date: "2026-01-02"}); err != nil {
		t.Fatal(err)
	}
	path := capture.StreamPath(dir, "2026-01-02")

	// Simulate a power-loss torn tail: append half a record, no newline flush.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`999999 [{"ev":"T","sym":"NV`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Replay: the good frames flow, the torn tail is skipped, no error.
	table, p, finish := newTestPipeline(t, "NVDA")
	stats, err := StreamCapture(path, p)
	finish()
	if err != nil {
		t.Fatalf("replay must tolerate a torn tail, got: %v", err)
	}
	// start control + real frame = 2 intact records; torn record skipped.
	if stats.Frames != 2 {
		t.Fatalf("Frames = %d, want 2 (control + data; torn tail skipped)", stats.Frames)
	}
	if stats.Trades != 1 || stats.Quotes != 1 {
		t.Fatalf("stats = %+v, want the data frame decoded", stats)
	}
	if got := table.Lookup("NVDA").Quotes.Load(); got != 1 {
		t.Fatalf("NVDA quotes = %d, want 1", got)
	}

	// Manifest exists after a clean close.
	if _, err := os.Stat(filepath.Join(dir, "2026-01-02", "manifest.json")); err != nil {
		t.Fatalf("manifest missing after clean close: %v", err)
	}
}
