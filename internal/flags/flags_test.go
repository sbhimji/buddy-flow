package flags

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/delta"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/optequity"
)

const (
	sgrGreen = "\x1b[1;32m"
	sgrRed   = "\x1b[1;31m"
)

func fixed(lit, positive, ok bool) Fn {
	return func(*devview.RowCtx) (bool, bool, bool) { return lit, positive, ok }
}

var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visible strips SGR bytes.
func visible(s string) string { return sgrRE.ReplaceAllString(s, "") }

// TestRenderPerSlot: every slot lit green / lit red / unlit / gap / unwired
// renders its own glyph individually wrapped, and nothing else on the row
// changes — the column is presence, never magnitude.
func TestRenderPerSlot(t *testing.T) {
	rc := &devview.RowCtx{}
	all := "·····" + "·"
	if got := (Set{}).Render(rc); got != all {
		t.Fatalf("empty set = %q, want %q", got, all)
	}
	for i, g := range Glyphs {
		set := func(fn Fn) Set {
			var s Set
			p := []*Fn{&s.Z, &s.R, &s.B, &s.Dollar, &s.Delta, &s.Conv}
			*p[i] = fn
			return s
		}
		want := func(mid string) string { return strings.Repeat(gap, i) + mid + strings.Repeat(gap, Width-1-i) }
		cases := []struct {
			name string
			fn   Fn
			want string
		}{
			{"lit positive", fixed(true, true, true), want(sgrGreen + g + reset)},
			{"lit negative", fixed(true, false, true), want(sgrRed + g + reset)},
			{"unlit", fixed(false, true, true), all},
			{"ok=false", fixed(true, true, false), all}, // a gap never lights, even if lit is set
			{"unwired", nil, all},
		}
		for _, c := range cases {
			got := set(c.fn).Render(rc)
			if got != c.want {
				t.Errorf("slot %d (%s) %s = %q, want %q", i, g, c.name, got, c.want)
			}
			if n := len([]rune(visible(got))); n != Width {
				t.Errorf("slot %d %s: %d visible runes, want %d", i, c.name, n, Width)
			}
		}
	}
}

// TestRenderTwoLitAndDeterminism: two glyphs lit together render two
// independent wraps (no shared span, no count); repeated renders and a
// second Set with the same predicates yield identical bytes.
func TestRenderTwoLitAndDeterminism(t *testing.T) {
	rc := &devview.RowCtx{}
	s := Set{Z: fixed(true, true, true), B: fixed(true, false, true), Dollar: fixed(true, true, true), Conv: fixed(false, false, true)}
	want := sgrGreen + "Z" + reset + gap + sgrRed + "B" + reset + sgrGreen + "$" + reset + gap + gap
	if got := s.Render(rc); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if visible(s.Render(rc)) != "Z·B$··" {
		t.Errorf("visible = %q", visible(s.Render(rc)))
	}
	s2 := Set{Z: fixed(true, true, true), B: fixed(true, false, true), Dollar: fixed(true, true, true), Conv: fixed(false, false, true)}
	for i := 0; i < 3; i++ {
		if a, b := s.Render(rc), s2.Render(rc); a != want || b != want {
			t.Fatalf("nondeterministic: %q / %q", a, b)
		}
	}
	col := s.Column()
	if col.Name != "flags" || col.Width != Width || col.Style != nil || col.Cell(rc) != want {
		t.Errorf("column = %+v", col)
	}
}

// TestExtendTraderRealStack: leftmost on the real composed set, footer
// block first, scanner clean; a footer without the cum_share anchor panics.
func TestExtendTraderRealStack(t *testing.T) {
	stub := devview.Column{Name: "breadth", Width: 9, Cell: func(rc *devview.RowCtx) string { return "" }}
	cols, rank, footer := flowshare.TraderColumns(bucket.NewStore(), nil, nil, nil, stub)
	cols, footer = delta.New(bucket.NewStore()).ExtendTrader(cols, footer)
	cols, footer = (&optequity.Source{}).ExtendTrader(cols, footer)
	cols, footer = (Set{Z: flowshare.CumZFlag(rank)}).ExtendTrader(cols, footer)
	names := []string{}
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "flags cum_share cum_share_typ cum_share_z breadth concentration_day delta class% conv_z net_z" {
		t.Errorf("order = %s", got)
	}
	lines := strings.Split(footer, "\n")
	if !strings.HasPrefix(lines[0], "flags ") || !strings.HasPrefix(strings.TrimSpace(lines[1]), "Z = ") || !strings.HasPrefix(lines[2], "cum_share ") {
		t.Errorf("footer block misplaced:\n%s", footer)
	}
	for _, g := range Glyphs {
		if !strings.Contains(Footer, g+" = ") {
			t.Errorf("footer has no clause for glyph %s", g)
		}
	}
	// Thresholds are printed from the constants.
	for _, want := range []string{"±2.0σ", "70%", "±0.20"} {
		if !strings.Contains(Footer, want) {
			t.Errorf("footer lacks %q", want)
		}
	}
	// Scope law scanner — the same substrings the other trader footers guard.
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "score", "hot"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q — scope law bans it", banned)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("no panic on a footer without the cum_share anchor")
		}
	}()
	(Set{}).ExtendTrader(cols, "something else\n")
}

// ---- frame parsing (the G1 agreement test on a replayed frame) ----

type cellRead struct {
	text string // visible, trimmed
	sgr  string // SGR of the first styled non-space rune, "" if unstyled
}

type rowRead struct {
	name  string
	cells []cellRead
	// glyphs: the flags column's six visible runes and each one's SGR.
	glyphs [Width]cellRead
}

// parseFrame reads a rendered trader frame: line 0 is the clock line, line
// 1 the header, then one basket row per line until a blank line. Column
// spans come from the header (cells are right-aligned to their header
// name's end). Returns the header names and rows.
func parseFrame(t *testing.T, frame string) ([]string, []rowRead) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) < 3 {
		t.Fatalf("frame too short")
	}
	hdr := []rune(lines[1])
	var names []string
	var ends []int
	for i := 0; i < len(hdr); {
		if hdr[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(hdr) && hdr[j] != ' ' {
			j++
		}
		names, ends = append(names, string(hdr[i:j])), append(ends, j-1)
		i = j
	}
	if names[0] != "BASKET" {
		t.Fatalf("header does not start with BASKET: %q", lines[1])
	}
	names, ends = names[1:], ends[1:]
	var rows []rowRead
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		// Visible runes with the SGR in force at each.
		var vis []rune
		var sgr []string
		cur := ""
		for i := 0; i < len(line); {
			if loc := sgrRE.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
				seq := line[i : i+loc[1]]
				if seq == reset {
					cur = ""
				} else {
					cur = seq
				}
				i += loc[1]
				continue
			}
			r := []rune(line[i:])[0]
			vis, sgr = append(vis, r), append(sgr, cur)
			i += len(string(r))
		}
		var row rowRead
		nameEnd := 0
		for nameEnd < len(vis) && vis[nameEnd] != ' ' {
			nameEnd++
		}
		row.name = string(vis[:nameEnd])
		prev := nameEnd - 1
		for k, e := range ends {
			if e >= len(vis) {
				t.Fatalf("row %q shorter than header column %s", row.name, names[k])
			}
			var c cellRead
			var sb strings.Builder
			for p := prev + 1; p <= e; p++ {
				sb.WriteRune(vis[p])
				if vis[p] != ' ' && c.sgr == "" && sgr[p] != "" {
					c.sgr = sgr[p]
				}
			}
			c.text = strings.TrimSpace(sb.String())
			row.cells = append(row.cells, c)
			if names[k] == "flags" {
				for g := 0; g < Width; g++ {
					p := e - Width + 1 + g
					row.glyphs[g] = cellRead{text: string(vis[p]), sgr: sgr[p]}
				}
			}
			prev = e
		}
		rows = append(rows, row)
	}
	return names, rows
}

// TestParseFrameSynthetic pins the parser on a hand-built two-row frame.
func TestParseFrameSynthetic(t *testing.T) {
	// Header names are right-aligned to each column's width (flags 6,
	// cum_share_z 11, breadth 9), cells likewise — as devview renders.
	frame := "09:45:00 ET  x\n" +
		"BASKET   flags  cum_share_z    breadth\n" +
		"alpha   " + sgrGreen + "Z" + reset + "··" + sgrGreen + "$" + reset + "··  " + sgrGreen + "       +2.5" + reset + "   2/3   1$\n" +
		"beta    ······         +1.0  " + sgrRed + " 0/5     " + reset + "\n" +
		"\nfooter\n"
	names, rows := parseFrame(t, frame)
	if strings.Join(names, " ") != "flags cum_share_z breadth" || len(rows) != 2 {
		t.Fatalf("names %v rows %d", names, len(rows))
	}
	a := rows[0]
	if a.name != "alpha" || a.glyphs[0] != (cellRead{"Z", sgrGreen}) || a.glyphs[1] != (cellRead{"·", ""}) || a.glyphs[3] != (cellRead{"$", sgrGreen}) {
		t.Errorf("alpha glyphs = %+v", a.glyphs)
	}
	if a.cells[1] != (cellRead{"+2.5", sgrGreen}) || a.cells[2] != (cellRead{"2/3   1$", ""}) {
		t.Errorf("alpha cells = %+v", a.cells)
	}
	b := rows[1]
	if b.cells[1] != (cellRead{"+1.0", ""}) || b.cells[2] != (cellRead{"0/5", sgrRed}) || b.glyphs[2].sgr != "" {
		t.Errorf("beta = %+v", b)
	}
}

var breadthRE = regexp.MustCompile(`^(\d+)/(\d+)(?:\s+(\S+)\$)?$`)

// TestFlagsAgreeWithCells is the G1 agreement test on a replayed frame:
// TRADER_FRAME names a file holding one rendered trader frame (the block
// from the "HH:MM:SS ET" line through the footer, as cmd/replay -view-at
// prints it). For every basket row, each glyph is lit ⇔ its cell is
// styled, in the same colour; `$` restates the breadth cell's own count.
// Skips when the file is not given (acceptance evidence, not CI input).
func TestFlagsAgreeWithCells(t *testing.T) {
	path := os.Getenv("TRADER_FRAME")
	if path == "" {
		t.Skip("TRADER_FRAME not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text() + "\n")
	}
	names, rows := parseFrame(t, sb.String())
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	if idx["flags"] != 0 {
		t.Fatalf("flags is column %d, want leftmost; header %v", idx["flags"], names)
	}
	pairs := []struct {
		slot int
		col  string
	}{{0, "cum_share_z"}, {1, "5m_z"}, {2, "breadth"}, {4, "delta"}, {5, "conv_z"}}
	lit := 0
	for _, r := range rows {
		for _, p := range pairs {
			ci, ok := idx[p.col]
			if !ok {
				continue // column not composed on this frame: slot must be unlit
			}
			g, c := r.glyphs[p.slot], r.cells[ci]
			if (g.sgr != "") != (c.sgr != "") || g.sgr != c.sgr {
				t.Errorf("%s: glyph %s %q/%q disagrees with cell %s %q/%q", r.name, Glyphs[p.slot], g.text, g.sgr, p.col, c.text, c.sgr)
			}
			if g.sgr == "" && g.text != gap {
				t.Errorf("%s: unlit slot %d shows %q", r.name, p.slot, g.text)
			}
			if g.sgr != "" {
				lit++
				if g.text != Glyphs[p.slot] {
					t.Errorf("%s: lit slot %d shows %q, want %s", r.name, p.slot, g.text, Glyphs[p.slot])
				}
			}
		}
		// `$`: restates the visible breadth cell — k ≥ 1 and 2k ≥ up.
		g := r.glyphs[3]
		want := false
		if m := breadthRE.FindStringSubmatch(r.cells[idx["breadth"]].text); m != nil && m[3] != "" {
			if k, err := strconv.Atoi(m[3]); err == nil {
				up, _ := strconv.Atoi(m[1])
				want = k >= 1 && 2*k >= up
			}
		}
		if got := g.sgr != ""; got != want || (got && (g.text != "$" || g.sgr != sgrGreen)) {
			t.Errorf("%s: $ glyph %q/%q vs breadth %q (want lit=%v)", r.name, g.text, g.sgr, r.cells[idx["breadth"]].text, want)
		}
		if got := len([]rune(strings.Join(func() []string {
			s := make([]string, Width)
			for i := range r.glyphs {
				s[i] = r.glyphs[i].text
			}
			return s
		}(), ""))); got != Width {
			t.Errorf("%s: %d glyph runes", r.name, got)
		}
	}
	t.Logf("%d rows, %d lit signed glyphs agree with their cells", len(rows), lit)
}
