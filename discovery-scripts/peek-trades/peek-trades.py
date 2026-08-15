#!/usr/bin/env python3
"""Print a few raw trade events from a capture stream.jsonl.

Each capture line is `<recv_ns> <raw websocket frame>`; a frame is a JSON
array of events. This prints trade ("T") events exactly as they came off the
wire — the input end of the pipeline trace.

Usage:
  python3 discovery-scripts/peek-trades.py data/capture/2026-08-13/stream.jsonl
  python3 discovery-scripts/peek-trades.py data/capture/2026-08-13/stream.jsonl -n 5 -sym NVDA
  python3 discovery-scripts/peek-trades.py data/capture/2026-08-13/stream.jsonl -with-conditions
"""
import argparse
import json

ap = argparse.ArgumentParser()
ap.add_argument("capture", help="path to stream.jsonl")
ap.add_argument("-n", type=int, default=3, help="how many trades to print (default 3)")
ap.add_argument("-sym", help="only this symbol (default: any)")
ap.add_argument("-with-conditions", action="store_true",
                help="only trades carrying condition codes (c non-empty)")
args = ap.parse_args()

printed = 0
with open(args.capture) as f:
    for line in f:
        sp = line.find(" ")
        if sp < 0:
            continue  # torn line (kill drill) — reader skips, so do we
        recv_ns = line[:sp]
        try:
            events = json.loads(line[sp + 1:])
        except json.JSONDecodeError:
            continue
        if not isinstance(events, list):
            continue
        for ev in events:
            if ev.get("ev") != "T":
                continue
            if args.sym and ev.get("sym") != args.sym:
                continue
            if getattr(args, "with_conditions") and not ev.get("c"):
                continue
            print(f"--- trade {printed + 1}  (received {recv_ns}, SIP t={ev.get('t')} ms)")
            print(json.dumps(ev, indent=2))
            printed += 1
            if printed >= args.n:
                raise SystemExit(0)
print(f"only found {printed} matching trades")
