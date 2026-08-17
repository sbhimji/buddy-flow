#!/usr/bin/env python3
"""Independent recomputation of the trader view's cum_share / cum_share_typ /
cum_share_z columns (3.1 D7, auction-inclusive slice per the 2026-08-16
amendment), following the pattern of verify.py.

Usage:
    go run ./cmd/replay -capture data/capture/2026-08-14/stream.jsonl \
        -view -view-mode trader -view-at 13:01:00 > /tmp/view.out
    python3 discovery-scripts/view-verify/verify-cum.py /tmp/view.out

Python side: bucket CSVs read directly, stdlib only. Slice: auction-inclusive
= continuous + block + non_price_forming + cross_open + cross_reopen +
cross_close dollars (profile.CountedWithAuctions). Baseline:
data/profiles/baskets/<basket>.csv at the completed minute's minute_of_day;
floor from _floors.csv sigma_floor_cumshare; z = (share − median) /
max(σ, floor), printed digits compared against the rendered table.

Window restriction (mini-spec 3.1 done-when #5): the 08-14 capture starts
11:51:35 ET, so the Go store's [09:30, 13:01) cum window effectively holds
[11:51:35, 13:01). Two source subtleties, both measured 2026-08-16:

  --source=flat (default): data/buckets/2026-08-14.trades-only.csv, the
  SIP-flat-file tape — fully independent of the capture path. The capture
  joined MID-second at 11:51:35, so that boundary second is taken from the
  capture-derived partial.csv (the flat file holds ~$4.0M the capture never
  saw). Even so, capture vs SIP flat file differ slightly across the window
  (same reason verify.py carries tolerances): expect a few LAST-PRINTED-DIGIT
  flips (2026-08-16 run: 18/22 exact; 4 rows off by 0.01% of share or 0.1σ
  at rounding boundaries — consumption_software, defense_tech_growth,
  memory_storage, photonics_optics).

  --source=capture: data/buckets/2026-08-14.partial.csv, derived from the
  same capture the view replays — verifies the view MATH (window, slice,
  share division, baseline lookup, σ guard) with no data-source delta:
  all 22 baskets must match to the printed digit.

Verification script, not product code. Read-only w.r.t. data/ and Go code.
"""

import argparse
import csv
import json
import os
import re
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

ET = ZoneInfo("America/New_York")

CAPTURE_START_SEC = 1786722695  # first second in 2026-08-14.partial.csv (11:51:35 ET)

AUCTION_COLS = ["continuous_dollars", "block_dollars", "non_price_forming_dollars",
                "cross_open_dollars", "cross_reopen_dollars", "cross_close_dollars"]


def load_dollars(path, from_sec, to_sec):
    """{symbol: auction-inclusive dollars} over [from_sec, to_sec)."""
    d = {}
    with open(path, newline="") as f:
        for row in csv.DictReader(f):
            sec = int(row["second"])
            if from_sec <= sec < to_sec:
                v = sum(float(row[c]) for c in AUCTION_COLS)
                d[row["symbol"]] = d.get(row["symbol"], 0.0) + v
    return d


def skip_comments(path):
    return [l for l in open(path) if not l.startswith("#")]


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("view_stdout", help="stdout of the trader-mode -view-at replay")
    ap.add_argument("--date", default="2026-08-14")
    ap.add_argument("--at", default="13:01:00", help="render second, ET HH:MM:SS")
    ap.add_argument("--source", choices=["flat", "capture"], default="flat")
    ap.add_argument("--profiles", default="data/profiles")
    ap.add_argument("--baskets",
                    default="docs/foundations/morning-tape-baskets-v2.json")
    args = ap.parse_args()

    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo = os.path.abspath(os.path.join(script_dir, "..", ".."))

    h, m, s = (int(x) for x in args.at.split(":"))
    end_sec = int(datetime(*[int(x) for x in args.date.split("-")], h, m, s,
                           tzinfo=ET).timestamp())
    end_sec = (end_sec // 60) * 60  # end of last completed minute
    key = datetime.fromtimestamp(end_sec - 60, ET)
    key_minute = key.hour * 60 + key.minute

    with open(os.path.join(repo, args.baskets)) as f:
        cfg = json.load(f)
    baskets = {n: sorted(set(b["members"])) for n, b in cfg["baskets"].items()}
    union = sorted({m for ms in baskets.values() for m in ms})

    flat = os.path.join(repo, "data", "buckets", args.date + ".trades-only.csv")
    partial = os.path.join(repo, "data", "buckets", args.date + ".partial.csv")
    if args.source == "capture":
        dollars = load_dollars(partial, 0, end_sec)
    else:
        # Flat file for everything except the mid-second capture-join
        # boundary, which comes from the capture-derived file.
        dollars = load_dollars(flat, CAPTURE_START_SEC + 1, end_sec)
        for sym, v in load_dollars(partial, CAPTURE_START_SEC,
                                   CAPTURE_START_SEC + 1).items():
            dollars[sym] = dollars.get(sym, 0.0) + v
    uni = sum(dollars.get(m, 0.0) for m in union)

    def baseline(name):
        rows = skip_comments(os.path.join(repo, args.profiles, "baskets", name + ".csv"))
        for r in csv.DictReader(rows):
            if int(r["minute_of_day"]) == key_minute:
                return (float(r["median_cum_share"]), float(r["sigma_cum_share"]),
                        int(r["cum_days"]))
        raise SystemExit("minute %d not in %s profile" % (key_minute, name))

    floor = None
    for r in csv.DictReader(skip_comments(os.path.join(repo, args.profiles, "_floors.csv"))):
        if int(r["minute_of_day"]) == key_minute:
            floor = float(r["sigma_floor_cumshare"])
    if floor is None:
        raise SystemExit("floor minute %d missing" % key_minute)

    # View side: rendered rows, ANSI stripped.
    ansi = re.compile("\x1b\\[[0-9;]*m")
    view = {}
    with open(args.view_stdout) as f:
        for line in f:
            p = ansi.sub("", line).split()
            if p and p[0] in baskets:
                view[p[0]] = (p[1], p[2], p[3])  # cum_share, typ, z
    missing = sorted(set(baskets) - set(view))
    if missing:
        raise SystemExit("baskets missing from view stdout: %s" % missing)

    fails = 0
    print("source=%s  window=[%s, %s) ET  key minute=%d  uni $=%.0f  floor=%.6g"
          % (args.source, datetime.fromtimestamp(
                 CAPTURE_START_SEC if args.source == "flat" else CAPTURE_START_SEC, ET).time(),
             datetime.fromtimestamp(end_sec, ET).time(), key_minute, uni, floor))
    print("%-26s %8s %8s %8s %8s %6s %6s" % ("basket", "py cum", "view",
                                             "py typ", "view", "py z", "view"))
    for name in sorted(baskets):
        share = sum(dollars.get(m, 0.0) for m in baskets[name]) / uni
        med, sig, days = baseline(name)
        z = (share - med) / max(sig, floor)
        py = ("%.2f%%" % (100 * share), "%.2f%%" % (100 * med), "%+.1f" % z)
        v = view[name]
        ok = v == py
        fails += 0 if ok else 1
        print("%-26s %8s %8s %8s %8s %6s %6s%s"
              % (name, py[0], v[0], py[1], v[1], py[2], v[2],
                 "" if ok else "   <-- MISMATCH"))
    print("RESULT:", "ALL %d MATCH TO PRINTED DIGIT" % len(baskets) if fails == 0
          else "%d last-digit mismatches (expected 0 for --source=capture; "
               "up to a few for --source=flat — see docstring)" % fails)
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
