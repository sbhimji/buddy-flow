package classify

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Acceptance test for story 0.3: every condition code observed in one full
// recorded session must classify to a non-UNKNOWN class (policy doc, mini-spec).
//
// Input: a Massive flat-file trades CSV for a full session day at the
// hardcoded tradesCSV path (gitignored). Plain .csv or .csv.gz both work.
// Skips when the file is not present so the unit suite stays green without
// the data.
//
// Also reports (informational, per docs/backlog.md): id 55 occurrences with
// sample context, the full condition-ID histogram, and the class distribution
// by prints and dollars.
// tradesCSV is the session file under test. Hardcoded for now; rename the
// downloaded file to trades.csv (or edit this constant to match its name).
const tradesCSV = "../../data/trades/2026-08-11.csv.gz"

func TestAcceptanceFullSession(t *testing.T) {
	path := tradesCSV
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("%s not present yet — drop the flat file to run acceptance", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var reader io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gzip %s: %v", path, err)
		}
		defer gz.Close()
		reader = gz
	}

	cr := csv.NewReader(reader)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(strings.ToLower(name))] = i
	}
	for _, required := range []string{"ticker", "conditions", "price", "size", "sip_timestamp"} {
		if _, ok := col[required]; !ok {
			t.Fatalf("column %q not found in header %v", required, header)
		}
	}

	type id55Sample struct{ ticker, ts, size string }
	var (
		rows        int64
		condCounts  = map[int32]int64{}
		classPrints = map[Class]int64{}
		classDollar = map[Class]float64{}
		unknownIDs  = map[int32]int64{}
		id55Samples []id55Sample
		condBuf     []int32
	)

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("row %d: %v", rows+2, err)
		}
		rows++

		condBuf = parseConditions(rec[col["conditions"]], condBuf[:0])
		for _, id := range condBuf {
			condCounts[id]++
		}

		class, unknown := Classify(condBuf)
		classPrints[class]++
		for _, id := range unknown {
			unknownIDs[id]++
		}

		price, _ := strconv.ParseFloat(rec[col["price"]], 64)
		size, _ := strconv.ParseFloat(rec[col["size"]], 64)
		classDollar[class] += price * size

		for _, id := range condBuf {
			if id == 55 && len(id55Samples) < 20 {
				id55Samples = append(id55Samples, id55Sample{
					ticker: rec[col["ticker"]],
					ts:     rec[col["sip_timestamp"]],
					size:   rec[col["size"]],
				})
			}
		}
	}

	if rows == 0 {
		t.Fatalf("%s contained no data rows", path)
	}
	t.Logf("file: %s — %d trades", path, rows)

	// Condition-ID histogram, descending by count.
	ids := make([]int32, 0, len(condCounts))
	for id := range condCounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return condCounts[ids[i]] > condCounts[ids[j]] })
	var hist strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&hist, "  id %-3d %12d\n", id, condCounts[id])
	}
	t.Logf("condition-ID histogram (%d distinct):\n%s", len(ids), hist.String())

	// Class distribution.
	var dist strings.Builder
	for c := Unknown; c <= Continuous; c++ {
		if classPrints[c] > 0 {
			fmt.Fprintf(&dist, "  %-18s %12d prints  $%.0f\n", c.String(), classPrints[c], classDollar[c])
		}
	}
	t.Logf("class distribution:\n%s", dist.String())

	// Backlog id 55 report (informational — provisional class, does not fail).
	if n := condCounts[55]; n > 0 {
		t.Logf("id 55 observed %d times — co-occurrence analysis needed per docs/backlog.md; samples: %v", n, id55Samples)
	} else {
		t.Logf("id 55: not observed this session (backlog item stays open on the runtime tripwire)")
	}

	// THE acceptance criterion: no observed code may be unknown to the table.
	if len(unknownIDs) > 0 {
		t.Errorf("condition IDs observed on the tape but missing from the normative table (classify UNKNOWN): %v — classify them in docs/foundations/print-inclusion.md, then update the table here", unknownIDs)
	}
}

// parseConditions parses the flat-file conditions field. Massive encodes it as
// separator-joined numeric IDs; tolerate space, comma, or semicolon so a vendor
// format wobble doesn't silently misparse. Unparseable tokens are reported via
// a sentinel ID -1 (which the table does not contain, so it surfaces as UNKNOWN
// in the acceptance run rather than vanishing).
func parseConditions(field string, buf []int32) []int32 {
	field = strings.TrimSpace(field)
	if field == "" {
		return buf
	}
	for _, tok := range strings.FieldsFunc(field, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';'
	}) {
		n, err := strconv.Atoi(tok)
		if err != nil {
			n = -1
		}
		buf = append(buf, int32(n))
	}
	return buf
}
