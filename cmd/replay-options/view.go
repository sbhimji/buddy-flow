// view.go — the 7.4-era snapshot view: after an instant replay, render one
// per-basket table at a chosen ET second. Deliberately static (post-replay,
// single goroutine) — the live-refreshing column view is story 7.7. Dev
// view only; every rendered string is a statement of measurement.
package main

import (
	"fmt"
	"math"
	"sort"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// renderSnapshot prints per-basket options flow at atSec (epoch seconds):
// cumulative since open, plus the last completed minute. No baselines, no
// z-scores — those are 7.6/7.7; sums are labeled as sums.
func renderSnapshot(store *optbucket.Store, baskets []universe.Basket, base *conviction.Baselines, date string, atSec int64) error {
	openSec, err := session.BucketStart(date, session.OpenMinute)
	if err != nil {
		return err
	}
	if atSec <= openSec {
		return fmt.Errorf("-view-at is at or before the open")
	}
	lastMinStart := (atSec / 60) * 60
	if lastMinStart-60 >= openSec {
		lastMinStart -= 60 // last COMPLETED minute
	} else {
		lastMinStart = openSec
	}

	type row struct {
		name               string
		prints, contracts  int64
		cumNet, cumConv    float64
		cumSweep, cumGross float64
		minNet, minConv    float64
		convZ, netZ        string
	}
	rows := make([]row, 0, len(baskets))
	for _, bk := range baskets {
		var cum, last optbucket.Bucket
		for _, sym := range bk.Members {
			c := store.Window(sym, openSec, atSec)
			cum.Add(&c)
			m := store.Window(sym, lastMinStart, minInt64(lastMinStart+60, atSec))
			last.Add(&m)
		}
		gross := cum.PremCallAsk + cum.PremCallBid + cum.PremCallMid +
			cum.PremPutAsk + cum.PremPutBid + cum.PremPutMid
		r := row{
			name: bk.Name, prints: cum.Prints, contracts: cum.Contracts,
			cumNet: cum.SignedNotional(), cumConv: cum.NetConviction,
			cumSweep: cum.PremSweep, cumGross: gross,
			minNet: last.SignedNotional(), minConv: last.NetConviction,
			convZ: "·", netZ: "·",
		}
		if base != nil && lastMinStart+60 <= atSec { // only a COMPLETED minute has a z
			// The shared basket-minute aggregation (MO-4 F4) — the same
			// arithmetic as `last` above over a completed minute, and the
			// one the equity screen's conv_z/net_z will use.
			conv, net := conviction.BasketMinute(conviction.StoreMinutes(store), bk.Members, lastMinStart)
			cz, nz, cok, nok := base.Z(bk.Name, session.MinuteOfDay(lastMinStart*1_000_000_000), conv, net)
			r.convZ, r.netZ = conviction.FormatZ(cz, cok), conviction.FormatZ(nz, nok)
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		return math.Abs(rows[i].cumConv) > math.Abs(rows[j].cumConv)
	})

	clock := time.Unix(atSec, 0).In(session.ET()).Format("15:04:05")
	baselineNote := "no baselines loaded (pass -profiles for z)"
	if base != nil {
		baselineNote = "z vs 20d matched-minute basket baselines"
	}
	fmt.Printf("\nOPTIONS FLOW %s at %s ET — sums since 09:30; %s\n", date, clock, baselineNote)
	fmt.Printf("%-24s %8s %10s %12s %12s %7s %11s %11s %7s %7s\n",
		"basket", "prints", "contracts", "cum_net_$", "cum_conv_$", "sweep%", "min_net_$", "min_conv_$", "conv_z", "net_z")
	for _, r := range rows {
		sweepPct := "·"
		if r.cumGross > 0 {
			sweepPct = fmt.Sprintf("%.1f", 100*r.cumSweep/r.cumGross)
		}
		fmt.Printf("%-24s %8d %10d %12s %12s %7s %11s %11s %7s %7s\n",
			r.name, r.prints, r.contracts,
			dollars(r.cumNet), dollars(r.cumConv), sweepPct,
			dollars(r.minNet), dollars(r.minConv), r.convZ, r.netZ)
	}
	fmt.Println("\ncum_net_$: ask-side minus bid-side premium, calls positive puts negative, summed over basket members since the open.")
	fmt.Println("cum_conv_$: the same premium weighted per print by sweep/moneyness/expiry/size-vs-OI (options-weights-v1).")
	fmt.Println("sweep%: share of gross premium printed on intermarket-sweep condition codes. min_*: last completed minute.")
	if base != nil {
		fmt.Println(conviction.Footer)
	}
	return nil
}

// dollars renders a signed dollar amount compactly ($1.2M, -$34k).
func dollars(v float64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%s$%.2fB", sign, v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%s$%.1fM", sign, v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%s$%.0fk", sign, v/1e3)
	case v == 0:
		return "0"
	default:
		return fmt.Sprintf("%s$%.0f", sign, v)
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
