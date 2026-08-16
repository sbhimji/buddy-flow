// Floors are the σ guard's per-minute floors (mini-spec 2.2, F13):
// zguard.Z divides by σ_used = max(σ_measured, floor). A floor is
// frac × the universe median of sigma_family at that ET minute-of-day, one
// per profiled family (counted shares, counted dollars). Floors derive from
// profiles, so they are computed here and written by the same build, into
// the profile dir as _floors.csv — the leading underscore keeps the name
// outside the per-ticker <SYMBOL>.csv namespace. Consumers read floor
// values, never the fraction (D3/D4).

package profile

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"buddy-flow/internal/session"
)

// FloorsFile is the floors filename inside a profile dir.
const FloorsFile = "_floors.csv"

// FloorRow is one ET minute-of-day's σ floors. FlowShare is the 3.1 family:
// frac × median across baskets of σ_share (see FlowShareFloors); the two
// volume families come from ComputeFloors.
type FloorRow struct {
	MinuteOfDay         int
	SigmaFloorShares    float64
	SigmaFloorDollars   float64
	SigmaFloorFlowShare float64
	SigmaFloorCumShare  float64
}

// Floors is the full session's floor table.
type Floors struct {
	Rows []FloorRow // indexed 0..389 = minute-of-day − session.OpenMinute
}

// Minute returns the floors for an ET minute-of-day, or false if the minute
// is outside the profiled session.
func (f *Floors) Minute(minuteOfDay int) (FloorRow, bool) {
	i := minuteOfDay - session.OpenMinute
	if i < 0 || i >= len(f.Rows) {
		return FloorRow{}, false
	}
	return f.Rows[i], true
}

// ComputeFloors derives the floor table from a full universe of profiles
// (D2): floor(minute, family) = frac × median across tickers of
// sigma_family(ticker, minute). MAD=0 tickers contribute their honest zeros
// to the median — the floor reflects the universe, not the thin name.
func ComputeFloors(profiles []Profile, frac float64) (*Floors, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no profiles to compute floors from")
	}
	if math.IsNaN(frac) || math.IsInf(frac, 0) || frac < 0 {
		return nil, fmt.Errorf("sigma floor frac must be a nonnegative number (got %v)", frac)
	}
	for _, p := range profiles {
		if len(p.Rows) != session.MinutesPerSession {
			return nil, fmt.Errorf("profile %s has %d rows, want %d", p.Symbol, len(p.Rows), session.MinutesPerSession)
		}
	}
	fl := &Floors{Rows: make([]FloorRow, session.MinutesPerSession)}
	shares := make([]float64, len(profiles))
	dollars := make([]float64, len(profiles))
	for i := range fl.Rows {
		for j, p := range profiles {
			shares[j] = p.Rows[i].SigmaShares
			dollars[j] = p.Rows[i].SigmaDollars
		}
		fl.Rows[i] = FloorRow{
			MinuteOfDay:       session.OpenMinute + i,
			SigmaFloorShares:  frac * median(shares),
			SigmaFloorDollars: frac * median(dollars),
		}
	}
	return fl, nil
}

var floorsHeader = []string{"minute_of_day", "sigma_floor_shares", "sigma_floor_dollars", "sigma_floor_flowshare", "sigma_floor_cumshare"}

// WriteFloors persists the floor table as <dir>/_floors.csv, atomically,
// deterministically. The leading comment line records the fraction and the
// build inputs; inputs must itself be deterministic for a given build
// (dates and counts, never wall-clock time).
func WriteFloors(dir string, fl *Floors, frac float64, inputs string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, FloorsFile)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# sigma floors (mini-spec 2.2): sigma_floor_frac=%s; built from %s\n", fnum(frac), inputs)
	fmt.Fprintln(w, strings.Join(floorsHeader, ","))
	for _, r := range fl.Rows {
		fmt.Fprintf(w, "%d,%s,%s,%s,%s\n", r.MinuteOfDay, fnum(r.SigmaFloorShares), fnum(r.SigmaFloorDollars), fnum(r.SigmaFloorFlowShare), fnum(r.SigmaFloorCumShare))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFloors loads <dir>/_floors.csv. Leading # comment lines are skipped;
// columns mapped by name; same compatibility contract as bucket.ReadCSV
// (see its doc). Row contiguity is a checked invariant, as in Read.
func ReadFloors(dir string) (*Floors, error) {
	path := filepath.Join(dir, FloorsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	headerLine := ""
	for sc.Scan() {
		line++
		if !strings.HasPrefix(sc.Text(), "#") {
			headerLine = sc.Text()
			break
		}
	}
	if headerLine == "" {
		return nil, fmt.Errorf("%s: no header line: %w", path, sc.Err())
	}
	names := strings.Split(headerLine, ",")
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	for _, n := range floorsHeader {
		if _, ok := idx[n]; !ok {
			return nil, fmt.Errorf("%s: missing column %q", path, n)
		}
	}
	fl := &Floors{}
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
		r := FloorRow{
			MinuteOfDay:         pI("minute_of_day"),
			SigmaFloorShares:    pF("sigma_floor_shares"),
			SigmaFloorDollars:   pF("sigma_floor_dollars"),
			SigmaFloorFlowShare: pF("sigma_floor_flowshare"),
			SigmaFloorCumShare:  pF("sigma_floor_cumshare"),
		}
		if perr != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, perr)
		}
		fl.Rows = append(fl.Rows, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(fl.Rows) != session.MinutesPerSession {
		return nil, fmt.Errorf("%s: %d rows, want %d", path, len(fl.Rows), session.MinutesPerSession)
	}
	for i, r := range fl.Rows {
		if r.MinuteOfDay != session.OpenMinute+i {
			return nil, fmt.Errorf("%s: row %d has minute_of_day %d, want %d (rows must be contiguous from %d)",
				path, i, r.MinuteOfDay, session.OpenMinute+i, session.OpenMinute)
		}
	}
	return fl, nil
}
