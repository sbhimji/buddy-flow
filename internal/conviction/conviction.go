// Package conviction is the read-time z layer for the options tape's two
// signed basket series (mini-spec 7.7): NetOptionsConviction and the
// unweighted net notional, each against its basket's day-level 20-day
// matched-minute baseline through zguard — the one z function.
package conviction

import (
	"fmt"

	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/zguard"
)

// Baselines are the loaded basket profiles and floors, stamp-checked.
type Baselines struct {
	Baskets map[string]*optprofile.Profile
	Floors  *optprofile.Floors
}

// Load reads every basket's profile and the floors, refusing stale
// weights or changed membership (V3).
func Load(dir string, baskets []universe.Basket, weightsStamp string) (*Baselines, error) {
	b := &Baselines{Baskets: map[string]*optprofile.Profile{}}
	for _, bk := range baskets {
		p, err := optprofile.ReadBasket(dir, bk.Name, bk.Members, weightsStamp)
		if err != nil {
			return nil, err
		}
		b.Baskets[bk.Name] = p
	}
	fl, err := optprofile.ReadFloors(dir, weightsStamp)
	if err != nil {
		return nil, err
	}
	b.Floors = fl
	return b, nil
}

// Z computes both z-scores for a basket's completed minute (values are the
// basket's summed net_conviction and net_notional over that minute).
// ok=false is zguard's defined null or the MinProfiledDays gate.
func (b *Baselines) Z(basket string, minuteOfDay int, netConv, netNotional float64) (convZ, netZ float64, convOK, netOK bool) {
	p := b.Baskets[basket]
	if p == nil || !session.InRegular(minuteOfDay) {
		return 0, 0, false, false
	}
	i := minuteOfDay - session.OpenMinute
	r := p.Rows[i]
	if r.Days < optprofile.MinProfiledDays {
		return 0, 0, false, false
	}
	convZ, convOK = zguard.Z(netConv, r.MedianNetConv, r.SigmaNetConv, b.Floors.BasketNetConv[i])
	netZ, netOK = zguard.Z(netNotional, r.MedianNetNotional, r.SigmaNetNotional, b.Floors.BasketNetNotional[i])
	return
}

// FormatZ renders a z as the dev view does: signed one decimal, "·" for null.
func FormatZ(z float64, ok bool) string {
	if !ok {
		return "·"
	}
	return fmt.Sprintf("%+.1f", z)
}

// Footer is the rendered legend — statements of measurement only (V4).
const Footer = "conv_z: last completed minute's conviction-weighted options premium (ask-side minus bid-side, calls positive puts negative) vs the basket's 20d matched-minute median/MAD, σ floor D18. net_z: same for the unweighted premium. · = not measurable (below 10 profiled days or σ floor 0)."
