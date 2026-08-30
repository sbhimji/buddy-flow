package optfollow

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/session"
)

// Replayed-data acceptance (MO-4 done-when 3 and 4), gated on a finished
// capture so the unit suite stays hermetic:
//
//	MO4_CAPTURE=<dir with stream.jsonl + manifest.json> \
//	MO4_BUCKETS=data/buckets-options/2026-08-24.csv \
//	MO4_PROFILES=data/profiles-options go test ./internal/optfollow -run Replay -v
//
// Done-when 3: Follower.Minute equals the bucket file's DeriveMinute for
// each (symbol, minute) — the file is the 7.4-verified artifact the Python
// recomputation (scratch minute_recompute.py) reads.
//
// Done-when 4: BasketMinute reproduces the three basket-minutes at 10:29 on
// 08-24 through Baselines.Z against the profile rows FROZEN in testdata
// (the on-disk profiles roll nightly, so the digits are reproducible only
// against those rows); store-vs-session parity; and, when the on-disk
// profile row still equals the frozen one, the live Baselines agree.
func TestReplayMinuteParity(t *testing.T) {
	capDir, buckets, profiles := os.Getenv("MO4_CAPTURE"), os.Getenv("MO4_BUCKETS"), os.Getenv("MO4_PROFILES")
	if capDir == "" || buckets == "" || profiles == "" {
		t.Skip("set MO4_CAPTURE, MO4_BUCKETS and MO4_PROFILES to run against a finished capture")
	}
	f, err := Start(Config{
		CapturePath: capDir + "/stream.jsonl", WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.Done():
	case <-time.After(9 * time.Minute):
		t.Fatal("follow did not finish")
	}
	if err := f.Err(); err != nil {
		t.Fatal(err)
	}
	sess, err := optbucket.ReadCSV(buckets, f.Stamp)
	if err != nil {
		t.Fatal(err)
	}
	minute, err := session.BucketStart("2026-08-24", session.OpenMinute+59) // 10:29 ET
	if err != nil {
		t.Fatal(err)
	}

	// Done-when 3: per-ticker minute reader vs the bucket file.
	for _, sym := range []string{"NVDA", "AAPL", "LITE", "QQQ", "MU"} {
		conv, net, ok := f.Minute(sym, minute)
		if !ok {
			t.Fatalf("%s: Minute not ok on a finished capture", sym)
		}
		b, err := sess.DeriveMinute(sym, minute)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("Minute(%s, 10:29) = net_conviction %.6f net_notional %.6f  (bucket file: %.6f %.6f)\n",
			sym, conv, net, b.NetConviction, b.SignedNotional())
		if math.Abs(conv-b.NetConviction) > 1e-6 || math.Abs(net-b.SignedNotional()) > 1e-6 {
			t.Errorf("%s: Follower.Minute disagrees with the bucket file", sym)
		}
	}

	// Done-when 4.
	frozen := loadFrozenRows(t)
	mod := session.MinuteOfDay(minute * 1_000_000_000)
	for _, bk := range f.Baskets {
		fr, ok := frozen[bk.Name]
		if !ok {
			continue
		}
		conv, net, ok := conviction.BasketMinute(f.MinuteBucket, bk.Members, minute)
		if !ok {
			t.Fatalf("%s: BasketMinute not ok on a finished capture", bk.Name)
		}
		cz, nz, cok, nok := fr.baselines(bk.Name, mod).Z(bk.Name, mod, conv, net)
		got := [2]string{conviction.FormatZ(cz, cok), conviction.FormatZ(nz, nok)}
		fmt.Printf("BasketMinute(%s, 10:29) = %.2f / %.2f → conv_z %s net_z %s (frozen rows)\n", bk.Name, conv, net, got[0], got[1])
		if got != fr.want {
			t.Errorf("%s: z = %v against the frozen rows, want %v", bk.Name, got, fr.want)
		}
		// Store vs session: the same function on the read-back file.
		sconv, snet, sok := conviction.BasketMinute(conviction.SessionMinutes(sess), bk.Members, minute)
		if !sok || sconv != conv || snet != net {
			t.Errorf("%s: store vs session BasketMinute differ: %v/%v vs %v/%v ok=%v", bk.Name, conv, net, sconv, snet, sok)
		}
		// Live baselines agree while the on-disk row is still the frozen one.
		if live := f.Base.Baskets[bk.Name].Rows[mod-session.OpenMinute]; live == fr.row {
			lcz, lnz, lcok, lnok := f.Base.Z(bk.Name, mod, conv, net)
			if lgot := [2]string{conviction.FormatZ(lcz, lcok), conviction.FormatZ(lnz, lnok)}; lgot != got {
				t.Errorf("%s: live baselines %v differ from frozen %v on an identical row", bk.Name, lgot, got)
			}
		} else {
			fmt.Printf("%s: on-disk profile row has rolled past the frozen fixture; live digits not compared\n", bk.Name)
		}
	}
}

type frozenRow struct {
	row                    optprofile.Row
	floorConv, floorNotion float64
	want                   [2]string
}

// baselines builds a Baselines whose only populated minute is mod.
func (fr frozenRow) baselines(name string, mod int) *conviction.Baselines {
	n := session.MinutesPerSession
	p := &optprofile.Profile{Name: name, Rows: make([]optprofile.Row, n)}
	fl := &optprofile.Floors{
		MinuteOfDay: make([]int, n), NetConv: make([]float64, n), NetNotional: make([]float64, n),
		BasketNetConv: make([]float64, n), BasketNetNotional: make([]float64, n),
	}
	i := mod - session.OpenMinute
	p.Rows[i] = fr.row
	fl.BasketNetConv[i], fl.BasketNetNotional[i] = fr.floorConv, fr.floorNotion
	return &conviction.Baselines{Baskets: map[string]*optprofile.Profile{name: p}, Floors: fl}
}

func loadFrozenRows(t *testing.T) map[string]frozenRow {
	t.Helper()
	f, err := os.Open("testdata/basket-minute-1029-2026-08-24.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]frozenRow{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		c := strings.Split(line, ",")
		if len(c) != 12 || c[0] != "basket" {
			t.Fatalf("fixture line %q", line)
		}
		num := func(s string) float64 {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		mod, _ := strconv.Atoi(c[2])
		days, _ := strconv.Atoi(c[3])
		out[c[1]] = frozenRow{
			row: optprofile.Row{MinuteOfDay: mod, Days: days,
				MedianNetConv: num(c[4]), SigmaNetConv: num(c[5]), MedianNetNotional: num(c[6]), SigmaNetNotional: num(c[7])},
			floorConv: num(c[8]), floorNotion: num(c[9]), want: [2]string{c[10], c[11]},
		}
	}
	return out
}
