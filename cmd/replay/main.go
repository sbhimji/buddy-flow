// Command replay re-runs recorded market data — captured sessions or vendor
// flat files — through the same ingest pipeline the live command uses, and
// reports what flowed. It is the acceptance instrument for every story
// (stories close on replayed data, never live) and the regeneration tool for
// derived data such as bucket files.
//
// Examples:
//
//	go run ./cmd/replay -capture data/capture/2026-08-13/stream.jsonl -buckets data/buckets/2026-08-13.csv
//	go run ./cmd/replay -trades data/flat-files/trades/2026-08-11.csv.gz -quotes data/flat-files/quotes/2026-08-11.csv.gz
//	go run ./cmd/replay -quotes data/flat-files/quotes/2026-08-11.csv.gz -date 2026-08-11 -from 09:29:55 -to 09:35:00
//	go run ./cmd/replay -trades data/flat-files/trades/2026-08-11.csv.gz -full   # whole-market stress mode
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"buddy-flow/internal/breadth"
	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/feed"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

func main() {
	var (
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config (universe source of truth)")
		tradesPath  = flag.String("trades", "", "trades flat file (.csv or .csv.gz); empty = skip")
		quotesPath  = flag.String("quotes", "", "quotes flat file (.csv or .csv.gz); empty = skip")
		capturePath = flag.String("capture", "", "captured live session (stream.jsonl[.gz]) to replay; exercises the live decoder")
		speed       = flag.Float64("speed", 0, "capture replay pacing: 0=instant, 1=real-time (recorded spacing), N=accelerated ×N")
		full        = flag.Bool("full", false, "stress mode: skip the universe filter (whole-market load)")
		date        = flag.String("date", "", "session date YYYY-MM-DD (required with -from/-to)")
		fromS       = flag.String("from", "", "window start, ET HH:MM:SS inclusive")
		toS         = flag.String("to", "", "window end, ET HH:MM:SS exclusive")
		queueSize   = flag.Int("queue", 0, "pipeline queue size (0 = default)")
		nbboSyms    = flag.String("nbbo", "SPY,NVDA,AAPL", "comma-separated symbols to print final NBBO for (spot checks)")
		bucketsPath = flag.String("buckets", "", "write the 1.4 bucket store CSV here after the run; empty = no buckets")
		view        = flag.Bool("view", false, "3.0 developer view: auto-refreshing basket table (requires -capture; pace with -speed)")
		profilesDir = flag.String("profiles", "data/profiles", "profile directory for -view baselines")
		viewMode    = flag.String("view-mode", "dev", "-view column set: dev (all metrics, name order) or trader (trader-view-v0: cum-share story, cum-z sort, significance highlight)")
		viewAt      = flag.String("view-at", "", "also render the -view table as of this ET HH:MM:SS after the replay (spot checks; buckets are event-time keyed, so any past second is exact)")
	)
	flag.Parse()
	if *tradesPath == "" && *quotesPath == "" && *capturePath == "" {
		fmt.Fprintln(os.Stderr, "nothing to do: pass -trades, -quotes, and/or -capture")
		os.Exit(2)
	}
	if *speed != 0 && *capturePath == "" {
		fmt.Fprintln(os.Stderr, "-speed applies only to -capture replay")
		os.Exit(2)
	}
	// The view needs a session clock; flat-file replay streams trades and
	// quotes concurrently with meaningless cross-feed order, so "current
	// time" only exists on capture replay (mini-spec 3.0 D2).
	if *view && *capturePath == "" {
		fmt.Fprintln(os.Stderr, "-view applies only to -capture replay")
		os.Exit(2)
	}
	if *viewMode != "dev" && *viewMode != "trader" {
		fmt.Fprintln(os.Stderr, "-view-mode must be dev or trader")
		os.Exit(2)
	}
	if *viewAt != "" {
		if !*view {
			fmt.Fprintln(os.Stderr, "-view-at requires -view")
			os.Exit(2)
		}
		// Validate NOW: a malformed time must fail at flag parse, not after
		// minutes of replay have already run.
		if _, err := time.Parse("15:04:05", *viewAt); err != nil {
			fmt.Fprintf(os.Stderr, "-view-at must be ET HH:MM:SS: %v\n", err)
			os.Exit(2)
		}
	}
	if *capturePath != "" && (*tradesPath != "" || *quotesPath != "" || *fromS != "" || *toS != "" || *full || *date != "") {
		fmt.Fprintln(os.Stderr, "-capture replays alone (no -trades/-quotes/-from/-to/-full/-date)")
		os.Exit(2)
	}
	// A bucket file is a whole-session record; refuse runs that can only
	// produce a structurally partial store (windowed seconds, a feed missing,
	// non-universe symbols mixed in). Loud at flag parse, not a surprise file.
	// One deliberate door (mini-spec 2.1 D3): a trades-only run may write
	// buckets IF the filename self-declares it (`*.trades-only.csv`) — the
	// profile bootstrap needs volume, not quotes, and the name means nothing
	// downstream can mistake the file for a full session.
	if *bucketsPath != "" && *capturePath == "" {
		tradesOnly := strings.HasSuffix(*bucketsPath, bucket.TradesOnlySuffix)
		switch {
		case tradesOnly && (*tradesPath == "" || *quotesPath != ""):
			fmt.Fprintln(os.Stderr, "a .trades-only.csv bucket file takes -trades alone")
			os.Exit(2)
		case !tradesOnly && (*tradesPath == "" || *quotesPath == ""):
			fmt.Fprintln(os.Stderr, "-buckets needs both -trades and -quotes (a one-feed bucket file would record a session that had no quotes/trades); trades-only is allowed iff the path ends in .trades-only.csv")
			os.Exit(2)
		case *fromS != "" || *toS != "":
			fmt.Fprintln(os.Stderr, "-buckets cannot be windowed (-from/-to): the file would masquerade as a full session")
			os.Exit(2)
		case *full:
			fmt.Fprintln(os.Stderr, "-buckets with -full would mix non-universe symbols into a session bucket file")
			os.Exit(2)
		}
	}

	opt := feed.Options{Full: *full}
	if *fromS != "" || *toS != "" {
		if *date == "" {
			fmt.Fprintln(os.Stderr, "-from/-to require -date YYYY-MM-DD")
			os.Exit(2)
		}
		opt.FromNs = mustET(*date, *fromS)
		opt.ToNs = mustET(*date, *toS)
	}

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	table := ingest.NewTable(syms)
	p := ingest.NewPipeline(table, *queueSize)

	var store *bucket.Store
	if *bucketsPath != "" || *view {
		store = bucket.NewStore()
	}
	// The view wraps the store (delegates + tracks the replayed clock), so
	// one observer slot serves both; -buckets still writes the same store.
	// Exactly one SetObserver call — the pipeline has a single slot, and a
	// second call would silently replace the first.
	var dv *devview.View
	if *view {
		bks, err := universe.LoadBaskets(*basketsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if dv, err = devview.New(store, table, bks, *profilesDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// 3.1 columns: share profiles + floors are hard requirements when
		// viewing — a missing baseline must fail loudly, not render fake
		// zeros (same posture as devview.New on per-ticker profiles).
		unionStates, err := flowshare.Union(bks, table)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		shares, floors, err := flowshare.LoadBaselines(*profilesDir, bks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v (rebuild with cmd/profiles?)\n", err)
			os.Exit(1)
		}
		bc, err := breadth.New(store, table)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if *viewMode == "trader" {
			cols, rank, footer := flowshare.TraderColumns(store, unionStates, shares, floors, bc.Column(true))
			dv.SetColumns(cols)
			dv.SetRank(rank)
			dv.SetFooter(footer)
			dv.SetStatus(bc.Status)
		} else {
			for _, c := range flowshare.Columns(store, unionStates, shares, floors) {
				dv.Register(c)
			}
			dv.Register(bc.Column(false))
			dv.Register(bc.DetailColumn())
		}
	}
	switch { // before Run starts (pipeline contract)
	case dv != nil:
		p.SetObserver(dv)
	case store != nil:
		p.SetObserver(store)
	}

	fmt.Printf("universe: %d symbols  full=%v  window=[%s, %s)\n", table.Len(), *full, *fromS, *toS)

	done := make(chan struct{})
	go func() { p.Run(); close(done) }()

	// Capture replay (mini-spec 1.2 done-when #2): one sequential stream
	// through the live decoder, then the same report path as flat files.
	if *capturePath != "" {
		stopRender := startRenderLoop(dv)
		start := time.Now()
		ls, err := feed.StreamCapture(*capturePath, p, feed.ReplayOptions{
			Speed: *speed,
			Log:   func(f string, a ...any) { fmt.Printf("replay: "+f+"\n", a...) },
		})
		p.Close()
		<-done
		stopRender()
		if *viewAt != "" {
			renderViewAt(dv, store, *viewAt)
		}
		fmt.Printf("capture: frames=%d trades=%d quotes=%d status=%d unknown=%d decode-errs=%d elapsed=%s\n",
			ls.Frames, ls.Trades, ls.Quotes, ls.Status, ls.Unknown, ls.DecodeErrs,
			time.Since(start).Round(time.Millisecond))
		// The absolute lost-message check must hold on the acceptance path
		// too: replay-twice determinism cannot catch a DETERMINISTIC loss —
		// the same messages vanish both times (review #5).
		if submitted := ls.Trades + ls.Quotes; p.Processed.Load() != submitted {
			fmt.Printf("!! processed=%d != submitted=%d — pipeline lost messages\n", p.Processed.Load(), submitted)
			os.Exit(1)
		}
		if *view {
			reportState(table, nil, false) // table is the product; totals only
		} else {
			reportState(table, splitCSV(*nbboSyms), true)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture replay failed (stats above are partial): %v\n", err)
			os.Exit(3) // no bucket write: a partial file at the target path would masquerade as a session
		}
		// Captures are the one input that can legitimately produce a
		// non-spanning store (late start, smoke run) — enforce D3 naming
		// from the data itself rather than trusting the caller. Only when a
		// file is actually being written: -view alone also builds a store.
		if *bucketsPath != "" {
			if nerr := validateCaptureBucketName(store, *bucketsPath); nerr != nil {
				fmt.Fprintln(os.Stderr, nerr)
				os.Exit(2)
			}
			writeBuckets(store, *bucketsPath) // reports the tripwire itself
		} else {
			reportTripwire(store) // -view-only runs still build a store; its tripwire must still be read
		}
		return
	}

	// Trades and quotes stream concurrently — cross-symbol order in the
	// ticker-sorted flat files is meaningless anyway (see feed package doc);
	// final state and counts are deterministic regardless of interleaving.
	// A feeder error (truncated gzip, missing column) is carried back here so
	// the run still drains, reports partial stats, and exits distinctly —
	// never os.Exit mid-goroutine, which would discard the other feeder's
	// work and alias the "lost messages" exit code.
	type result struct {
		stats feed.Stats
		err   error
	}
	start := time.Now()
	var wg sync.WaitGroup
	results := map[string]result{}
	var resMu sync.Mutex
	for _, f := range []struct {
		path string
		kind ingest.Kind
		name string
	}{
		{*tradesPath, ingest.KindTrade, "trades"},
		{*quotesPath, ingest.KindQuote, "quotes"},
	} {
		if f.path == "" {
			continue
		}
		wg.Add(1)
		go func(path string, kind ingest.Kind, name string) {
			defer wg.Done()
			st, err := feed.StreamFile(path, kind, p, opt)
			resMu.Lock()
			results[name] = result{st, err}
			resMu.Unlock()
		}(f.path, f.kind, f.name)
	}
	wg.Wait()
	p.Close()
	<-done
	elapsed := time.Since(start)

	inputErr := false
	for name, r := range results {
		if r.err != nil {
			inputErr = true
			fmt.Fprintf(os.Stderr, "%s feeder failed (stats below are partial): %v\n", name, r.err)
		}
	}

	// --- report ---
	windowed := opt.FromNs != 0 || opt.ToNs != 0
	var totalSubmitted int64
	for _, name := range []string{"trades", "quotes"} {
		r, ok := results[name]
		if !ok {
			continue
		}
		st := r.stats
		fmt.Printf("%s: rows=%d submitted=%d filtered=%d windowed=%d malformed=%d\n",
			name, st.Rows, st.Submitted, st.Filtered, st.Windowed, st.Malformed)
		totalSubmitted += st.Submitted
	}
	fmt.Printf("elapsed=%s  processed=%d  max-queue-depth=%d  cond-overflow=%d\n",
		elapsed.Round(time.Millisecond), p.Processed.Load(),
		p.MaxQueueDepth.Load(), p.CondOverflow.Load())
	if windowed {
		// A windowed run still scans the whole file; submitted/elapsed would
		// measure the scan, not the pipeline. Refuse to print a number that
		// looks like a floor measurement (mini-spec 1.1, review finding #2).
		fmt.Printf("throughput: n/a on windowed runs (whole file is scanned; rate would not measure the pipeline)\n")
	} else {
		fmt.Printf("throughput=%.0f msg/sec (end-to-end lower bound: includes gzip+CSV scan of all rows)\n",
			float64(totalSubmitted)/elapsed.Seconds())
	}
	if p.Processed.Load() != totalSubmitted {
		fmt.Printf("!! processed != submitted — pipeline lost messages\n")
		os.Exit(1)
	}

	// Stress-mode extras: without this line a -full run hides most of its
	// load from the report (review finding #8).
	if *full {
		var xT, xQ, xRegT, xRegQ int64
		extras := table.Extras()
		for _, s := range extras {
			xT += s.Trades.Load()
			xQ += s.Quotes.Load()
			xRegT += s.SeqRegressTrades.Load()
			xRegQ += s.SeqRegressQuotes.Load()
		}
		fmt.Printf("extra (non-universe) symbols: %d  trades=%d quotes=%d  seq-regressions: trades=%d quotes=%d\n",
			len(extras), xT, xQ, xRegT, xRegQ)
	}

	if opt.FromNs != 0 {
		// Quotes are state-building: a -from window truncates each symbol's
		// quote history, so the "final book" is an artifact, not an "as of"
		// (review finding #4). Suppress rather than print a wrong book.
		reportState(table, nil, true)
		fmt.Printf("nbbo: suppressed (-from truncates quote history; books would not be \"as of\" any instant)\n")
	} else {
		reportState(table, splitCSV(*nbboSyms), true)
	}

	if inputErr {
		os.Exit(3) // bad input file — distinct from exit 1 (lost messages) and 2 (flag misuse)
	}
	writeBuckets(store, *bucketsPath)
}

// startRenderLoop drives the -view refresh: re-render whenever the REPLAYED
// clock has crossed a second (checked on a wall-clock ticker — the renderer
// itself is pure and never sees wall time). Instant replay naturally
// collapses to a render or two plus the final one. The returned stop must
// be called after the pipeline drains; it prints the final table into
// scrollback (no clear) so the session's last state survives above the
// stats. No-op when dv is nil.
func startRenderLoop(dv *devview.View) (stop func()) {
	if dv == nil {
		return func() {}
	}
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		var last int64
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				if sec := dv.ClockSec(); sec > last {
					last = sec
					fmt.Print("\033[H\033[2J" + dv.Render(sec))
				}
			}
		}
	}()
	return func() {
		close(quit)
		wg.Wait()
		if sec := dv.ClockSec(); sec > 0 {
			fmt.Println()
			fmt.Print(dv.Render(sec))
		}
	}
}

// hhmm formats an epoch second as ET wall-clock time.
func hhmm(sec int64) string { return time.Unix(sec, 0).In(session.ET()).Format("15:04:05") }

// renderViewAt prints the -view table as of an ET HH:MM:SS on the replayed
// session's date (buckets are event-time keyed, so a post-hoc render is
// exact). Diagnostics, never silence: an empty replay says so, and a second
// outside the captured span is flagged — an all-zero table must be
// distinguishable from "you asked about time we have no data for". hms is
// already format-validated at flag parse.
func renderViewAt(dv *devview.View, store *bucket.Store, hms string) {
	clockSec := dv.ClockSec()
	if clockSec == 0 {
		fmt.Fprintf(os.Stderr, "-view-at %s: replay produced no messages; no session date to resolve against\n", hms)
		return
	}
	sec := mustET(session.Date(clockSec*1e9), hms) / 1e9
	if minSec, maxSec, ok := store.Bounds(); ok && (sec < minSec || sec > maxSec) {
		fmt.Fprintf(os.Stderr, "note: -view-at %s is outside the captured span (%s..%s ET) — the table below reads zero because there is no data there\n",
			hms, hhmm(minSec), hhmm(maxSec))
	}
	fmt.Println()
	fmt.Print(dv.Render(sec))
}

// reportTripwire prints the 0.3 unknown-condition tripwire. Consulted on
// every path that builds a store: writeBuckets covers -buckets runs, and
// the -view-only path calls it directly — a tripwire nobody reads is no
// tripwire. Nil-safe.
func reportTripwire(store *bucket.Store) {
	if store == nil {
		return
	}
	if n, ids := store.Unknown(); n > 0 {
		fmt.Printf("!! tripwire: %d prints carried condition IDs missing from the 0.3 table: %v\n", n, ids)
	}
}

// validateCaptureBucketName enforces the D3 coverage-naming contract on the
// one path that produces partial files: a capture-derived store that does
// not span the regular session (coverage through the 16:00 closing cross)
// must carry the .partial.csv suffix, and a spanning one must not — a
// mislabeled full session would silently vanish from baselines. Captures
// carry both feeds by construction, so a spanning store missing either side
// is a broken subscription, not a session. The session date comes from the
// data, not a flag.
func validateCaptureBucketName(store *bucket.Store, path string) error {
	if strings.HasSuffix(path, bucket.TradesOnlySuffix) {
		return fmt.Errorf("%s names are for flat-file bootstrap runs; a capture replay carries quotes", bucket.TradesOnlySuffix)
	}
	minSec, maxSec, ok := store.Bounds()
	if !ok {
		return nil // empty store: nothing to misrepresent
	}
	date, spans, err := bucket.SpansRegularSession(minSec, maxSec)
	if err != nil {
		return err
	}
	partial := strings.HasSuffix(path, bucket.PartialSuffix)
	if spans {
		if trades, quotes := store.Totals(); trades == 0 || quotes == 0 {
			return fmt.Errorf("capture spans %s but has trades=%d quotes=%d — a feed is missing; not writing a session bucket file", date, trades, quotes)
		}
	}
	if spans && partial {
		return fmt.Errorf("capture spans the regular session on %s (%s..%s ET): drop the %s suffix", date, hhmm(minSec), hhmm(maxSec), bucket.PartialSuffix)
	}
	if !spans && !partial {
		return fmt.Errorf("capture does not span the regular session on %s (data %s..%s ET vs 09:30-16:00 incl. close): name the file *%s so it cannot enter baselines", date, hhmm(minSec), hhmm(maxSec), bucket.PartialSuffix)
	}
	return nil
}

// writeBuckets persists the 1.4 store and reports the unknown-condition
// tripwire. No-op when -buckets was not given. Only called on clean runs —
// a bucket file from a partial run would masquerade as a full session.
func writeBuckets(store *bucket.Store, path string) {
	if store == nil || path == "" {
		return
	}
	rows, err := store.WriteCSV(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bucket write failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("buckets: %d (second,symbol) rows -> %s\n", rows, path)
	reportTripwire(store)
}

// reportState prints universe totals, (when top is set) the busiest
// symbols, and (when requested) final NBBO for the spot-check symbols.
// Shared by the flat-file and capture-replay paths so both report
// identically — done-when #2 of mini-spec 1.2 compares exactly these lines
// across runs. -view runs pass top=false and no nbboSyms (owner request
// 2026-08-16): the table is the product there and the spot-check lines are
// clutter; the universe-totals line stays everywhere — it is the
// lost-message reconciliation evidence.
func reportState(table *ingest.Table, nbboSyms []string, top bool) {
	var uTrades, uQuotes, regT, regQ int64
	type symCount struct {
		sym string
		n   int64
	}
	var busiest []symCount
	for _, s := range table.All() {
		t, q := s.Trades.Load(), s.Quotes.Load()
		uTrades += t
		uQuotes += q
		regT += s.SeqRegressTrades.Load()
		regQ += s.SeqRegressQuotes.Load()
		if t+q > 0 {
			busiest = append(busiest, symCount{s.Symbol, t + q})
		}
	}
	fmt.Printf("universe totals: trades=%d quotes=%d  seq-regressions: trades=%d quotes=%d  active-symbols=%d/%d\n",
		uTrades, uQuotes, regT, regQ, len(busiest), table.Len())

	if top {
		sort.Slice(busiest, func(i, j int) bool {
			if busiest[i].n != busiest[j].n {
				return busiest[i].n > busiest[j].n
			}
			return busiest[i].sym < busiest[j].sym // tie-break for deterministic output
		})
		fmt.Printf("top symbols by messages:")
		for i := 0; i < len(busiest) && i < 10; i++ {
			fmt.Printf("  %s=%d", busiest[i].sym, busiest[i].n)
		}
		fmt.Println()
	}

	for _, sym := range nbboSyms {
		st := table.Lookup(sym)
		if st == nil {
			fmt.Printf("nbbo %s: not in universe\n", sym)
			continue
		}
		n := st.NBBO()
		fmt.Printf("nbbo %s: bid %.4f x %d (exch %d)  ask %.4f x %d (exch %d)  ts=%d seq=%d\n",
			sym, n.BidPrice, n.BidSize, n.BidExch, n.AskPrice, n.AskSize, n.AskExch, n.Ts, n.Seq)
	}
}

// mustET converts "YYYY-MM-DD" + "HH:MM:SS" in America/New_York to epoch ns.
func mustET(date, hms string) int64 {
	if hms == "" {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+hms, session.ET())
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad time %q %q: %v\n", date, hms, err)
		os.Exit(2)
	}
	return t.UnixNano()
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
