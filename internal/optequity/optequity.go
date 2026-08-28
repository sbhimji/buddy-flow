// Package optequity puts the options tape on the equity screen (mini-spec
// MO-5, owner decision O1): basket conv_z / net_z columns on the trader
// table and the per-ticker OptionsSource the drill-down and crossings
// strip take — as measurement columns only, never a brightness input or
// driver (DEV-PLAN Appendix C amendment 2026-08-26; 6.6 / D13 unchanged
// for driver use).
//
// One Source serves both cmd/replay (a read-back options bucket file) and
// cmd/live (the shared read-only optfollow.Follower over the day's options
// capture). The basket numbers have one code path — conviction.BasketMinute
// through conviction.Baselines.Z — so the equity table and the :8788 options
// table can never disagree about a basket-minute. Reads happen on the
// render goroutine; the Follower's MinuteBucket is its locked/atomic
// accessor, and the Baselines are immutable after load.
//
// Stamp posture (7.7 V3, MO-5 L2): the profiles and floors must carry the
// weights config's stamp; a mismatch is an error from the loaders that
// names the file — the caller refuses the options columns loudly and the
// equity table starts without them. Never a blended number.
package optequity

import (
	"fmt"
	"strings"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optfollow"
	"buddy-flow/internal/session"
	"buddy-flow/internal/tickerview"
	"buddy-flow/internal/universe"
)

const (
	sgrGreen = "\x1b[1;32m"
	sgrRed   = "\x1b[1;31m"
)

// SignificantZ is the highlight threshold for conv_z / net_z — the SAME
// constant the share z uses (trader-view-v0 T2), so the two panels can
// never disagree about "significant".
const SignificantZ = flowshare.SignificantZ

// Source is the options tape as the equity screen reads it.
type Source struct {
	Base   *conviction.Baselines    // basket profiles + floors, stamp-checked
	Read   conviction.MinuteBuckets // per-ticker minute reader (ok=false = not measurable)
	Ticker *tickerview.OptionsSource
	Stamp  string

	memoAt int64
	memo   map[string]cell // per basket name within one render second
	minute int64           // last completed regular-session minute at memoAt (0 = none)
}

type cell struct {
	convZ, netZ   float64
	convOK, netOK bool
}

// LoadReplay binds a read-back options bucket file (7.4) and the 7.6
// profiles — ticker and basket, both stamp-checked against the weights
// config — for cmd/replay.
func LoadReplay(weightsPath, bucketsPath, profilesDir string, baskets []universe.Basket, symbols []string) (*Source, error) {
	w, hash, err := optclassify.LoadWeights(weightsPath)
	if err != nil {
		return nil, err
	}
	stamp := w.Version + "@" + hash
	sess, err := optbucket.ReadCSV(bucketsPath, stamp)
	if err != nil {
		return nil, err
	}
	base, err := conviction.Load(profilesDir, baskets, stamp)
	if err != nil {
		return nil, err
	}
	tk, err := tickerview.LoadOptions(profilesDir, symbols, stamp, tickerview.SessionMinutes(sess))
	if err != nil {
		return nil, err
	}
	return &Source{Base: base, Read: conviction.SessionMinutes(sess), Ticker: tk, Stamp: stamp}, nil
}

// FromFollower binds a running follower (started with a ProfilesDir, so
// its basket Baselines are loaded and stamp-checked) plus the per-ticker
// profiles from the same directory, for cmd/live. Every read goes
// through the follower's MinuteBucket / Minute — ok=false until the tape
// has been read past the minute's end (MO-4 F3), rendered as a gap.
func FromFollower(f *optfollow.Follower, profilesDir string, symbols []string) (*Source, error) {
	if f.Base == nil {
		return nil, fmt.Errorf("options follower started without profiles — conv_z/net_z need -options-profiles")
	}
	tk, err := tickerview.LoadOptions(profilesDir, symbols, f.Stamp, f.Minute)
	if err != nil {
		return nil, err
	}
	return &Source{Base: f.Base, Read: f.MinuteBucket, Ticker: tk, Stamp: f.Stamp}, nil
}

// prepare resets the memo when the render second changes and fixes the
// minute the columns describe: the last completed minute (the table's
// "completed minute = HH:MM"), clamped so a post-close render keeps
// reading 15:59 as the drill-down does. Baselines.Z gates on the regular
// session, so a pre-open render (completed minute 09:29 or earlier) gaps.
func (s *Source) prepare(atSec int64) {
	if s.memoAt == atSec && s.memo != nil {
		return
	}
	s.memoAt, s.memo, s.minute = atSec, map[string]cell{}, 0
	closeSec, err := session.BucketStart(session.Date(atSec*1e9), session.CloseMinute)
	if err != nil {
		return
	}
	end := session.MinuteStart(atSec * 1e9)
	if end > closeSec {
		end = closeSec
	}
	s.minute = end - 60
}

func (s *Source) at(rc *devview.RowCtx) cell {
	s.prepare(rc.AtSec)
	if c, ok := s.memo[rc.Basket.Name]; ok {
		return c
	}
	var c cell
	if s.minute > 0 {
		members := make([]string, len(rc.Basket.States))
		for i, st := range rc.Basket.States {
			members[i] = st.Symbol
		}
		if conv, net, ok := conviction.BasketMinute(s.Read, members, s.minute); ok {
			mod := session.MinuteOfDay(s.minute * 1e9)
			c.convZ, c.netZ, c.convOK, c.netOK = s.Base.Z(rc.Basket.Name, mod, conv, net)
		}
	}
	s.memo[rc.Basket.Name] = c
	return c
}

// style is the T2 colour posture at the shared threshold: bold green at
// or beyond +SignificantZ, bold red at or beyond −SignificantZ, nothing
// on a gap.
func style(z float64, ok bool) string {
	switch {
	case !ok:
		return ""
	case z >= SignificantZ:
		return sgrGreen
	case z <= -SignificantZ:
		return sgrRed
	}
	return ""
}

// Columns is the basket pair: last completed minute's basket sum through
// the basket's 20d matched-minute baseline; · on any null (7.7 V1).
func (s *Source) Columns() []devview.Column {
	return []devview.Column{
		{Name: "conv_z", Width: 6,
			Cell:  func(rc *devview.RowCtx) string { c := s.at(rc); return conviction.FormatZ(c.convZ, c.convOK) },
			Style: func(rc *devview.RowCtx) string { c := s.at(rc); return style(c.convZ, c.convOK) }},
		{Name: "net_z", Width: 6,
			Cell:  func(rc *devview.RowCtx) string { c := s.at(rc); return conviction.FormatZ(c.netZ, c.netOK) },
			Style: func(rc *devview.RowCtx) string { c := s.at(rc); return style(c.netZ, c.netOK) }},
	}
}

// Footer is the trader legend for the pair: conviction.Footer verbatim
// (it passes that package's scanner), laid out as one footer line per
// column so it reads like the rest of the block.
const Footer = "conv_z / net_z    = " + conviction.Footer + " Bold at/beyond ±2.0σ (the same threshold as cum_share_z). Options premium is measured here, never used as a driver of any other column.\n"

// ExtendTrader inserts the pair after class% (README column order:
// … delta class% conv_z net_z pre_share) and the footer block before the
// gap-glyph line. Both anchors are other packages' (delta's column,
// flowshare's footer line); a miss is a wiring error and panics at
// composition — never a silent mis-ordered append (delta's posture).
func (s *Source) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	pair := s.Columns()
	at := -1
	for i, col := range cols {
		if col.Name == "class%" {
			at = i + 1
			break
		}
	}
	if at < 0 {
		panic("optequity.ExtendTrader: no class% column to anchor conv_z/net_z after")
	}
	out := make([]devview.Column, 0, len(cols)+len(pair))
	out = append(out, cols[:at]...)
	out = append(out, pair...)
	out = append(out, cols[at:]...)
	i := strings.Index(footer, "\n·  ")
	if i < 0 {
		panic("optequity.ExtendTrader: trader footer has no gap-glyph line to anchor the options block before")
	}
	return out, footer[:i+1] + Footer + footer[i+1:]
}
