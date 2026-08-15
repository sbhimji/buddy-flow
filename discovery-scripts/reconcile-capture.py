# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "boto3",
# ]
# ///
"""Mini-spec 1.2 criterion 4: reconcile a live capture against the vendor's
flat files, windowed to the capture's span. Downloads the flat files if
absent, counts per-symbol trades/quotes on both sides by SIP timestamp over
an interior window, and reports the delta. The delta is REPORTED, not gated:
exit 0 regardless of mismatch; nonzero only on operational failure.

The window is interior to the capture span on purpose: the capture holds
what was RECEIVED in its span, the flat file is windowed by SIP timestamp —
a late print straddling a boundary would otherwise create a phantom delta.

Run:  uv run discovery-scripts/reconcile-capture.py
"""
import csv
import gzip
import io
import json
import os
import sys
import time
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

DATE = "2026-08-14"
WIN_FROM_ET = "12:00:00"  # capture began 11:51 ET; window sits inside it
WIN_TO_ET = "16:00:00"

REPO = Path(__file__).resolve().parent.parent
BASKETS = REPO / "docs/foundations/morning-tape-baskets-v2.json"
CAPTURE = REPO / f"data/capture/{DATE}/stream.jsonl"
YEAR, MONTH = DATE.split("-")[0], DATE.split("-")[1]
FILES = {  # kind -> (local path, S3 object key)  [same bucket/endpoint as data/download.py]
    "trades": (REPO / f"data/flat-files/trades/{DATE}.csv.gz",
               f"us_stocks_sip/trades_v1/{YEAR}/{MONTH}/{DATE}.csv.gz"),
    "quotes": (REPO / f"data/flat-files/quotes/{DATE}.csv.gz",
               f"us_stocks_sip/quotes_v1/{YEAR}/{MONTH}/{DATE}.csv.gz"),
}


def log(msg):
    print(msg, file=sys.stderr, flush=True)


def env_var(name):
    """Environment first, then .env at the repo root — same contract as the
    Go apiKey() (KEY=VALUE lines, quotes stripped)."""
    if v := os.environ.get(name):
        return v
    env_file = REPO / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            k, _, v = line.strip().partition("=")
            if k == name and v:
                return v.strip().strip("'\"")
    sys.exit(f"{name} not set (environment or .env at repo root)")


def universe():
    """Replicates internal/universe.Load: benchmarks (minus the non-equity
    bond_gate_futures group) + all basket members, de-duplicated."""
    cfg = json.loads(BASKETS.read_text())
    syms = set()
    for group, members in cfg["benchmarks"].items():
        if group == "bond_gate_futures":
            continue
        syms.update(members)
    for basket in cfg["baskets"].values():
        syms.update(basket["members"])
    return syms


def window_ns():
    tz = ZoneInfo("America/New_York")
    lo = datetime.strptime(f"{DATE} {WIN_FROM_ET}", "%Y-%m-%d %H:%M:%S").replace(tzinfo=tz)
    hi = datetime.strptime(f"{DATE} {WIN_TO_ET}", "%Y-%m-%d %H:%M:%S").replace(tzinfo=tz)
    return int(lo.timestamp()) * 10**9, int(hi.timestamp()) * 10**9


def ensure_downloaded():
    """Download both flat files unless present with the right size."""
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError

    session = boto3.Session(  # credentials as in data/download.py
        aws_access_key_id=env_var("AWS_ACCESS_KEY_ID"),
        aws_secret_access_key=env_var("AWS_SECRET_ACCESS_KEY"),
    )
    s3 = session.client("s3", endpoint_url="https://files.massive.com",
                        config=Config(signature_version="s3v4"))
    for kind, (local, key) in FILES.items():
        try:
            total = s3.head_object(Bucket="flatfiles", Key=key)["ContentLength"]
        except ClientError as e:
            code = e.response.get("Error", {}).get("Code", "")
            if code in ("404", "NoSuchKey", "NotFound"):
                log(f"FATAL: {kind} flat file for {DATE} not published yet (key {key})")
                sys.exit(1)
            raise
        if local.exists() and local.stat().st_size == total:
            log(f"{kind}: already downloaded ({total/1e9:.2f} GB), skipping")
            continue
        local.parent.mkdir(parents=True, exist_ok=True)
        log(f"{kind}: downloading {total/1e9:.2f} GB -> {local}")
        state = {"done": 0, "t0": time.monotonic(), "last": 0.0}

        def progress(chunk, state=state, total=total, kind=kind):
            state["done"] += chunk
            now = time.monotonic()
            if now - state["last"] >= 5:
                state["last"] = now
                pct = state["done"] / total * 100
                mbs = state["done"] / 1e6 / (now - state["t0"])
                log(f"  {kind}: {pct:5.1f}%  {mbs:6.1f} MB/s")

        s3.download_file("flatfiles", key, str(local), Callback=progress)
        log(f"{kind}: download done")


def scan_capture(syms, lo, hi):
    """Per-symbol T/Q counts from stream.jsonl, SIP ts (ms on the wire) in
    [lo, hi) ns. Unparseable lines (kill-drill torn lines) are skipped."""
    trades, quotes = {}, {}
    frames = skipped = outside_universe = 0
    with open(CAPTURE, "rb") as f:
        for line in f:
            sp = line.find(b" ")
            if sp < 0:
                skipped += 1
                continue
            try:
                events = json.loads(line[sp + 1:])
            except json.JSONDecodeError:
                skipped += 1
                continue
            frames += 1
            if frames % 1_000_000 == 0:
                log(f"  capture: {frames/1e6:.0f}M frames")
            if not isinstance(events, list):
                continue
            for ev in events:
                kind = ev.get("ev")
                if kind not in ("T", "Q"):
                    continue
                sym = ev.get("sym")
                if sym not in syms:
                    outside_universe += 1
                    continue
                ns = ev.get("t", 0) * 1_000_000
                if lo <= ns < hi:
                    d = trades if kind == "T" else quotes
                    d[sym] = d.get(sym, 0) + 1
    log(f"  capture: {frames} frames, {skipped} unparseable lines skipped, "
        f"{outside_universe} events outside universe")
    return trades, quotes


def scan_flatfile(path, syms, lo, hi):
    """Per-symbol counts from a whole-market flat file, universe rows only,
    sip_timestamp (ns) in [lo, hi).

    Rows are parsed without the csv module for speed: ticker is column 0 and
    never quoted, so a partition() gets it; sip_timestamp sits AFTER every
    quotable column (conditions/indicators), so an rsplit anchored on the
    header's column count reads it safely on member rows only.
    """
    counts = {}
    bsyms = {s.encode() for s in syms}
    with gzip.open(path, "rb") as gz:
        f = io.BufferedReader(gz, 1 << 22)
        header = next(csv.reader([f.readline().decode()]))
        ts_idx = header.index("sip_timestamp")
        if header.index("ticker") != 0:
            raise SystemExit(f"{path}: ticker is not column 0; script assumptions broken")
        for name in header[ts_idx:]:
            if name in ("conditions", "indicators"):
                raise SystemExit(f"{path}: quotable column {name!r} at/after sip_timestamp; "
                                 "rsplit shortcut unsafe")
        n_right = len(header) - ts_idx  # sip_timestamp is n_right-th field from the end
        rows = 0
        for line in f:
            rows += 1
            if rows % 100_000_000 == 0:
                log(f"  {path.name}: {rows/1e6:.0f}M rows")
            ticker = line.partition(b",")[0]
            if ticker not in bsyms:
                continue
            ns = int(line.rsplit(b",", n_right)[1])
            if lo <= ns < hi:
                sym = ticker.decode()
                counts[sym] = counts.get(sym, 0) + 1
    log(f"  {path.name}: {rows} rows scanned")
    return counts


def report(side, cap, flat, syms):
    ct, ft = sum(cap.values()), sum(flat.values())
    delta = ft - ct
    pct = (delta / ft * 100) if ft else 0.0
    print(f"\n== {side} ==")
    print(f"capture total = {ct:,}   flat-file total = {ft:,}   "
          f"delta (flat - capture) = {delta:+,} ({pct:+.4f}%)")
    diff = [(s, cap.get(s, 0), flat.get(s, 0)) for s in sorted(syms)
            if cap.get(s, 0) != flat.get(s, 0)]
    exact = len(syms) - len(diff)
    print(f"{exact}/{len(syms)} symbols exact")
    if diff:
        print(f"{'symbol':<8} {'capture':>12} {'flat':>12} {'delta':>10}")
        for s, c, fl in diff:
            print(f"{s:<8} {c:>12,} {fl:>12,} {fl - c:>+10,}")


def main():
    syms = universe()
    lo, hi = window_ns()
    print(f"universe: {len(syms)} symbols")
    print(f"window: {DATE} [{WIN_FROM_ET}, {WIN_TO_ET}) ET = [{lo}, {hi}) ns")
    if not CAPTURE.exists():
        log(f"FATAL: capture missing: {CAPTURE}")
        sys.exit(1)

    ensure_downloaded()

    log("scanning capture...")
    cap_t, cap_q = scan_capture(syms, lo, hi)
    log("scanning trades flat file...")
    flat_t = scan_flatfile(FILES["trades"][0], syms, lo, hi)
    log("scanning quotes flat file...")
    flat_q = scan_flatfile(FILES["quotes"][0], syms, lo, hi)

    report("trades", cap_t, flat_t, syms)
    report("quotes", cap_q, flat_q, syms)
    print("\ndelta is reported, not gated (mini-spec 1.2 criterion 4); exit 0")


if __name__ == "__main__":
    main()
