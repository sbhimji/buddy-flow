#!/usr/bin/env python3
"""Independent recomputation of MO-2 signed volume (story 3.3 part 1).

Replays one symbol's trades and quotes from a capture (stream.jsonl.gz) in
arrival order through a stdlib-Python port of the internal/aggressor
cascade, then compares one symbol-minute's signed family (ask/bid totals,
quote-ruled subset, tick_rule, late — counts and dollars) against the Go
bucket CSV (sum of the sixty 1-second rows). Optionally prints the honesty
split over a window from the bucket file.

WHAT THIS PROVES: that the storage plumbing is faithful — the Go store
records, sums, writes and re-reads exactly what the cascade decided, print
by print, in arrival order. It does NOT prove the cascade is a correct
classifier of aggressor side: the condition tables and the rules below are
hand copies of the Go policy (internal/classify, internal/aggressor), so a
policy error would be reproduced here identically. Loading the tables from
Go instead of copying them is a deferred item (MO-2 close notes).

Verification script, not product code. Stdlib only. Read-only w.r.t. data/
and Go code.

Usage:
  verify-aggressor.py --capture <stream.jsonl.gz> --buckets <buckets.csv>
      --symbol NVDA --minute 09:35 [--date 2026-08-24]
      [--window 09:30 10:00]
"""

import argparse
import csv
import gzip
import json
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

ET = ZoneInfo("America/New_York")

LATE_TOLERANCE_MS = 1000  # aggressor.LateTolerance (1s) on the wire's ms clock

# classify.saleConditions → class. Eligibility = CONTINUOUS only (review S1).
# Precedence is exclusion-dominant; anything unknown quarantines.
CLASS = {
    1: "CONT", 2: "DUP", 3: "CONT", 4: "CONT", 5: "NPF", 6: "CONT", 7: "NF",
    8: "XC", 9: "BLOCK", 10: "NPF", 11: "CONT", 12: "NF", 13: "NF", 14: "CONT",
    15: "NF", 16: "NF", 17: "XO", 18: "XR", 19: "XC", 20: "NF", 21: "NPF",
    22: "NPF", 23: "CONT", 24: "BLOCK", 25: "XO", 27: "CONT", 28: "XR",
    29: "NF", 30: "CONT", 31: "CONT", 32: "NPF", 33: "NPF", 34: "CONT",
    35: "CONT", 36: "CONT", 37: "CONT", 38: "NF", 52: "NPF", 53: "NPF", 55: "XO",
}
NEUTRAL = {41, 57, 58, 59, 60, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71}
PRECEDENCE = ["UNK", "NF", "DUP", "NPF", "XR", "XO", "XC", "BLOCK", "CONT"]


def classify(conds):
    best = "CONT"
    for c in conds or []:
        if c in NEUTRAL:
            continue
        k = CLASS.get(c, "UNK")
        if PRECEDENCE.index(k) < PRECEDENCE.index(best):
            best = k
    return best


# classify.QuoteUsable
QUOTE_UNUSABLE = {15, 17, 18, 19, 20, 21, 22, 23, 27, 32, 40, 43, 84, 85}
QUOTE_KNOWN = {1, 2, 3, 4, 5, 6, 7, 9, 10, 11, 12, 13, 14, 16, 26, 28, 29, 30,
               41, 42, 71, 81, 82, 94}


def quote_usable(c):
    if c is None:
        return True
    conds = c if isinstance(c, list) else [c]
    return all((x not in QUOTE_UNUSABLE) and (x in QUOTE_KNOWN) for x in conds)


class State:
    """aggressor.State (tick reference) + the book as of arrival (ingest's NBBO)."""

    def __init__(self):
        self.cur = None   # (bid, ask, ts_ms, usable): the latest quote, as ingest installs it
        self.last = None
        self.tick_ref = None

    def observe_quote(self, q):
        self.cur = (q.get("bp", 0.0), q.get("ap", 0.0), q["t"], quote_usable(q.get("c")))

    def book_for(self, t_ms):
        return self.cur


def cascade(st, t):
    """Returns (eligible, late, rule, side) for one trade; advances st.
    rule ∈ {None, 'quote', 'mid', 'tick'}; side ∈ {None, 'ask', 'bid'}."""
    cls = classify(t.get("c"))
    if cls != "CONT":
        return False, False, None, None
    pt = t.get("pt", 0)
    if pt == 0 or t["t"] - pt > LATE_TOLERANCE_MS:
        return True, True, None, None
    price = t["p"]
    ref = st.tick_ref
    if st.last is not None and price != st.last:
        ref = st.last
    rule, side = None, None
    book = st.book_for(t["t"])
    if book is not None:
        bid, ask, _, usable = book
        if bid > 0 and ask > 0 and bid <= ask and usable:
            unlocked = bid < ask
            mid = (bid + ask) / 2
            if price > ask or (unlocked and price == ask):
                rule, side = "quote", "ask"
            elif price < bid or (unlocked and price == bid):
                rule, side = "quote", "bid"
            elif price > mid:
                rule, side = "mid", "ask"
            elif price < mid:
                rule, side = "mid", "bid"
            else:
                rule = "tick"
                if ref is not None:
                    if price > ref:
                        side = "ask"
                    elif price < ref:
                        side = "bid"
    # advance reference (eligible on-time prints only)
    if st.last is None:
        st.last = price
    elif price != st.last:
        st.tick_ref = st.last
        st.last = price
    return True, False, rule, side


FIELDS = ["eligible", "ask_trades", "ask_dollars", "bid_trades", "bid_dollars",
          "quote_ask_trades", "quote_ask_dollars", "quote_bid_trades", "quote_bid_dollars",
          "tick_rule", "late"]


def zero():
    return {k: (0.0 if k.endswith("dollars") else 0) for k in FIELDS}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--capture", required=True)
    ap.add_argument("--buckets", required=True)
    ap.add_argument("--symbol", required=True)
    ap.add_argument("--minute", required=True, help="ET HH:MM")
    ap.add_argument("--date", default=None, help="YYYY-MM-DD (default: from bucket file)")
    ap.add_argument("--window", nargs=2, metavar=("FROM", "TO"),
                    help="ET HH:MM HH:MM: also print the honesty split over this window from the bucket file")
    a = ap.parse_args()

    # --- Go side: bucket CSV, sum of the minute's 1-second rows ------------
    rows = {}
    with open(a.buckets) as f:
        r = csv.DictReader(f)
        if "ask_dollars" not in r.fieldnames:
            sys.exit("bucket file has no signed columns (HasSigned=false)")
        for row in r:
            rows.setdefault(row["symbol"], {})[int(row["second"])] = row
    if a.date is None:
        first = min(min(m) for m in rows.values())
        a.date = datetime.fromtimestamp(first, ET).strftime("%Y-%m-%d")
    hh, mm = map(int, a.minute.split(":"))
    m0 = int(datetime(*map(int, a.date.split("-")), hh, mm, tzinfo=ET).timestamp())
    sym_rows = rows.get(a.symbol, {})
    go = zero()
    for sec in range(m0, m0 + 60):
        row = sym_rows.get(sec)
        if row is None:
            continue
        for k in FIELDS:
            if k == "eligible":
                go[k] += int(row["continuous_trades"])
            elif k.endswith("dollars"):
                go[k] += float(row[k])
            else:
                go[k] += int(row[k])

    # --- Python side: replay the symbol's messages in arrival order --------
    st = State()
    per_sec = {}  # sec -> sums in arrival order (matches Go's per-bucket +=)
    with gzip.open(a.capture, "rt") as f:
        for line in f:
            i = line.find(" ")
            try:
                evs = json.loads(line[i + 1:])
            except Exception:
                continue
            for e in evs:
                if e.get("sym") != a.symbol:
                    continue
                ev = e.get("ev")
                if ev == "Q":
                    st.observe_quote(e)
                elif ev == "T":
                    elig, late, rule, side = cascade(st, e)
                    if not elig:
                        continue
                    d = per_sec.setdefault(e["t"] // 1000, zero())
                    d["eligible"] += 1
                    if late:
                        d["late"] += 1
                    if rule == "tick":
                        d["tick_rule"] += 1
                    if side:
                        dollars = float(e["p"] * e["s"])
                        d[side + "_trades"] += 1
                        d[side + "_dollars"] += dollars
                        if rule in ("quote", "mid"):
                            d["quote_" + side + "_trades"] += 1
                            d["quote_" + side + "_dollars"] += dollars
    py = zero()
    for sec in range(m0, m0 + 60):
        d = per_sec.get(sec)
        if d is None:
            continue
        for k in FIELDS:
            py[k] += d[k]

    print(f"{a.symbol} {a.date} {a.minute} ET  (bucket file: {a.buckets})")
    ok = True
    for k in FIELDS:
        same = (go[k] == py[k])
        ok &= same
        print(f"  {k:18s} go={go[k]!r:<24} py={py[k]!r:<24} {'MATCH' if same else 'DIFF'}")
    print("RESULT:", "MATCH" if ok else "MISMATCH")

    if a.window:
        (fh, fm), (th, tm) = (map(int, a.window[0].split(":")), map(int, a.window[1].split(":")))
        w0 = int(datetime(*map(int, a.date.split("-")), fh, fm, tzinfo=ET).timestamp())
        w1 = int(datetime(*map(int, a.date.split("-")), th, tm, tzinfo=ET).timestamp())
        tot = {"eligible": 0, "ask": 0, "bid": 0, "quote": 0, "tick": 0, "late": 0,
               "elig_d": 0.0, "ask_d": 0.0, "bid_d": 0.0, "quote_d": 0.0, "block": 0}
        for sym, m in rows.items():
            for sec, row in m.items():
                if w0 <= sec < w1:
                    tot["eligible"] += int(row["continuous_trades"])
                    tot["block"] += int(row["block_trades"])
                    tot["elig_d"] += float(row["continuous_dollars"])
                    tot["ask"] += int(row["ask_trades"])
                    tot["bid"] += int(row["bid_trades"])
                    tot["quote"] += int(row["quote_ask_trades"]) + int(row["quote_bid_trades"])
                    tot["ask_d"] += float(row["ask_dollars"])
                    tot["bid_d"] += float(row["bid_dollars"])
                    tot["quote_d"] += float(row["quote_ask_dollars"]) + float(row["quote_bid_dollars"])
                    tot["tick"] += int(row["tick_rule"])
                    tot["late"] += int(row["late"])
        e = tot["eligible"] or 1
        ed = tot["elig_d"] or 1.0
        unc = tot["eligible"] - tot["ask"] - tot["bid"]
        nobook = unc - tot["late"]
        print(f"window {a.window[0]}-{a.window[1]} ET, all symbols: eligible={tot['eligible']} "
              f"(block prints, never signed: {tot['block']}) ask={100*tot['ask']/e:.1f}% bid={100*tot['bid']/e:.1f}% "
              f"quote={100*tot['quote']/e:.1f}% tick={100*tot['tick']/e:.1f}% late={100*tot['late']/e:.1f}% "
              f"nobook={100*nobook/e:.1f}% unclassified(incl late)={100*unc/e:.1f}%")
        print(f"  by dollars: ask={100*tot['ask_d']/ed:.1f}% bid={100*tot['bid_d']/ed:.1f}% "
              f"quote={100*tot['quote_d']/ed:.1f}% "
              f"unclassified(incl late)={100*(tot['elig_d']-tot['ask_d']-tot['bid_d'])/ed:.1f}%")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
