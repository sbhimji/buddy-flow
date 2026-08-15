package bucket

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Session is a bucket file loaded for reading. Zero-materialization is the
// reader's job (D5/F13): Get returns a zero Bucket for any (symbol, second)
// with no row — 0/positive = 0. The whole-universe-silent 0/0 case
// (carry-forward-blank, rendered as gap) belongs to the metric layer, which
// can see the universe; this reader only answers per-symbol lookups.
type Session struct {
	rows map[string]map[int64]Bucket
}

// ReadCSV loads a session bucket file (plain or .gz — D4 gzips after close).
// Columns are mapped by header name, both directions of compatibility
// intended (D3):
//
//   - Newer files, this reader: unknown columns are ignored, so files
//     written after 3.3 adds columns still read here.
//   - Older files, future readers: whoever adds columns must read them as
//     OPTIONAL — absent from an old file means "not recorded then", never an
//     error and never a silent zero presented as a measurement. The 1.4
//     columns below are the permanent required base; only those may error
//     when missing.
func ReadCSV(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	if !sc.Scan() {
		return nil, fmt.Errorf("%s: empty file (no header): %w", path, sc.Err())
	}
	names := strings.Split(sc.Text(), ",")
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	col := func(name string) (int, error) {
		i, ok := idx[name]
		if !ok {
			return 0, fmt.Errorf("%s: missing column %q", path, name)
		}
		return i, nil
	}
	want := header()
	pos := make([]int, len(want))
	for i, name := range want {
		if pos[i], err = col(name); err != nil {
			return nil, err
		}
	}

	s := &Session{rows: map[string]map[int64]Bucket{}}
	line := 1
	for sc.Scan() {
		line++
		fields := strings.Split(sc.Text(), ",")
		get := func(i int) string { return fields[pos[i]] }
		if len(fields) < len(names) {
			return nil, fmt.Errorf("%s:%d: %d fields, header has %d", path, line, len(fields), len(names))
		}
		var b Bucket
		var sec int64
		var sym string
		var perr error
		pInt := func(v string) int64 {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil && perr == nil {
				perr = err
			}
			return n
		}
		pF := func(v string) float64 {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil && perr == nil {
				perr = err
			}
			return x
		}
		sec = pInt(get(0))
		sym = get(1)
		b.Trades = pInt(get(2))
		b.Shares = pF(get(3))
		b.Dollars = pF(get(4))
		b.LastPrice = pF(get(5))
		b.LastSipTs = pInt(get(6))
		b.Quotes = pInt(get(7))
		for c := 0; c < NumClasses; c++ {
			b.Class[c].Trades = pInt(get(8 + 3*c))
			b.Class[c].Shares = pF(get(9 + 3*c))
			b.Class[c].Dollars = pF(get(10 + 3*c))
		}
		if perr != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, perr)
		}
		m := s.rows[sym]
		if m == nil {
			m = map[int64]Bucket{}
			s.rows[sym] = m
		}
		m[sec] = b
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Get returns the bucket for (symbol, epoch-second), or a zero Bucket if no
// row exists — a silent second on an active tape is a true zero (F13).
func (s *Session) Get(symbol string, sec int64) Bucket {
	return s.rows[symbol][sec]
}

// DeriveMinute sums the sixty 1-second buckets [minuteSec, minuteSec+60) —
// the only way 1-minute bars come to exist (no second storage resolution).
// minuteSec is the epoch-second of the minute start; a non-aligned value is
// an error in the caller, not a silent misalignment.
func (s *Session) DeriveMinute(symbol string, minuteSec int64) (Bucket, error) {
	if minuteSec%60 != 0 {
		return Bucket{}, fmt.Errorf("minuteSec %d is not minute-aligned", minuteSec)
	}
	var out Bucket
	m := s.rows[symbol]
	for sec := minuteSec; sec < minuteSec+60; sec++ {
		if b, ok := m[sec]; ok {
			out.add(&b)
		}
	}
	return out, nil
}

// Symbols returns the symbols present in the file (unordered).
func (s *Session) Symbols() []string {
	out := make([]string, 0, len(s.rows))
	for sym := range s.rows {
		out = append(out, sym)
	}
	return out
}

// Len returns the number of (symbol, second) rows loaded.
func (s *Session) Len() int {
	n := 0
	for _, m := range s.rows {
		n += len(m)
	}
	return n
}
