package optfollow

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/session"
)

// Replayed-data acceptance (MO-4 done-when 3 and 4), gated on a finished
// capture so the unit suite stays hermetic:
//
//	MO4_CAPTURE=<dir with stream.jsonl + manifest.json> \
//	MO4_BUCKETS=data/buckets-options/2026-08-24.csv \
//	MO4_PROFILES=data/profiles-options go test ./internal/optfollow -run Replay -v
//
// Follower.Minute must equal the bucket file's DeriveMinute for each
// (symbol, minute) — the file is the 7.4-verified artifact and the Python
// recomputation reads it — and BasketMinute through Baselines.Z must
// reproduce the three 7.7-verified basket-minutes at 10:29 on 08-24.
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
		conv, net := f.Minute(sym, minute)
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

	// Done-when 4: the three 7.7-verified basket-minutes. The digits depend
	// on the profiles on disk, which the nightly rolls (7.7 recorded
	// -0.0/-0.1, +0.4/+0.4, +2.7/+2.2 against the 08-24-night profiles);
	// pass the digits for the current profiles as
	// MO4_WANT_Z="semis_compute=-0.0/-0.0,proof_tier_ai_megacap=+0.6/+0.6,photonics_optics=+2.7/+2.2"
	// (the Python recomputation's output) to assert them; unset, they print.
	want := map[string][2]string{}
	for _, kv := range strings.Split(os.Getenv("MO4_WANT_Z"), ",") {
		if name, zs, ok := strings.Cut(kv, "="); ok {
			if c, n, ok := strings.Cut(zs, "/"); ok {
				want[name] = [2]string{c, n}
			}
		}
	}
	mod := session.MinuteOfDay(minute * 1_000_000_000)
	for _, bk := range f.Baskets {
		switch bk.Name {
		case "semis_compute", "proof_tier_ai_megacap", "photonics_optics":
		default:
			continue
		}
		conv, net := conviction.BasketMinute(f.MinuteBucket, bk.Members, minute)
		cz, nz, cok, nok := f.Base.Z(bk.Name, mod, conv, net)
		got := [2]string{conviction.FormatZ(cz, cok), conviction.FormatZ(nz, nok)}
		fmt.Printf("BasketMinute(%s, 10:29) = %.2f / %.2f → conv_z %s net_z %s\n", bk.Name, conv, net, got[0], got[1])
		if w, ok := want[bk.Name]; ok && got != w {
			t.Errorf("%s: z = %v, want %v", bk.Name, got, w)
		}
		// Same numbers from the read-back file through the same function.
		sconv, snet := conviction.BasketMinute(conviction.SessionMinutes(sess), bk.Members, minute)
		if sconv != conv || snet != net {
			t.Errorf("%s: store vs session BasketMinute differ: %v/%v vs %v/%v", bk.Name, conv, net, sconv, snet)
		}
	}
}
