package optfollow

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"buddy-flow/internal/session"
)

const (
	weightsPath = "../../docs/foundations/options-weights-v1.json"
	basketsCfg  = "../../docs/foundations/morning-tape-baskets-v2.json"
)

// One verbatim captured frame (08-24, QQQ put at the ask, premium $1077,
// executed 09:30:04.258 ET): the print the tests read back.
const realQQQPut = `["option_trades:QQQ",{"id":"01a033f6-c0dd-7363-b328-c1835dbb903b","underlying_symbol":"QQQ","executed_at":1787578204258,"nbbo_bid":"10.67","nbbo_ask":"10.84","size":1,"price":"10.77","option_symbol":"QQQ260918P00700000","created_at":1787578204381,"report_flags":[],"tags":["ask_side","bearish","etf"],"expiry":"2026-09-18","option_type":"put","open_interest":103157,"strike":"700","premium":"1077.00","volume":3,"underlying_price":"709.27","ewma_nbbo_ask":"10.84","ewma_nbbo_bid":"10.67","implied_volatility":"0.2136485532271869","delta":"-0.3783591758814107","theta":"-0.272542686935146","gamma":"0.00958815292159987","vega":"0.705838322695791","rho":"-0.1911841182721973","theo":"10.76999999999993","trade_code":"slan","exchange":"AMXO","ask_vol":1,"bid_vol":2,"no_side_vol":0,"mid_vol":0,"multi_vol":0,"stock_multi_vol":0}]`

func writeCapture(t *testing.T, dir string, withManifest bool) string {
	t.Helper()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, []byte("1787578204384592000 "+realQQQPut+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withManifest {
		if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func waitDone(t *testing.T, f *Follower) {
	t.Helper()
	select {
	case <-f.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("follower did not finish")
	}
}

// A finished capture (manifest present): the follower reads it, ends on
// its own, and the store holds the print — Minute reads it back with the
// 7.4 derivation (ask-side put → net_notional −premium).
func TestFinishedCaptureEndsOnManifest(t *testing.T) {
	dir := t.TempDir()
	var ticks, opened atomic.Int32
	f, err := Start(Config{
		CapturePath: writeCapture(t, dir, true), WeightsPath: weightsPath, BasketsCfg: basketsCfg,
		Poll:   time.Millisecond,
		Tick:   func(int64) { ticks.Add(1) },
		Opened: func() { opened.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, f)
	if err := f.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if !f.Opened() || opened.Load() != 1 || ticks.Load() < 1 {
		t.Fatalf("opened=%v openedCalls=%d ticks=%d", f.Opened(), opened.Load(), ticks.Load())
	}
	if f.Stats.Frames != 1 || f.Stats.Prints != 1 || f.Pipeline.Processed.Load() != 1 {
		t.Fatalf("stats=%+v processed=%d", *f.Stats, f.Pipeline.Processed.Load())
	}
	if f.Base != nil || f.Stamp == "" || len(f.Baskets) == 0 {
		t.Fatalf("base=%v stamp=%q baskets=%d", f.Base, f.Stamp, len(f.Baskets))
	}
	open, err := session.BucketStart("2026-08-24", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	if f.Store.MaxSec.Load() != open+4 {
		t.Fatalf("MaxSec = %d, want 09:30:04 (%d)", f.Store.MaxSec.Load(), open+4)
	}
	conv, net := f.Minute("QQQ", open)
	if net != -1077 || conv >= 0 {
		t.Fatalf("Minute(QQQ, 09:30) = %v / %v, want conviction < 0 and net −1077", conv, net)
	}
	if conv, net := f.Minute("QQQ", open+60); conv != 0 || net != 0 {
		t.Fatalf("silent minute = %v / %v, want 0 / 0", conv, net)
	}
	if b := f.MinuteBucket("QQQ", open); b.Prints != 1 || b.PremPutAsk != 1077 {
		t.Fatalf("MinuteBucket = %+v", b)
	}
	f.Stop() // idempotent after a natural end
}

// A capture still being written (no manifest): the follower keeps
// polling until Stop, which drains and closes Done.
func TestStopWhileTailing(t *testing.T) {
	dir := t.TempDir()
	f, err := Start(Config{
		CapturePath: writeCapture(t, dir, false), WeightsPath: weightsPath, BasketsCfg: basketsCfg,
		Poll: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.Pipeline.Processed.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-f.Done():
		t.Fatal("follower ended without a manifest or Stop")
	default:
	}
	f.Stop()
	waitDone(t, f)
	if !f.Opened() || f.Err() != nil || f.Stats.Prints != 1 {
		t.Fatalf("opened=%v err=%v prints=%d", f.Opened(), f.Err(), f.Stats.Prints)
	}
}

// Started before the feeder created the file: Waiting fires, and Stop
// wakes the wait promptly even with a long WaitPoll; Opened stays false.
func TestStopWhileWaitingForFile(t *testing.T) {
	dir := t.TempDir()
	waiting := make(chan string, 1)
	f, err := Start(Config{
		CapturePath: filepath.Join(dir, "stream.jsonl"), WeightsPath: weightsPath, BasketsCfg: basketsCfg,
		WaitPoll: time.Hour,
		Waiting: func(p string) {
			select {
			case waiting <- p:
			default:
			}
		},
		Opened: func() { t.Error("Opened must not fire for a file that never appeared") },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-waiting:
		if p != filepath.Join(dir, "stream.jsonl") {
			t.Fatalf("Waiting(%q)", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Waiting never fired")
	}
	start := time.Now()
	f.Stop()
	waitDone(t, f)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop took %s while waiting for the file", d)
	}
	if f.Opened() || f.Err() != nil {
		t.Fatalf("opened=%v err=%v", f.Opened(), f.Err())
	}
}

func TestStartRefusesBadConfig(t *testing.T) {
	if _, err := Start(Config{CapturePath: "x", WeightsPath: "/nonexistent.json", BasketsCfg: basketsCfg}); err == nil {
		t.Fatal("bad weights path must fail Start")
	}
	if _, err := Start(Config{CapturePath: "x", WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: t.TempDir()}); err == nil {
		t.Fatal("an empty profiles dir must refuse the whole view (7.7 V3)")
	}
}
