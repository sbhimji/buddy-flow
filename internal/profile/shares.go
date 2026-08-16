// Share profiles are the 3.1 FlowShare baselines (mini-spec
// docs/mini-specs/3.1-flowshare.md): per basket, per ET minute-of-day, the
// median and robust σ of the basket's daily FlowShare over the lookback —
// each day's share computed first, then median/MAD over days, because
// medians do not pass through division and share σ is dominated by
// basket↔universe co-movement that per-ticker σs cannot see (D2).
//
// These are a DERIVED CACHE, not a second store of record: per-ticker
// profiles and bucket files remain the only stored truth. Each file's
// header records the member list it was built from, and the reader refuses
// a file whose membership differs from the current config (D2a) — a basket
// edit can never serve a silently stale baseline; the remedy is the cheap
// nightly rebuild.
//
// Zero-sum caveat (D1, verbatim per the dev plan): shares are zero-sum only
// INSIDE the tracked universe — flow leaving the universe entirely
// redistributes shares misleadingly (mitigated by 3.7's de-grossing
// precedence). The denominator is the union of distinct basket members
// (benchmarks excluded); numerators keep full membership, so overlapping
// baskets' shares can sum >100%.

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
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// SharesDir is the subdirectory of a profile dir holding basket share
// profiles: <profileDir>/baskets/<basket>.csv.
const SharesDir = "baskets"

// ShareRow is one profiled minute bucket of a basket's FlowShare, plus the
// D7 since-open cumulative family: the day's share of the tape from the
// open THROUGH this minute. The two families sample independently — a
// silent minute has no per-minute share but usually a defined cumulative
// one (anything traded earlier keeps the cumulative denominator >0).
type ShareRow struct {
	MinuteOfDay    int
	Days           int // per-minute samples present (D2b/D3 skips excluded)
	MedianShare    float64
	SigmaShare     float64
	CumDays        int // cumulative samples present
	MedianCumShare float64
	SigmaCumShare  float64
}

// ShareProfile is one basket's 390 share buckets plus the membership it was
// built from (the D2a staleness stamp).
type ShareProfile struct {
	Basket  string
	Members []string // sorted, deduped — as built
	Rows    []ShareRow
}

// Minute returns the share row for an ET minute-of-day, or false outside
// the profiled session.
func (p *ShareProfile) Minute(minuteOfDay int) (ShareRow, bool) {
	i := minuteOfDay - session.OpenMinute
	if i < 0 || i >= len(p.Rows) {
		return ShareRow{}, false
	}
	return p.Rows[i], true
}

// SkippedDay records a lookback day dropped by the D2b coverage rule and
// the members that caused it.
type SkippedDay struct {
	Date      string
	Uncovered []string
}

// BuildShares computes every basket's share profile over the given days.
// Denominator: union of distinct basket members, each once (D1). A day on
// which any union member printed zero trades across the whole regular
// session is skipped for EVERY basket — an uncovered member corrupts the
// common denominator (D2b). A minute whose universe counted $vol is 0
// contributes no sample (D3: 0/0 is not a zero share).
func BuildShares(baskets []universe.Basket, days []Day) ([]ShareProfile, []SkippedDay, error) {
	if len(baskets) == 0 {
		return nil, nil, fmt.Errorf("no baskets")
	}
	if len(days) == 0 {
		return nil, nil, fmt.Errorf("no input days")
	}
	// Same duplicate-date guard as Build: a repeated day would double-count
	// its share samples and bias the median with no other symptom. (A
	// MISDATED file, unlike in Build, degrades loudly here without its own
	// guard: every symbol reads zero in the claimed session, so D2b skips
	// the day naming all of them.)
	seen := map[string]bool{}
	for _, d := range days {
		if seen[d.Date] {
			return nil, nil, fmt.Errorf("duplicate date %s", d.Date)
		}
		seen[d.Date] = true
	}
	unionSet := map[string]bool{}
	for _, b := range baskets {
		for _, m := range b.Members {
			unionSet[m] = true
		}
	}
	union := make([]string, 0, len(unionSet))
	for s := range unionSet {
		union = append(union, s)
	}
	sort.Strings(union)

	// samples[basketIdx][minuteIdx] = per-day share values; cumSamples the
	// same for the since-open cumulative family (D7).
	samples := make([][][]float64, len(baskets))
	cumSamples := make([][][]float64, len(baskets))
	for i := range samples {
		samples[i] = make([][]float64, session.MinutesPerSession)
		cumSamples[i] = make([][]float64, session.MinutesPerSession)
	}
	var skipped []SkippedDay

	for _, d := range days {
		sess, err := bucket.ReadCSV(d.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("day %s: %w", d.Date, err)
		}
		starts := make([]int64, session.MinutesPerSession)
		for m := session.OpenMinute; m < session.CloseMinute; m++ {
			if starts[m-session.OpenMinute], err = session.BucketStart(d.Date, m); err != nil {
				return nil, nil, err
			}
		}
		// Per-symbol counted dollars per minute, plus raw print counts for
		// the D2b coverage rule (coverage looks at ALL prints — a symbol
		// whose only activity was crosses still traded that day).
		dollars := map[string][]float64{}
		var uncovered []string
		for _, sym := range union {
			ds := make([]float64, session.MinutesPerSession)
			var prints int64
			for i := range ds {
				min, err := sess.DeriveMinute(sym, starts[i])
				if err != nil {
					return nil, nil, err
				}
				_, ds[i] = Counted(min)
				prints += min.Trades
			}
			dollars[sym] = ds
			if prints == 0 {
				uncovered = append(uncovered, sym)
			}
		}
		if len(uncovered) > 0 {
			skipped = append(skipped, SkippedDay{Date: d.Date, Uncovered: uncovered})
			continue
		}
		cumUni := 0.0
		cumBasket := make([]float64, len(baskets))
		for i := 0; i < session.MinutesPerSession; i++ {
			var uni float64
			for _, sym := range union {
				uni += dollars[sym][i]
			}
			cumUni += uni
			for bi, b := range baskets {
				var bd float64
				for _, m := range b.Members {
					bd += dollars[m][i]
				}
				cumBasket[bi] += bd
				if uni != 0 { // 0/0 minute: no per-minute sample (D3)
					samples[bi][i] = append(samples[bi][i], bd/uni)
				}
				if cumUni != 0 { // cumulative samples once ANYTHING has traded (D7)
					cumSamples[bi][i] = append(cumSamples[bi][i], cumBasket[bi]/cumUni)
				}
			}
		}
	}

	out := make([]ShareProfile, 0, len(baskets))
	for bi, b := range baskets {
		p := ShareProfile{
			Basket:  b.Name,
			Members: append([]string(nil), b.Members...),
			Rows:    make([]ShareRow, session.MinutesPerSession),
		}
		for i, xs := range samples[bi] {
			med, sig := robust(xs)
			cumMed, cumSig := robust(cumSamples[bi][i])
			p.Rows[i] = ShareRow{
				MinuteOfDay: session.OpenMinute + i, Days: len(xs),
				MedianShare: med, SigmaShare: sig,
				CumDays:        len(cumSamples[bi][i]),
				MedianCumShare: cumMed, SigmaCumShare: cumSig,
			}
		}
		out = append(out, p)
	}
	return out, skipped, nil
}

var sharesHeader = []string{"minute_of_day", "days", "median_share", "sigma_share",
	"cum_days", "median_cum_share", "sigma_cum_share"}

// WriteShares persists share profiles under <dir>/baskets/, one CSV per
// basket, atomically, deterministically. The header comment carries the
// D2a membership stamp and the build inputs (which must themselves be
// deterministic — dates and counts, never wall-clock time).
func WriteShares(dir string, profiles []ShareProfile, inputs string) error {
	sub := filepath.Join(dir, SharesDir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return err
	}
	for _, p := range profiles {
		path := filepath.Join(sub, p.Basket+".csv")
		tmp := path + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		fmt.Fprintf(w, "# 3.1 share profile (derived cache): basket=%s; members=%s; built from %s\n",
			p.Basket, strings.Join(p.Members, " "), inputs)
		fmt.Fprintln(w, strings.Join(sharesHeader, ","))
		for _, r := range p.Rows {
			fmt.Fprintf(w, "%d,%d,%s,%s,%d,%s,%s\n", r.MinuteOfDay, r.Days, fnum(r.MedianShare), fnum(r.SigmaShare),
				r.CumDays, fnum(r.MedianCumShare), fnum(r.SigmaCumShare))
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

var membersStamp = "members="

// ReadShares loads one basket's share profile and enforces the D2a
// staleness contract: wantMembers (sorted, deduped — the CURRENT config)
// must equal the membership recorded at build time, or the read fails
// naming the basket. Columns mapped by name; contiguity validated.
func ReadShares(dir, basket string, wantMembers []string) (*ShareProfile, error) {
	path := filepath.Join(dir, SharesDir, basket+".csv")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var built []string
	headerLine := ""
	line := 0
	for sc.Scan() {
		line++
		t := sc.Text()
		if !strings.HasPrefix(t, "#") {
			headerLine = t
			break
		}
		if i := strings.Index(t, membersStamp); i >= 0 {
			rest := t[i+len(membersStamp):]
			if j := strings.Index(rest, ";"); j >= 0 {
				rest = rest[:j]
			}
			built = strings.Fields(rest)
		}
	}
	if headerLine == "" {
		return nil, fmt.Errorf("%s: no header line: %w", path, sc.Err())
	}
	if built == nil {
		return nil, fmt.Errorf("%s: no membership stamp in header comment (not a share profile?)", path)
	}
	if !equalStrings(built, wantMembers) {
		return nil, fmt.Errorf("share profile for basket %s is stale: built with members %v, config now has %v — rebuild profiles",
			basket, built, wantMembers)
	}
	names := strings.Split(headerLine, ",")
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	for _, n := range sharesHeader {
		if _, ok := idx[n]; !ok {
			return nil, fmt.Errorf("%s: missing column %q", path, n)
		}
	}
	p := &ShareProfile{Basket: basket, Members: append([]string(nil), built...)}
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
		r := ShareRow{
			MinuteOfDay: pI("minute_of_day"), Days: pI("days"),
			MedianShare: pF("median_share"), SigmaShare: pF("sigma_share"),
			CumDays:        pI("cum_days"),
			MedianCumShare: pF("median_cum_share"), SigmaCumShare: pF("sigma_cum_share"),
		}
		if perr != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, perr)
		}
		p.Rows = append(p.Rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// shareFamilyFloors derives one σ floor family over the basket share
// profiles (D4/D7): per minute, frac × median across baskets of that
// family's σ, the median taken over baskets with a DEFINED σ at that
// minute (≥1 sampled day). No defined σ → floor 0 (z then nulls via
// zguard's 0/0 rule — a defined gap, never an invented floor).
func shareFamilyFloors(profiles []ShareProfile, frac float64, sample func(ShareRow) (sigma float64, days int)) ([]float64, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no share profiles to compute floors from")
	}
	for _, p := range profiles {
		if len(p.Rows) != session.MinutesPerSession {
			return nil, fmt.Errorf("share profile %s has %d rows, want %d", p.Basket, len(p.Rows), session.MinutesPerSession)
		}
	}
	out := make([]float64, session.MinutesPerSession)
	sigmas := make([]float64, 0, len(profiles))
	for i := range out {
		sigmas = sigmas[:0]
		for _, p := range profiles {
			if sigma, days := sample(p.Rows[i]); days >= 1 {
				sigmas = append(sigmas, sigma)
			}
		}
		if len(sigmas) > 0 {
			out[i] = frac * median(sigmas)
		}
	}
	return out, nil
}

// FlowShareFloors is the per-minute share family's floor (D4), indexed 0..389.
func FlowShareFloors(profiles []ShareProfile, frac float64) ([]float64, error) {
	return shareFamilyFloors(profiles, frac, func(r ShareRow) (float64, int) { return r.SigmaShare, r.Days })
}

// CumShareFloors is the since-open cumulative family's floor (D7) — its own
// family because cumulative shares are far less volatile than per-minute
// shares; borrowing the per-minute floor would over-damp every cum z.
func CumShareFloors(profiles []ShareProfile, frac float64) ([]float64, error) {
	return shareFamilyFloors(profiles, frac, func(r ShareRow) (float64, int) { return r.SigmaCumShare, r.CumDays })
}
