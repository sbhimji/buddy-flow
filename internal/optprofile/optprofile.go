// Package optprofile builds the 20-day time-of-day matched baselines for
// the options tape (mini-spec 7.6): per-ticker and per-basket minute
// profiles of the two signed series (net_conviction, net_notional), plus
// the σ floors their z-scores require. Estimator: median / MAD×1.4826
// (D6). Every artifact carries the weights stamp and refuses a mismatch.
package optprofile

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// MinProfiledDays gates z computation (the 2.1/3.1 rule).
const MinProfiledDays = 10

// FloorsFile / BasketsDir mirror the equity layout.
const (
	FloorsFile = "_floors.csv"
	BasketsDir = "baskets"
)

// Day is one covered lookback day: its bucket session.
type Day struct {
	Date    string
	Session *optbucket.Session
}

// Row is one minute-of-day's baseline for both signed families.
type Row struct {
	MinuteOfDay       int
	Days              int
	MedianNetConv     float64
	SigmaNetConv      float64
	MedianNetNotional float64
	SigmaNetNotional  float64
}

// Profile is one ticker's (or basket's) 390 rows.
type Profile struct {
	Name    string   // symbol, or basket name
	Members []string // basket profiles only (sorted); nil for tickers
	Rows    []Row
}

// minuteValues returns, for one day, the (netconv, netnotional) of the
// given symbols summed per regular-session minute. Silent = 0 (P1).
func minuteValues(day Day, symbols []string) (conv, notional [session.MinutesPerSession]float64, err error) {
	for i := 0; i < session.MinutesPerSession; i++ {
		start, err := session.BucketStart(day.Date, session.OpenMinute+i)
		if err != nil {
			return conv, notional, err
		}
		for _, sym := range symbols {
			b, err := day.Session.DeriveMinute(sym, start)
			if err != nil {
				return conv, notional, err
			}
			conv[i] += b.NetConviction
			notional[i] += b.SignedNotional()
		}
	}
	return conv, notional, nil
}

// build folds per-day minute series into one profile.
func build(name string, members []string, symbols []string, days []Day) (Profile, error) {
	p := Profile{Name: name, Members: members, Rows: make([]Row, session.MinutesPerSession)}
	convSamples := make([][]float64, session.MinutesPerSession)
	notSamples := make([][]float64, session.MinutesPerSession)
	for _, d := range days {
		conv, notional, err := minuteValues(d, symbols)
		if err != nil {
			return p, fmt.Errorf("%s %s: %w", name, d.Date, err)
		}
		for i := range conv {
			convSamples[i] = append(convSamples[i], conv[i])
			notSamples[i] = append(notSamples[i], notional[i])
		}
	}
	for i := range p.Rows {
		r := &p.Rows[i]
		r.MinuteOfDay = session.OpenMinute + i
		r.Days = len(convSamples[i])
		r.MedianNetConv, r.SigmaNetConv = robust(convSamples[i])
		r.MedianNetNotional, r.SigmaNetNotional = robust(notSamples[i])
	}
	return p, nil
}

// Build returns per-ticker profiles (sorted by symbol).
func Build(symbols []string, days []Day) ([]Profile, error) {
	syms := append([]string(nil), symbols...)
	sort.Strings(syms)
	out := make([]Profile, 0, len(syms))
	for _, s := range syms {
		p, err := build(s, nil, []string{s}, days)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// BuildBaskets returns per-basket profiles as day-level member sums
// (the 3.1 D2 rule), sorted by basket name.
func BuildBaskets(baskets []universe.Basket, days []Day) ([]Profile, error) {
	bks := append([]universe.Basket(nil), baskets...)
	sort.Slice(bks, func(i, j int) bool { return bks[i].Name < bks[j].Name })
	out := make([]Profile, 0, len(bks))
	for _, bk := range bks {
		members := append([]string(nil), bk.Members...)
		sort.Strings(members)
		p, err := build(bk.Name, members, members, days)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// robust is median and MAD×1.4826 (D6). Empty → (0, 0).
func robust(xs []float64) (med, sigma float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	med = median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - med)
	}
	return med, median(dev) * profile.MADConsistency
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Floors holds per-minute σ floors for the four families.
type Floors struct {
	MinuteOfDay       []int
	NetConv           []float64 // per-ticker family
	NetNotional       []float64
	BasketNetConv     []float64 // basket family
	BasketNetNotional []float64
}

// ComputeFloors: floor(minute, family) = frac × median across profiles of
// that family's σ at that minute, over profiles meeting MinProfiledDays.
// No qualifying profile → floor 0 (zguard renders the null).
func ComputeFloors(tickers, baskets []Profile, frac float64) *Floors {
	n := session.MinutesPerSession
	fl := &Floors{
		MinuteOfDay: make([]int, n), NetConv: make([]float64, n), NetNotional: make([]float64, n),
		BasketNetConv: make([]float64, n), BasketNetNotional: make([]float64, n),
	}
	family := func(profs []Profile, pick func(Row) float64) []float64 {
		out := make([]float64, n)
		for i := 0; i < n; i++ {
			var sig []float64
			for _, p := range profs {
				if p.Rows[i].Days >= MinProfiledDays {
					sig = append(sig, pick(p.Rows[i]))
				}
			}
			if len(sig) > 0 {
				out[i] = frac * median(sig)
			}
		}
		return out
	}
	for i := range fl.MinuteOfDay {
		fl.MinuteOfDay[i] = session.OpenMinute + i
	}
	fl.NetConv = family(tickers, func(r Row) float64 { return r.SigmaNetConv })
	fl.NetNotional = family(tickers, func(r Row) float64 { return r.SigmaNetNotional })
	fl.BasketNetConv = family(baskets, func(r Row) float64 { return r.SigmaNetConv })
	fl.BasketNetNotional = family(baskets, func(r Row) float64 { return r.SigmaNetNotional })
	return fl
}

// --- persistence -----------------------------------------------------------

var rowHeader = []string{"minute_of_day", "days", "median_netconv", "sigma_netconv", "median_netnotional", "sigma_netnotional"}

func fnum(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func writeAtomic(path string, body func(w *bufio.Writer)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	body(w)
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeProfile(path string, p Profile, stamp, inputs string) error {
	return writeAtomic(path, func(w *bufio.Writer) {
		if p.Members != nil {
			fmt.Fprintf(w, "# 7.6 basket profile: basket=%s; members=%s; weights=%s; %s\n", p.Name, strings.Join(p.Members, " "), stamp, inputs)
		} else {
			fmt.Fprintf(w, "# 7.6 ticker profile: symbol=%s; weights=%s; %s\n", p.Name, stamp, inputs)
		}
		fmt.Fprintln(w, strings.Join(rowHeader, ","))
		for _, r := range p.Rows {
			fmt.Fprintf(w, "%d,%d,%s,%s,%s,%s\n", r.MinuteOfDay, r.Days,
				fnum(r.MedianNetConv), fnum(r.SigmaNetConv), fnum(r.MedianNetNotional), fnum(r.SigmaNetNotional))
		}
	})
}

// Write persists ticker profiles to <dir>/<SYM>.csv.
func Write(dir string, profiles []Profile, stamp, inputs string) error {
	for _, p := range profiles {
		if err := writeProfile(filepath.Join(dir, p.Name+".csv"), p, stamp, inputs); err != nil {
			return err
		}
	}
	return nil
}

// WriteBaskets persists basket profiles to <dir>/baskets/<basket>.csv.
func WriteBaskets(dir string, profiles []Profile, stamp, inputs string) error {
	for _, p := range profiles {
		if err := writeProfile(filepath.Join(dir, BasketsDir, p.Name+".csv"), p, stamp, inputs); err != nil {
			return err
		}
	}
	return nil
}

// WriteFloors persists <dir>/_floors.csv.
func WriteFloors(dir string, fl *Floors, frac float64, stamp, inputs string) error {
	return writeAtomic(filepath.Join(dir, FloorsFile), func(w *bufio.Writer) {
		fmt.Fprintf(w, "# 7.6 sigma floors: sigma_floor_frac=%s; weights=%s; %s\n", fnum(frac), stamp, inputs)
		fmt.Fprintln(w, "minute_of_day,sigma_floor_netconv,sigma_floor_netnotional,sigma_floor_basket_netconv,sigma_floor_basket_netnotional")
		for i, m := range fl.MinuteOfDay {
			fmt.Fprintf(w, "%d,%s,%s,%s,%s\n", m, fnum(fl.NetConv[i]), fnum(fl.NetNotional[i]), fnum(fl.BasketNetConv[i]), fnum(fl.BasketNetNotional[i]))
		}
	})
}

// headerField extracts "key=value" from a '#' header line split on ';'.
func headerField(line, key string) string {
	for _, part := range strings.Split(strings.TrimPrefix(line, "#"), ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, key+"="); ok {
			return v
		}
	}
	return ""
}

func readProfile(path, wantStamp string) (*Profile, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var p Profile
	var header string
	seenHeader := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			header = line
			if wantStamp != "" && headerField(line, "weights") != wantStamp {
				return nil, "", fmt.Errorf("%s: weights stamp %q does not match current config %q — rebuild profiles (weights changed)", path, headerField(line, "weights"), wantStamp)
			}
			continue
		}
		if !seenHeader {
			if line != strings.Join(rowHeader, ",") {
				return nil, "", fmt.Errorf("%s: unexpected columns %q", path, line)
			}
			seenHeader = true
			continue
		}
		fs := strings.Split(line, ",")
		if len(fs) != 6 {
			return nil, "", fmt.Errorf("%s: malformed row %q", path, line)
		}
		var r Row
		var perr error
		r.MinuteOfDay, perr = strconv.Atoi(fs[0])
		if perr != nil {
			return nil, "", fmt.Errorf("%s: %w", path, perr)
		}
		r.Days, perr = strconv.Atoi(fs[1])
		if perr != nil {
			return nil, "", fmt.Errorf("%s: %w", path, perr)
		}
		vals := [4]float64{}
		for i := range vals {
			if vals[i], perr = strconv.ParseFloat(fs[2+i], 64); perr != nil {
				return nil, "", fmt.Errorf("%s: %w", path, perr)
			}
		}
		r.MedianNetConv, r.SigmaNetConv, r.MedianNetNotional, r.SigmaNetNotional = vals[0], vals[1], vals[2], vals[3]
		if want := session.OpenMinute + len(p.Rows); r.MinuteOfDay != want {
			return nil, "", fmt.Errorf("%s: rows not contiguous at minute %d (want %d)", path, r.MinuteOfDay, want)
		}
		p.Rows = append(p.Rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, "", err
	}
	if len(p.Rows) != session.MinutesPerSession {
		return nil, "", fmt.Errorf("%s: %d rows, want %d", path, len(p.Rows), session.MinutesPerSession)
	}
	return &p, header, nil
}

// Read loads one ticker profile, refusing a stale weights stamp.
func Read(dir, symbol, wantStamp string) (*Profile, error) {
	p, _, err := readProfile(filepath.Join(dir, symbol+".csv"), wantStamp)
	if err != nil {
		return nil, err
	}
	p.Name = symbol
	return p, nil
}

// ReadBasket loads one basket profile, refusing a stale weights stamp or a
// membership that differs from the current config (D2a).
func ReadBasket(dir, basket string, wantMembers []string, wantStamp string) (*Profile, error) {
	p, header, err := readProfile(filepath.Join(dir, BasketsDir, basket+".csv"), wantStamp)
	if err != nil {
		return nil, err
	}
	p.Name = basket
	p.Members = strings.Fields(headerField(header, "members"))
	want := append([]string(nil), wantMembers...)
	sort.Strings(want)
	if strings.Join(p.Members, " ") != strings.Join(want, " ") {
		return nil, fmt.Errorf("baskets/%s.csv: membership %v differs from current config %v — rebuild profiles (membership changed)", basket, p.Members, want)
	}
	return p, nil
}

// ReadFloors loads _floors.csv, refusing a stale weights stamp.
func ReadFloors(dir, wantStamp string) (*Floors, error) {
	path := filepath.Join(dir, FloorsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fl := &Floors{}
	sc := bufio.NewScanner(f)
	seenHeader := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			if wantStamp != "" && headerField(line, "weights") != wantStamp {
				return nil, fmt.Errorf("%s: weights stamp mismatch — rebuild profiles", path)
			}
			continue
		}
		if !seenHeader {
			seenHeader = true
			continue
		}
		fs := strings.Split(line, ",")
		if len(fs) != 5 {
			return nil, fmt.Errorf("%s: malformed row %q", path, line)
		}
		m, err := strconv.Atoi(fs[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if want := session.OpenMinute + len(fl.MinuteOfDay); m != want {
			return nil, fmt.Errorf("%s: rows not contiguous at minute %d", path, m)
		}
		var v [4]float64
		for i := range v {
			if v[i], err = strconv.ParseFloat(fs[1+i], 64); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		fl.MinuteOfDay = append(fl.MinuteOfDay, m)
		fl.NetConv = append(fl.NetConv, v[0])
		fl.NetNotional = append(fl.NetNotional, v[1])
		fl.BasketNetConv = append(fl.BasketNetConv, v[2])
		fl.BasketNetNotional = append(fl.BasketNetNotional, v[3])
	}
	if len(fl.MinuteOfDay) != session.MinutesPerSession {
		return nil, fmt.Errorf("%s: %d rows, want %d", path, len(fl.MinuteOfDay), session.MinutesPerSession)
	}
	return fl, sc.Err()
}

// DiscoverDays lists covered days (canonical <date>.csv only, P1) in the
// bucket dir with date <= through, most recent n.
func DiscoverDays(bucketsDir, through string, n int) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(bucketsDir, "*.csv"))
	if err != nil {
		return nil, err
	}
	var dates []string
	for _, e := range entries {
		base := filepath.Base(e)
		if strings.Contains(base, ".partial.") || strings.Contains(base, ".trades-only.") {
			continue
		}
		d := strings.TrimSuffix(base, ".csv")
		if len(d) != 10 {
			continue
		}
		if through == "" || d <= through {
			dates = append(dates, d)
		}
	}
	sort.Strings(dates)
	if len(dates) > n {
		dates = dates[len(dates)-n:]
	}
	return dates, nil
}
