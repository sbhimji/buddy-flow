// Package profile builds and reads the 20-day per-ticker volume profiles
// (mini-spec 2.1, PR 2). Contract: bucket files in, profiles out — the
// builder is indifferent to whether a bucket file came from a live session,
// a capture replay, or a flat-file replay (that indifference is what makes
// bootstrap and nightly roll the same code path).
//
// A profile row is one (ticker, ET minute-of-day) bucket over the lookback
// days: median and robust sigma (MAD × 1.4826) of counted share and dollar
// volume. Counted = the $vol slice (mini-spec D2): CONTINUOUS + BLOCK +
// NON_PRICE_FORMING. Crosses are a separate ledger and never enter minute
// profiles; DUPLICATE/NON_FLOW/UNKNOWN never count anywhere.
//
// A ticker silent in a minute on some day contributes a true ZERO to that
// bucket's sample (F13) — thin names owe their thinness to the baseline.
// Sigma here is pre-floor: story 2.2 applies σ_used = max(σ, floor) at z
// time; MAD=0 is stored as 0, never invented away.
package profile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/classify"
	"buddy-flow/internal/session"
)

// MADConsistency scales MAD to estimate σ under normality (D6 default).
const MADConsistency = 1.4826

// Counted returns the $vol slice of a bucket (mini-spec 2.1 D2). Exported
// so every consumer of "counted volume" (the RVOL check today, Phase 3
// metrics tomorrow) reads the print-inclusion policy from one place.
func Counted(b bucket.Bucket) (shares, dollars float64) {
	for _, c := range []classify.Class{classify.Continuous, classify.Block, classify.NonPriceForming} {
		shares += b.Class[c].Shares
		dollars += b.Class[c].Dollars
	}
	return shares, dollars
}

// Row is one profiled minute bucket.
type Row struct {
	MinuteOfDay   int
	Days          int
	MedianShares  float64
	SigmaShares   float64
	MedianDollars float64
	SigmaDollars  float64
}

// Profile is one ticker's 390 regular-session minute buckets.
type Profile struct {
	Symbol string
	Rows   []Row // indexed 0..389 = minute-of-day − session.OpenMinute
}

// Day is one input session: its ET date and its bucket file.
type Day struct {
	Date string // "2006-01-02"
	Path string
}

// Build computes profiles for every symbol over the given days. Every
// symbol × minute gets exactly len(days) observations — absent rows are
// zeros, silence is data. Days must be distinct dates; order is irrelevant
// (each minute's sample is sorted before the median).
func Build(symbols []string, days []Day) ([]Profile, error) {
	if len(days) == 0 {
		return nil, fmt.Errorf("no input days")
	}
	seen := map[string]bool{}
	for _, d := range days {
		if seen[d.Date] {
			return nil, fmt.Errorf("duplicate date %s", d.Date)
		}
		seen[d.Date] = true
	}

	// samples[symbol][minuteIdx] = per-day counted volumes
	type acc struct{ shares, dollars []float64 }
	samples := make(map[string][]acc, len(symbols))
	for _, sym := range symbols {
		s := make([]acc, session.MinutesPerSession)
		for i := range s {
			s[i].shares = make([]float64, 0, len(days))
			s[i].dollars = make([]float64, 0, len(days))
		}
		samples[sym] = s
	}

	for _, d := range days {
		sess, err := bucket.ReadCSV(d.Path)
		if err != nil {
			return nil, fmt.Errorf("day %s: %w", d.Date, err)
		}
		// A misdated/misnamed file would contribute a full session of zeros
		// to every symbol with no other symptom — verify the data overlaps
		// the claimed date's REGULAR session (the window profiles read)
		// before trusting the filename.
		minSec, maxSec, ok := sess.Bounds()
		if !ok {
			return nil, fmt.Errorf("day %s: %s is empty", d.Date, d.Path)
		}
		openSec, err := session.BucketStart(d.Date, session.OpenMinute)
		if err != nil {
			return nil, err
		}
		closeSec, err := session.BucketStart(d.Date, session.CloseMinute)
		if err != nil {
			return nil, err
		}
		if maxSec < openSec || minSec >= closeSec {
			return nil, fmt.Errorf("day %s: %s has no data in that date's regular session (file spans %s .. %s ET dates)",
				d.Date, d.Path, session.Date(minSec*1e9), session.Date(maxSec*1e9))
		}
		// The minute→epoch table depends only on the date — hoist it out of
		// the symbol loop (389 symbols × 390 minutes of tz math otherwise).
		starts := make([]int64, session.MinutesPerSession)
		for m := session.OpenMinute; m < session.CloseMinute; m++ {
			if starts[m-session.OpenMinute], err = session.BucketStart(d.Date, m); err != nil {
				return nil, err
			}
		}
		for _, sym := range symbols {
			s := samples[sym]
			for m := session.OpenMinute; m < session.CloseMinute; m++ {
				min, err := sess.DeriveMinute(sym, starts[m-session.OpenMinute])
				if err != nil {
					return nil, err
				}
				sh, dl := Counted(min)
				i := m - session.OpenMinute
				s[i].shares = append(s[i].shares, sh)
				s[i].dollars = append(s[i].dollars, dl)
			}
		}
	}

	out := make([]Profile, 0, len(symbols))
	for _, sym := range symbols {
		p := Profile{Symbol: sym, Rows: make([]Row, session.MinutesPerSession)}
		for i, a := range samples[sym] {
			medS, sigS := robust(a.shares)
			medD, sigD := robust(a.dollars)
			p.Rows[i] = Row{
				MinuteOfDay: session.OpenMinute + i, Days: len(days),
				MedianShares: medS, SigmaShares: sigS,
				MedianDollars: medD, SigmaDollars: sigD,
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// robust returns median and MAD×1.4826. Mutates its argument (sorts).
func robust(xs []float64) (med, sigma float64) {
	med = median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		if x >= med {
			dev[i] = x - med
		} else {
			dev[i] = med - x
		}
	}
	return med, median(dev) * MADConsistency
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

var header = []string{"minute_of_day", "days", "median_shares", "sigma_shares", "median_dollars", "sigma_dollars"}

func fnum(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// Write persists profiles one CSV per ticker under dir (D4), atomically,
// deterministically. Path per ticker: <dir>/<SYMBOL>.csv.
func Write(dir string, profiles []Profile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range profiles {
		path := filepath.Join(dir, p.Symbol+".csv")
		tmp := path + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		fmt.Fprintln(w, strings.Join(header, ","))
		for _, r := range p.Rows {
			fmt.Fprintf(w, "%d,%d,%s,%s,%s,%s\n", r.MinuteOfDay, r.Days,
				fnum(r.MedianShares), fnum(r.SigmaShares), fnum(r.MedianDollars), fnum(r.SigmaDollars))
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

// Read loads one ticker's profile. Columns mapped by name; same
// compatibility contract as bucket.ReadCSV (see its doc).
func Read(dir, symbol string) (*Profile, error) {
	path := filepath.Join(dir, symbol+".csv")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return nil, fmt.Errorf("%s: empty file: %w", path, sc.Err())
	}
	names := strings.Split(sc.Text(), ",")
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	for _, n := range header {
		if _, ok := idx[n]; !ok {
			return nil, fmt.Errorf("%s: missing column %q", path, n)
		}
	}
	p := &Profile{Symbol: symbol}
	line := 1
	for sc.Scan() {
		line++
		fields := strings.Split(sc.Text(), ",")
		if len(fields) < len(names) {
			return nil, fmt.Errorf("%s:%d: %d fields, header has %d", path, line, len(fields), len(names))
		}
		var perr error
		pI := func(n string) int {
			v, err := strconv.Atoi(fields[idx[n]])
			if err != nil && perr == nil {
				perr = err
			}
			return v
		}
		pF := func(n string) float64 {
			v, err := strconv.ParseFloat(fields[idx[n]], 64)
			if err != nil && perr == nil {
				perr = err
			}
			return v
		}
		r := Row{
			MinuteOfDay: pI("minute_of_day"), Days: pI("days"),
			MedianShares: pF("median_shares"), SigmaShares: pF("sigma_shares"),
			MedianDollars: pF("median_dollars"), SigmaDollars: pF("sigma_dollars"),
		}
		if perr != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, perr)
		}
		p.Rows = append(p.Rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Minute() indexes rows positionally — make that a checked invariant,
	// not an assumption about what the file happened to contain.
	if len(p.Rows) != session.MinutesPerSession {
		return nil, fmt.Errorf("%s: %d rows, want %d", path, len(p.Rows), session.MinutesPerSession)
	}
	for i, r := range p.Rows {
		if r.MinuteOfDay != session.OpenMinute+i {
			return nil, fmt.Errorf("%s: row %d has minute_of_day %d, want %d (rows must be contiguous from %d)",
				path, i, r.MinuteOfDay, session.OpenMinute+i, session.OpenMinute)
		}
	}
	return p, nil
}

// Minute returns the profile row for an ET minute-of-day, or false if the
// minute is outside the profiled session.
func (p *Profile) Minute(minuteOfDay int) (Row, bool) {
	i := minuteOfDay - session.OpenMinute
	if i < 0 || i >= len(p.Rows) {
		return Row{}, false
	}
	return p.Rows[i], true
}
