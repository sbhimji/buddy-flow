# MO-4 — Shared read-only options follower + read locking

Status: **implemented on branch mo-4-options-follower, awaiting review** (written 2026-08-26). Prerequisite for MO-5. Not gated by
the equity stories (disjoint code) — can run in parallel with MO-2/MO-3.
Read first: `7.7-conviction-view.md` (follow mode, runbook), `7.4-options-buckets.md`,
backlog "Trader view as a separate process — follow-mode replay", backlog
"Shared websocket session runner".

## What

`cmd/replay-options/follow.go:runFollow` is the only code that can turn the
options capture file into a live `optbucket.Store`. MO-5 needs the same store
inside `cmd/live`. Extract it once, and make the store safe for a reader that
is not the feeder's peer.

New `internal/optfollow`:

    type Follower struct { Store *optbucket.Store; Base *conviction.Baselines; Stamp string; Stats *uwfeed.DecodeStats; ... }
    func Start(cfg Config) (*Follower, error)   // weights+stamp, baskets, optional profiles, store, pipeline, wait-for-file, FollowCapture on its own goroutine
    func (f *Follower) Stop()                    // closes pipeline, waits
    func (f *Follower) Done() <-chan struct{}    // session manifest seen or stopped

`cmd/replay-options -follow` becomes a thin caller (its `viewer` and
`Tick`-driven render unchanged — the Tick callback is exposed in `Config`).

## Decisions

- **F1 — Behaviour byte-identical for `replay-options -follow`.** Same
  wait-for-file loop (15 s poll), same first-frame message, same exit on
  manifest, same `follow ended:` line. The extraction is a move, not a
  rewrite; the follow-mode snapshot on a finished capture is the regression
  test.
- **F2 — Locking, stated.** `optbucket.Store` today has no mutex: the
  follower's "drain-then-render" synchronization works only because the
  renderer is the feeder's peer on the Tick callback. The equity render loop
  is not — it reads on its own goroutine while the options pipeline writes.
  Add an `RWMutex` to `optbucket.Store`: writers (`ObserveOptionTrade`) take
  it; `Get`/`Window`/`Bounds`/`Totals` take the read lock. `MaxSec` stays
  atomic. Cost is one uncontended lock per print — negligible at options
  print rates.
- **F3 — The minute reader for the ticker view.** `Follower.Minute(sym,
  minuteSec) (netConv, netNotional float64)` sums the store over
  `[minuteSec, minuteSec+60)` under the read lock — the function
  `tickerview.LoadOptions` takes. Same slice/derivation as the 7.7 basket
  snapshot (`SignedNotional` derived per 7.4).
- **F4 — Basket minute sums** for MO-5's `conv_z`/`net_z` come from the same
  reader summed over members — reuse `cmd/replay-options/view.go`'s basket
  aggregation by moving it into `internal/conviction` (`BasketMinute`), so
  :8788 and the equity table can never disagree.
- **F5 — Zero coupling to the capture process** (7.7 posture): the follower
  only ever opens the capture file read-only; killing it never touches
  `cmd/live-options`.

## Done when (replayed data)

1. `go run ./cmd/replay-options -follow -capture <finished 08-2x stream.jsonl>
   -profiles data/profiles-options` output byte-identical before/after
   (captured to a file, diffed).
2. Unit: a concurrent reader hammering `Window` while `ObserveOptionTrade`
   runs, under `go test -race` — clean.
3. `Follower.Minute` for one (symbol, minute) equals the 7.7 Python
   recomputation for that symbol-minute (the 7.7 script, per-ticker).
4. `conviction.BasketMinute` reproduces the three 7.7-verified basket-minutes
   (semis −0.0/−0.1, megacap +0.4/+0.4, photonics +2.7/+2.2 at 10:29 on 08-24).

## Decisions consumed

7.4 bucket derivations; 7.6 profiles + stamps; 7.7 V1–V5 and follow mode;
backlog "trader view as a separate process".
