// Package optbucket is the 1-second options bucket store (mini-spec 7.4,
// the 1.4 analog): per-underlying × epoch-second aggregation of classified
// prints, one resolution all session, coarser views derived at read time.
// The store owns the 7.3 classifier; every derived file carries the
// weights stamp so stale-weights artifacts refuse loudly.
package optbucket

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
)

// Bucket is one underlying's one-second aggregate. The six premium slices
// are the raw classified aggregates (unweighted signed notional derives
// from them at read time); NetConviction must be stored — it depends on
// per-print context (OI, moneyness, DTE) no aggregate preserves.
type Bucket struct {
	Prints    int64
	Contracts int64

	PremCallAsk, PremCallBid, PremCallMid float64 // Mid = mid_side + no_side + absent
	PremPutAsk, PremPutBid, PremPutMid    float64

	PremSweep     float64 // premium on sweep-coded prints, any side
	NetConviction float64 // Σ signed_notional × conviction_weight (weights-dependent)

	PrintsNoUprice int64 // D17 neutral-moneyness prints
	PrintsZeroOI   int64 // D16 neutral-size prints
}

// Store implements optingest.Observer. Single-goroutine access (the
// pipeline's consumer), same contract as bucket.Store.
type Store struct {
	weights     optclassify.Weights
	weightsName string // "<version>@<hash>", the artifact stamp
	buckets     map[string]map[int64]*Bucket

	cls    *optclassify.Classifier // lazily bound to the first print's ET date (B2)
	clsErr error                   // sticky init failure — fails WriteCSV loudly
	loc    *time.Location

	// MaxSec is the latest event-time second observed — the render clock
	// for paced/follow views (atomic: read from the render goroutine).
	MaxSec atomic.Int64

	Unclassifiable int64 // K3 bad-expiry prints, stamped into the header (B5)
	SweepPrints    int64 // classification-rate telemetry (7.3 done-when 3)
	SidePrints     [3]int64
}

// Side telemetry indices.
const (
	sideZero = 0 // mid/no_side/absent
	sideAsk  = 1
	sideBid  = 2
)

// NewStore binds a weights config to an empty store. weightsName is the
// stamp ("options-weights-v1@<hash>") from optclassify.LoadWeights.
func NewStore(w optclassify.Weights, weightsName string) (*Store, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	return &Store{
		weights:     w,
		weightsName: weightsName,
		buckets:     map[string]map[int64]*Bucket{},
		loc:         loc,
	}, nil
}

// ObserveOptionTrade classifies one print and folds it into its event-time
// second (B1).
func (s *Store) ObserveOptionTrade(t *optingest.OptionTrade) {
	if s.cls == nil && s.clsErr == nil {
		date := time.Unix(0, t.ExecNs).In(s.loc).Format("2006-01-02")
		s.cls, s.clsErr = optclassify.NewClassifier(s.weights, date)
	}
	if s.clsErr != nil {
		return // sticky; WriteCSV reports it
	}
	p, ok := s.cls.Classify(t)
	if !ok {
		s.Unclassifiable++
		return
	}

	sec := t.ExecNs / 1_000_000_000
	if sec > s.MaxSec.Load() {
		s.MaxSec.Store(sec)
	}
	bySec := s.buckets[t.Underlying]
	if bySec == nil {
		bySec = map[int64]*Bucket{}
		s.buckets[t.Underlying] = bySec
	}
	b := bySec[sec]
	if b == nil {
		b = &Bucket{}
		bySec[sec] = b
	}

	b.Prints++
	b.Contracts += t.Size
	switch {
	case t.IsCall && p.Sign > 0:
		b.PremCallAsk += t.Premium
	case t.IsCall && p.Sign < 0:
		b.PremCallBid += t.Premium
	case t.IsCall:
		b.PremCallMid += t.Premium
	case p.Sign > 0:
		b.PremPutAsk += t.Premium
	case p.Sign < 0:
		b.PremPutBid += t.Premium
	default:
		b.PremPutMid += t.Premium
	}
	if p.IsSweep {
		b.PremSweep += t.Premium
		s.SweepPrints++
	}
	b.NetConviction += p.Weighted
	if p.NoUPrice {
		b.PrintsNoUprice++
	}
	if p.ZeroOI {
		b.PrintsZeroOI++
	}
	switch p.Sign {
	case 1:
		s.SidePrints[sideAsk]++
	case -1:
		s.SidePrints[sideBid]++
	default:
		s.SidePrints[sideZero]++
	}
}

// Get returns one bucket (nil if silent). Window sums [fromSec, toSec).
func (s *Store) Get(sym string, sec int64) *Bucket { return s.buckets[sym][sec] }

func (s *Store) Window(sym string, fromSec, toSec int64) Bucket {
	var out Bucket
	bySec := s.buckets[sym]
	for sec := fromSec; sec < toSec; sec++ {
		if b := bySec[sec]; b != nil {
			out.Add(b)
		}
	}
	return out
}

func (o *Bucket) Add(b *Bucket) {
	o.Prints += b.Prints
	o.Contracts += b.Contracts
	o.PremCallAsk += b.PremCallAsk
	o.PremCallBid += b.PremCallBid
	o.PremCallMid += b.PremCallMid
	o.PremPutAsk += b.PremPutAsk
	o.PremPutBid += b.PremPutBid
	o.PremPutMid += b.PremPutMid
	o.PremSweep += b.PremSweep
	o.NetConviction += b.NetConviction
	o.PrintsNoUprice += b.PrintsNoUprice
	o.PrintsZeroOI += b.PrintsZeroOI
}

// SignedNotional derives the unweighted net from the stored slices:
// (call ask − call bid) − (put ask − put bid).
func (b *Bucket) SignedNotional() float64 {
	return (b.PremCallAsk - b.PremCallBid) - (b.PremPutAsk - b.PremPutBid)
}

// Bounds reports the observed second range (ok=false when empty).
func (s *Store) Bounds() (minSec, maxSec int64, ok bool) {
	for _, bySec := range s.buckets {
		for sec := range bySec {
			if !ok {
				minSec, maxSec, ok = sec, sec, true
				continue
			}
			if sec < minSec {
				minSec = sec
			}
			if sec > maxSec {
				maxSec = sec
			}
		}
	}
	return
}

// Totals reports prints and contracts across the store.
func (s *Store) Totals() (prints, contracts int64) {
	for _, bySec := range s.buckets {
		for _, b := range bySec {
			prints += b.Prints
			contracts += b.Contracts
		}
	}
	return
}

// Path / PartialPath mirror the 1.4 naming policy in the options dir.
func Path(dir, date string) string        { return filepath.Join(dir, date+".csv") }
func PartialPath(dir, date string) string { return filepath.Join(dir, date+".partial.csv") }

// columns is the self-describing header; readers map by name
// (column-compatibility contract: future columns append here).
var columns = []string{
	"second", "symbol", "prints", "contracts",
	"prem_call_ask", "prem_call_bid", "prem_call_mid",
	"prem_put_ask", "prem_put_bid", "prem_put_mid",
	"prem_sweep", "net_conviction", "prints_no_uprice", "prints_zero_oi",
}

// WriteCSV persists the store: stamped header, sorted rows, .tmp+rename,
// shortest-round-trip floats (B4). Returns rows written.
func (s *Store) WriteCSV(path string) (int, error) {
	if s.clsErr != nil {
		return 0, fmt.Errorf("classifier never initialized: %w", s.clsErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 1<<20)

	fmt.Fprintf(w, "# options buckets (mini-spec 7.4): weights=%s; unclassifiable=%d\n", s.weightsName, s.Unclassifiable)
	fmt.Fprintln(w, strings.Join(columns, ","))

	syms := make([]string, 0, len(s.buckets))
	for sym := range s.buckets {
		syms = append(syms, sym)
	}
	sort.Strings(syms)

	type row struct {
		sec int64
		sym string
		b   *Bucket
	}
	var rows []row
	for _, sym := range syms {
		for sec, b := range s.buckets[sym] {
			rows = append(rows, row{sec, sym, b})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].sec != rows[j].sec {
			return rows[i].sec < rows[j].sec
		}
		return rows[i].sym < rows[j].sym
	})

	n := 0
	for _, r := range rows {
		b := r.b
		fmt.Fprintf(w, "%d,%s,%d,%d,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d\n",
			r.sec, r.sym, b.Prints, b.Contracts,
			fnum(b.PremCallAsk), fnum(b.PremCallBid), fnum(b.PremCallMid),
			fnum(b.PremPutAsk), fnum(b.PremPutBid), fnum(b.PremPutMid),
			fnum(b.PremSweep), fnum(b.NetConviction), b.PrintsNoUprice, b.PrintsZeroOI)
		n++
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, path)
}

// fnum is the shortest round-tripping float form (the 1.4 determinism rule).
func fnum(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// SpansRegularSession re-exports the 1.4 coverage rule for the naming
// decision (B3).
func SpansRegularSession(minSec, maxSec int64) (date string, spans bool, err error) {
	return bucket.SpansRegularSession(minSec, maxSec)
}

// Session is a read-back bucket file: symbol → second → Bucket.
type Session struct {
	Buckets        map[string]map[int64]*Bucket
	WeightsName    string
	Unclassifiable int64
}

// ReadCSV loads a bucket file, refusing a weights-stamp mismatch when
// wantStamp is non-empty (the D2a analog — a stale-weights file demands a
// rebuild, never a silent blend). Columns map by name; unknown columns are
// ignored (column-compatibility contract).
func ReadCSV(path, wantStamp string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sess := &Session{Buckets: map[string]map[int64]*Bucket{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var colIdx map[string]int
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			for _, part := range strings.Split(strings.TrimPrefix(line, "#"), ";") {
				part = strings.TrimSpace(part)
				if v, ok := strings.CutPrefix(part, "options buckets (mini-spec 7.4): weights="); ok {
					sess.WeightsName = v
				}
				if v, ok := strings.CutPrefix(part, "unclassifiable="); ok {
					sess.Unclassifiable, _ = strconv.ParseInt(v, 10, 64)
				}
			}
			continue
		}
		if colIdx == nil {
			colIdx = map[string]int{}
			for i, c := range strings.Split(line, ",") {
				colIdx[c] = i
			}
			for _, c := range columns {
				if _, ok := colIdx[c]; !ok {
					return nil, fmt.Errorf("%s: missing column %q", path, c)
				}
			}
			if wantStamp != "" && sess.WeightsName != wantStamp {
				return nil, fmt.Errorf("%s: weights stamp %q does not match current config %q — rebuild buckets from capture (weights changed)", path, sess.WeightsName, wantStamp)
			}
			continue
		}
		fields := strings.Split(line, ",")
		get := func(c string) string { return fields[colIdx[c]] }
		sec, err := strconv.ParseInt(get("second"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: bad second %q", path, get("second"))
		}
		sym := get("symbol")
		b := &Bucket{}
		ints := []struct {
			c string
			p *int64
		}{{"prints", &b.Prints}, {"contracts", &b.Contracts}, {"prints_no_uprice", &b.PrintsNoUprice}, {"prints_zero_oi", &b.PrintsZeroOI}}
		for _, x := range ints {
			if *x.p, err = strconv.ParseInt(get(x.c), 10, 64); err != nil {
				return nil, fmt.Errorf("%s: bad %s %q", path, x.c, get(x.c))
			}
		}
		floats := []struct {
			c string
			p *float64
		}{{"prem_call_ask", &b.PremCallAsk}, {"prem_call_bid", &b.PremCallBid}, {"prem_call_mid", &b.PremCallMid},
			{"prem_put_ask", &b.PremPutAsk}, {"prem_put_bid", &b.PremPutBid}, {"prem_put_mid", &b.PremPutMid},
			{"prem_sweep", &b.PremSweep}, {"net_conviction", &b.NetConviction}}
		for _, x := range floats {
			if *x.p, err = strconv.ParseFloat(get(x.c), 64); err != nil {
				return nil, fmt.Errorf("%s: bad %s %q", path, x.c, get(x.c))
			}
		}
		bySec := sess.Buckets[sym]
		if bySec == nil {
			bySec = map[int64]*Bucket{}
			sess.Buckets[sym] = bySec
		}
		bySec[sec] = b
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if colIdx == nil {
		return nil, fmt.Errorf("%s: empty file", path)
	}
	return sess, nil
}

// DeriveMinute sums one symbol's 60 seconds from a minute-aligned start.
func (s *Session) DeriveMinute(sym string, minuteSec int64) (Bucket, error) {
	if minuteSec%60 != 0 {
		return Bucket{}, fmt.Errorf("minute start %d not aligned", minuteSec)
	}
	var out Bucket
	bySec := s.Buckets[sym]
	for sec := minuteSec; sec < minuteSec+60; sec++ {
		if b := bySec[sec]; b != nil {
			out.Add(b)
		}
	}
	return out, nil
}
