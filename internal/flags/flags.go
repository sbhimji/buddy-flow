// Package flags is the MO-6 `flags` column (mini-spec
// docs/mini-specs/metric-overload/MO-6-flags-column.md, owner decision
// O3): the leftmost trader column, a fixed-width string of six glyphs, one
// slot per named measurement, lit when that measurement is beyond its own
// threshold and `·` otherwise. The eye scans for several lit together;
// the code never counts them.
//
// G1 — each glyph is its column's own colour predicate, exported by the
// metric's package (flowshare.CumZFlag, flowshare.Run.RunFlag,
// breadth.Calc.Flag, breadth.VolCalc.DollarFlag, delta.Calc.Flag,
// optequity.Source.ConvFlag). No threshold lives here; a glyph and its
// cell cannot disagree because they call one function.
//
// G2 — never summed, never sorted on, never a score. Rank stays
// cum_share_z. A "sort by number of flags" is a composite and is declined
// by the spec (scope law; the APEX decision-layer failure).
//
// The composer lives in its own package rather than in internal/flowshare
// because delta's and optequity's packages/tests import flowshare — the
// footer names their thresholds, and a flowshare import of them would
// cycle.
package flags

import (
	"fmt"
	"strings"

	"buddy-flow/internal/breadth"
	"buddy-flow/internal/delta"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
)

// Fn is a glyph predicate: lit when the measurement is beyond its own
// threshold, positive by sign (colour), ok=false on a gap — never lit.
type Fn func(rc *devview.RowCtx) (lit, positive, ok bool)

// Width is the column's fixed width: one glyph per slot, always.
const Width = 6

// Glyphs are the six slots in order. `$` is unsigned (always green).
var Glyphs = [Width]string{"Z", "R", "B", "$", "δ", "C"}

// Set is the composed column: one predicate per slot. A nil slot is a
// metric that is not wired (e.g. no options tape) and stays unlit — the
// column ships with fewer glyphs rather than a fake one.
type Set struct {
	Z, R, B, Dollar, Delta, Conv Fn
}

func (s Set) slots() [Width]Fn { return [Width]Fn{s.Z, s.R, s.B, s.Dollar, s.Delta, s.Conv} }

const (
	gap   = "·"
	reset = "\x1b[0m"
)

// Render is the cell: each lit glyph individually ANSI-wrapped in its own
// colour, unlit slots the unstyled gap glyph (G5). Exactly Width visible
// runes — the renderer's padding never engages, so alignment survives
// the invisible SGR bytes. Pure in (store state, second): the predicates
// are.
func (s Set) Render(rc *devview.RowCtx) string {
	var sb strings.Builder
	for i, fn := range s.slots() {
		if fn == nil {
			sb.WriteString(gap)
			continue
		}
		lit, positive, ok := fn(rc)
		if sgr := flowshare.SGR(lit, positive, ok); sgr != "" {
			sb.WriteString(sgr + Glyphs[i] + reset)
		} else {
			sb.WriteString(gap)
		}
	}
	return sb.String()
}

// Column is the trader column. No Style: the cell carries its own
// per-glyph colour.
func (s Set) Column() devview.Column {
	return devview.Column{Name: "flags", Width: Width, Cell: s.Render}
}

// Footer is the legend (G4): one clause per glyph, statements of
// measurement, thresholds printed from the shared constants so the text
// cannot drift from the predicates. Goes through the scanner test.
var Footer = fmt.Sprintf(`flags             = six slots, one per measurement, each lit only when that measurement is beyond its own threshold — the same threshold that colours its cell — and · otherwise; read which are lit together (several lit at once is the simultaneity §2.5 describes); never counted, never ranked on
                    Z = cum_share_z at/beyond ±%.1fσ (green above own typical, red below); R = 5m_z at/beyond ±%.1fσ (green/red the same way); B = breadth over %.0f%% of the basket outperforming SPY (green) or underperforming (red); $ = of the outperforming stocks, at least half (and at least one) also on unusual volume — the cell's $ count against its ↑ count; δ = delta at/beyond ±%.2f (green ask-side, red bid-side; the thin-basket rule applies); C = conv_z at/beyond ±%.1fσ (green/red by sign); a metric that is not wired or has no basis leaves its slot ·
`, flowshare.SignificantZ, flowshare.SignificantZ, 100*breadth.HighlightFrac, delta.DeltaHighlight, flowshare.SignificantZ)

// ExtendTrader puts the column leftmost (README target order: flags
// cum_share …) and the footer block first — the column is read first, so
// its definition comes first. The footer must begin with the cum_share
// line (flowshare's trader footer); anything else is a wiring error and
// panics at composition (delta's posture).
func (s Set) ExtendTrader(cols []devview.Column, footer string) ([]devview.Column, string) {
	if !strings.HasPrefix(footer, "cum_share ") {
		panic("flags.ExtendTrader: trader footer does not start with the cum_share line to anchor the flags block before")
	}
	out := make([]devview.Column, 0, len(cols)+1)
	out = append(out, s.Column())
	out = append(out, cols...)
	return out, Footer + footer
}
