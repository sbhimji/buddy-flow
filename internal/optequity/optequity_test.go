package optequity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/conviction"
	"buddy-flow/internal/delta"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optfollow"
	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

const (
	weightsPath = "../../docs/foundations/options-weights-v1.json"
	basketsCfg  = "../../docs/foundations/morning-tape-baskets-v2.json"
)

func minuteAt(t *testing.T, hhmm int) int64 {
	t.Helper()
	sec, err := session.BucketStart("2026-08-24", hhmm)
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

// baselines builds a Baselines whose only populated basket minute is mod.
func baselines(name string, mod int, row optprofile.Row, floorConv, floorNet float64) *conviction.Baselines {
	n := session.MinutesPerSession
	p := &optprofile.Profile{Name: name, Rows: make([]optprofile.Row, n)}
	fl := &optprofile.Floors{
		MinuteOfDay: make([]int, n), NetConv: make([]float64, n), NetNotional: make([]float64, n),
		BasketNetConv: make([]float64, n), BasketNetNotional: make([]float64, n),
	}
	i := mod - session.OpenMinute
	p.Rows[i] = row
	fl.BasketNetConv[i], fl.BasketNetNotional[i] = floorConv, floorNet
	return &conviction.Baselines{Baskets: map[string]*optprofile.Profile{name: p}, Floors: fl}
}

func row(t *testing.T, s *Source, name string, members []string, atSec int64) *devview.RowCtx {
	t.Helper()
	states := make([]*ingest.SymbolState, len(members))
	for i, m := range members {
		states[i] = &ingest.SymbolState{Symbol: m}
	}
	return &devview.RowCtx{Basket: &devview.BasketRow{Name: name, States: states}, AtSec: atSec}
}

func render(s *Source, rc *devview.RowCtx) (cells, styles [2]string) {
	for i, c := range s.Columns() {
		cells[i] = c.Cell(rc)
		if c.Style != nil {
			styles[i] = c.Style(rc)
		}
	}
	return
}

// The basket cell IS conviction.BasketMinute through Baselines.Z for the
// last completed minute — hand-computed, and equal to the direct call —
// with every gap path rendering · (never 0) and the T2 colour engaging at
// exactly ±SignificantZ.
func TestBasketCellsAreBasketMinuteZ(t *testing.T) {
	const mod = 9*60 + 44 // completed minute 09:44 at a 09:45:00 render
	m0944 := minuteAt(t, mod)
	r := optprofile.Row{MinuteOfDay: mod, Days: 20, MedianNetConv: 100, SigmaNetConv: 50, MedianNetNotional: 1000, SigmaNetNotional: 0}
	base := baselines("semis", mod, r, 10, 250) // net σ 0 → floor 250 used
	buckets := map[string]optbucket.Bucket{
		"A": {PremCallAsk: 1500, PremCallBid: 200, PremPutAsk: 100, PremPutBid: 50, NetConviction: 150},
		"B": {PremCallAsk: 300, PremCallBid: 100, PremPutAsk: 0, PremPutBid: 50, NetConviction: 50},
	}
	incomplete := false
	read := func(sym string, minuteSec int64) (optbucket.Bucket, bool) {
		if minuteSec != m0944 || incomplete {
			return optbucket.Bucket{}, false
		}
		return buckets[sym], true
	}
	s := &Source{Base: base, Read: read}
	members := []string{"A", "B"}
	at := m0944 + 60 // 09:45:00
	cells, styles := render(s, row(t, s, "semis", members, at))
	// Σ conv = 200 → (200−100)/50 = +2.0; Σ net = (1800−300)−(100−100) = 1500 → (1500−1000)/250 = +2.0.
	if cells != [2]string{"+2.0", "+2.0"} || styles != [2]string{sgrGreen, ""} {
		t.Fatalf("cells %v styles %q", cells, styles)
	}
	conv, net, ok := conviction.BasketMinute(read, members, m0944)
	cz, nz, cok, nok := base.Z("semis", mod, conv, net)
	if !ok || conviction.FormatZ(cz, cok) != cells[0] || conviction.FormatZ(nz, nok) != cells[1] {
		t.Fatalf("cell %v != BasketMinute→Z %s/%s", cells, conviction.FormatZ(cz, cok), conviction.FormatZ(nz, nok))
	}
	// Mid-minute renders describe the same completed minute.
	if c2, _ := render(s, row(t, s, "semis", members, at+37)); c2 != cells {
		t.Fatalf("09:45:37 = %v, want %v", c2, cells)
	}
	// Just under the threshold: no colour. Just past −: red.
	buckets["B"] = optbucket.Bucket{PremCallAsk: 300, PremCallBid: 100, PremPutAsk: 0, PremPutBid: 50, NetConviction: 45}
	if c, st := render(s, row(t, s, "semis", members, at+10)); c[0] != "+1.9" || st[0] != "" {
		t.Fatalf("1.9σ = %q style %q", c[0], st[0])
	}
	buckets["B"] = optbucket.Bucket{NetConviction: -150, PremCallBid: 750}
	if c, st := render(s, row(t, s, "semis", members, at+20)); c != [2]string{"-2.0", "-2.0"} || st != [2]string{sgrRed, ""} {
		t.Fatalf("−2.0σ = %v style %q", c, st)
	}
	// Gaps: tape not at the minute → ·, unstyled; unknown basket → ·;
	// a pre-open render (completed minute 09:29) → ·.
	incomplete = true
	if c, st := render(s, row(t, s, "semis", members, at+30)); c != [2]string{"·", "·"} || st != [2]string{"", ""} {
		t.Fatalf("incomplete minute = %v style %q, want gaps", c, st)
	}
	incomplete = false
	if c, _ := render(s, row(t, s, "other", members, at+40)); c != [2]string{"·", "·"} {
		t.Fatalf("no baseline = %v, want gaps", c)
	}
	if c, _ := render(s, row(t, s, "semis", members, minuteAt(t, session.OpenMinute)+10)); c != [2]string{"·", "·"} {
		t.Fatalf("pre-open = %v, want gaps", c)
	}
	// Determinism: a second Source renders the same bytes.
	buckets["B"] = optbucket.Bucket{PremCallAsk: 300, PremCallBid: 100, PremPutAsk: 0, PremPutBid: 50, NetConviction: 50}
	s2 := &Source{Base: base, Read: read}
	c1, st1 := render(s, row(t, s, "semis", members, at+50))
	c2, st2 := render(s2, row(t, s2, "semis", members, at+50))
	if c1 != c2 || st1 != st2 || c1 != cells {
		t.Fatalf("not deterministic: %v/%q vs %v/%q", c1, st1, c2, st2)
	}
}

// ExtendTrader: after class%, before pre_share; footer block before the
// gap-glyph line; a missing anchor panics.
func TestExtendTraderOrderAndFooter(t *testing.T) {
	base := []devview.Column{{Name: "cum_share"}, {Name: "concentration_day"}, {Name: "delta"}, {Name: "class%"}, {Name: "pre_share"}}
	footer := "class%            = x\n·                 = no basis for comparison — never a zero\npre_share         = y\n"
	s := &Source{}
	cols, out := s.ExtendTrader(base, footer)
	names := []string{}
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "cum_share concentration_day delta class% conv_z net_z pre_share" {
		t.Errorf("order = %s", got)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "class% ") || !strings.HasPrefix(lines[1], "conv_z / net_z    = ") || !strings.HasPrefix(lines[2], "·") {
		t.Errorf("footer order wrong:\n%s", out)
	}
	for _, bad := range []struct {
		cols   []devview.Column
		footer string
	}{{[]devview.Column{{Name: "delta"}}, footer}, {base, "a = b\n"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("ExtendTrader on a missing anchor did not panic")
				}
			}()
			s.ExtendTrader(bad.cols, bad.footer)
		}()
	}
}

// The real stack: flowshare's trader set extended by delta then by this
// package lands conv_z/net_z right after class%, and the footer block
// between class% and the gap glyph.
func TestExtendRealTraderStack(t *testing.T) {
	stub := devview.Column{Name: "breadth", Width: 9, Cell: func(rc *devview.RowCtx) string { return "" }}
	cols, _, footer := flowshare.TraderColumns(bucket.NewStore(), nil, nil, nil, stub)
	cols, footer = delta.New(bucket.NewStore()).ExtendTrader(cols, footer)
	cols, footer = (&Source{}).ExtendTrader(cols, footer)
	names := []string{}
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "cum_share cum_share_typ cum_share_z breadth concentration_day delta class% conv_z net_z" {
		t.Errorf("order = %s", got)
	}
	lines := strings.Split(footer, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "class% ") {
			at = i
		}
	}
	if at < 0 || !strings.HasPrefix(lines[at+1], "conv_z / net_z ") || !strings.HasPrefix(lines[at+2], "·") {
		t.Errorf("footer block misplaced:\n%s", footer)
	}
}

// Scope law: the footer is a statement of measurement — the scanner the
// flowshare/conviction footers pass.
func TestFooterLanguage(t *testing.T) {
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q — scope law bans it", banned)
		}
	}
	if !strings.Contains(Footer, conviction.Footer) {
		t.Error("footer must carry conviction.Footer verbatim")
	}
	if !strings.Contains(Footer, "±2.0σ") || strings.Contains(Footer, "driver") {
		t.Errorf("footer threshold text / wording: %q", Footer)
	}
}

// Without options the trader set is untouched by this package: names and
// footer are the flowshare+delta composition, verbatim (golden).
func TestNoOptionsLeavesTraderSetUnchanged(t *testing.T) {
	stub := devview.Column{Name: "breadth", Width: 9, Cell: func(rc *devview.RowCtx) string { return "" }}
	cols, _, footer := flowshare.TraderColumns(bucket.NewStore(), nil, nil, nil, stub)
	cols, footer = delta.New(bucket.NewStore()).ExtendTrader(cols, footer)
	names := []string{}
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "cum_share cum_share_typ cum_share_z breadth concentration_day delta class%" {
		t.Errorf("names = %s", got)
	}
	i := strings.Index(flowshare.TraderFooter, "\n·  ")
	want := flowshare.TraderFooter[:i+1] + delta.Footer + flowshare.TraderFooter[i+1:]
	if footer != want || strings.Contains(footer, "conv_z") {
		t.Errorf("footer changed without options:\n%s", footer)
	}
}

// The clock-line status carries the options state after the inner
// status; empty state appends nothing; refusals name the file.
func TestStatusComposition(t *testing.T) {
	inner := func(atSec int64) string { return fmt.Sprintf("SPY +0.10%% at %d", atSec) }
	state := "waiting for tape"
	st := Status(inner, func() string { return state })
	if got := st(7); got != "SPY +0.10% at 7   options: waiting for tape" {
		t.Errorf("status = %q", got)
	}
	state = ""
	if got := st(7); got != "SPY +0.10% at 7" {
		t.Errorf("empty state = %q", got)
	}
	if got := Status(nil, func() string { return "following" })(1); got != "   options: following" {
		t.Errorf("nil inner = %q", got)
	}
	if got := RefusedState(fmt.Errorf("/x/y/_floors.csv: weights stamp mismatch — rebuild profiles")); got != "refused (_floors.csv)" {
		t.Errorf("refused = %q", got)
	}
	if got := RefusedState(fmt.Errorf("options follower started without profiles")); got != "refused" {
		t.Errorf("refused (no file) = %q", got)
	}
}

// --- follower-backed source -------------------------------------------

// One verbatim captured frame (08-24, QQQ put at the ask, premium $1077,
// executed 09:30:04.258 ET) and a later heartbeat at 09:31:00.5 ET, as in
// the optfollow tests.
const realQQQPut = `["option_trades:QQQ",{"id":"01a033f6-c0dd-7363-b328-c1835dbb903b","underlying_symbol":"QQQ","executed_at":1787578204258,"nbbo_bid":"10.67","nbbo_ask":"10.84","size":1,"price":"10.77","option_symbol":"QQQ260918P00700000","created_at":1787578204381,"report_flags":[],"tags":["ask_side","bearish","etf"],"expiry":"2026-09-18","option_type":"put","open_interest":103157,"strike":"700","premium":"1077.00","volume":3,"underlying_price":"709.27","ewma_nbbo_ask":"10.84","ewma_nbbo_bid":"10.67","implied_volatility":"0.2136485532271869","delta":"-0.3783591758814107","theta":"-0.272542686935146","gamma":"0.00958815292159987","vega":"0.705838322695791","rho":"-0.1911841182721973","theo":"10.76999999999993","trade_code":"slan","exchange":"AMXO","ask_vol":1,"bid_vol":2,"no_side_vol":0,"mid_vol":0,"multi_vol":0,"stock_multi_vol":0}]`

const (
	printRecvNs     = int64(1787578204384592000)
	heartbeatRecvNs = int64(1787578260500000000)
	heartbeat       = `[{"detail":"test","ev":"_capture","event":"heartbeat"}]`
)

// writeProfiles writes a complete, stamp-carrying 7.6 profile set (every
// basket, the floors, no ticker files) so conviction.Load and
// tickerview.LoadOptions accept the directory.
func writeProfiles(t *testing.T, dir, stamp string, baskets []universe.Basket) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, optprofile.BasketsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	n := session.MinutesPerSession
	var profiles []optprofile.Profile
	for _, bk := range baskets {
		p := optprofile.Profile{Name: bk.Name, Members: append([]string(nil), bk.Members...), Rows: make([]optprofile.Row, n)}
		for i := range p.Rows {
			p.Rows[i] = optprofile.Row{MinuteOfDay: session.OpenMinute + i, Days: 20, SigmaNetConv: 1, SigmaNetNotional: 1}
		}
		profiles = append(profiles, p)
	}
	if err := optprofile.WriteBaskets(dir, profiles, stamp, "test"); err != nil {
		t.Fatal(err)
	}
	fl := &optprofile.Floors{MinuteOfDay: make([]int, n), NetConv: make([]float64, n), NetNotional: make([]float64, n), BasketNetConv: make([]float64, n), BasketNetNotional: make([]float64, n)}
	for i := range fl.MinuteOfDay {
		fl.MinuteOfDay[i] = session.OpenMinute + i
		fl.NetConv[i], fl.NetNotional[i], fl.BasketNetConv[i], fl.BasketNetNotional[i] = 1, 1, 1, 1
	}
	if err := optprofile.WriteFloors(dir, fl, 0.25, stamp, "test"); err != nil {
		t.Fatal(err)
	}
}

func waitClock(t *testing.T, f *optfollow.Follower, recvNs int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.Clock() < recvNs && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.Clock() < recvNs {
		t.Fatal("follower clock did not advance")
	}
	f.Drained()
}

// The follower-backed source: before the tape has been read past the
// minute's end both the basket cells and the ticker reader are gaps
// (ok=false → ·, never 0); after a heartbeat crosses the minute they
// render, and the basket cell equals BasketMinute→Z over the follower.
func TestFollowerBackedSourceGapsUntilTapeAdvances(t *testing.T) {
	dir := t.TempDir()
	capPath := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(capPath, []byte(fmt.Sprintf("%d %s\n", printRecvNs, realQQQPut)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stamp: a throwaway follower tells us the weights stamp.
	probe, err := optfollow.Start(optfollow.Config{CapturePath: capPath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stamp := probe.Stamp
	bks := probe.Baskets
	probe.Stop()
	profDir := filepath.Join(dir, "profiles")
	writeProfiles(t, profDir, stamp, bks)

	f, err := optfollow.Start(optfollow.Config{CapturePath: capPath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: profDir, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	src, err := FromFollower(f, profDir, []string{"QQQ"})
	if err != nil {
		t.Fatal(err)
	}
	waitClock(t, f, printRecvNs)
	open := minuteAt(t, session.OpenMinute)
	at := open + 60 // 09:31:00: completed minute 09:30
	bk := bks[0]
	if c, st := render(src, row(t, src, bk.Name, bk.Members, at)); c != [2]string{"·", "·"} || st != [2]string{"", ""} {
		t.Fatalf("tape at 09:30:04: basket cells %v style %q, want gaps", c, st)
	}
	if _, _, ok := src.Ticker.Minute("QQQ", open); ok {
		t.Fatal("ticker reader must be ok=false while the tape has not passed 09:31")
	}
	fh, err := os.OpenFile(capPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(fmt.Sprintf("%d %s\n", heartbeatRecvNs, heartbeat))
	fh.Close()
	waitClock(t, f, heartbeatRecvNs)
	if _, net, ok := src.Ticker.Minute("QQQ", open); !ok || net != -1077 {
		t.Fatalf("ticker reader after the heartbeat: net %v ok=%v", net, ok)
	}
	cells, _ := render(src, row(t, src, bk.Name, bk.Members, at+1))
	conv, net, ok := conviction.BasketMinute(f.MinuteBucket, bk.Members, open)
	cz, nz, cok, nok := f.Base.Z(bk.Name, session.OpenMinute, conv, net)
	want := [2]string{conviction.FormatZ(cz, cok), conviction.FormatZ(nz, nok)}
	if !ok || cells != want || cells[0] == "·" {
		t.Fatalf("after the heartbeat: cells %v, BasketMinute→Z %v ok=%v", cells, want, ok)
	}
}

// Stamp enforcement (7.7 V3 / L2): a profile set carrying another
// weights stamp is refused with the file named — at Start (basket
// profiles) and at FromFollower (floors for the ticker source).
func TestStampMismatchNamesFile(t *testing.T) {
	dir := t.TempDir()
	capPath := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(capPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	probe, err := optfollow.Start(optfollow.Config{CapturePath: capPath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stamp, bks := probe.Stamp, probe.Baskets
	probe.Stop()
	stale := filepath.Join(dir, "stale")
	writeProfiles(t, stale, "options-weights-v0@deadbeef", bks)
	if _, err := optfollow.Start(optfollow.Config{CapturePath: capPath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: stale, Poll: time.Millisecond}); err == nil || !strings.Contains(err.Error(), ".csv") || !strings.Contains(err.Error(), "stamp") {
		t.Fatalf("stale basket profiles: err = %v, want a stamp refusal naming the file", err)
	}
	good := filepath.Join(dir, "good")
	writeProfiles(t, good, stamp, bks)
	f, err := optfollow.Start(optfollow.Config{CapturePath: capPath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: good, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	if _, err := FromFollower(f, stale, []string{"QQQ"}); err == nil || !strings.Contains(err.Error(), optprofile.FloorsFile) {
		t.Fatalf("stale floors: err = %v, want a refusal naming %s", err, optprofile.FloorsFile)
	}
	if _, err := FromFollower(f, good, []string{"QQQ"}); err != nil {
		t.Fatal(err)
	}
	if _, err := FromFollower(probe, good, nil); err == nil {
		t.Fatal("a follower without profiles must be refused")
	}
}
