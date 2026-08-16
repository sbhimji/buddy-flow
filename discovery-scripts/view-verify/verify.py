#!/usr/bin/env python3
"""Independent Python verification of the 3.0 dev view numbers.

Spec: discovery-scripts/view-verify/SPEC.md. Compares the Go dev view's
completed-minute columns ($last / base / rvol), rendered from the capture
via `go run ./cmd/replay -view -view-at T`, against a stdlib-Python
recomputation from the flat-file bucket CSV and the profile CSVs.

Verification script, not product code. Stdlib only. Read-only w.r.t.
data/ and Go code; writes only under discovery-scripts/view-verify/.
"""

import argparse
import csv
import json
import os
import re
import subprocess
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

ET = ZoneInfo("America/New_York")
GAP = "·"  # "·" — the view's gap glyph

DEFAULT_MINUTES = "12:00:00,12:31:00,13:01:00,14:00:00,15:30:00,15:59:00"

# --- formatter port (internal/devview/devview.go fmtDollars) -----------------


def fmt_dollars(v):
    """Exact port of devview.fmtDollars. Python's % / format() formatting of
    a float matches Go's fmt %.Nf (both round the binary double correctly,
    half-to-even at the representation level)."""

    def scaled(x, unit):
        if x < 10:
            return f"{x:.2f}{unit}"
        if x < 100:
            return f"{x:.1f}{unit}"
        return f"{x:.0f}{unit}"

    if v >= 1e9:
        return scaled(v / 1e9, "B")
    if v >= 1e6:
        return scaled(v / 1e6, "M")
    if v >= 1e3:
        return scaled(v / 1e3, "k")
    return f"{v:.0f}"


_UNIT = {"k": 1e3, "M": 1e6, "B": 1e9}


def parse_dollars(s):
    """Parse a fmtDollars string back to a float (3-sig-fig precision)."""
    if s == GAP:
        return None
    if s and s[-1] in _UNIT:
        return float(s[:-1]) * _UNIT[s[-1]]
    return float(s)


# --- view side ---------------------------------------------------------------


def view_cache_path(outdir, hms):
    return os.path.join(outdir, "view-%s.stdout.txt" % hms.replace(":", ""))


def run_view(repo_root, capture, hms, outdir, refresh=False):
    """Run (or reuse cached stdout of) the Go replay render for one second."""
    path = view_cache_path(outdir, hms)
    if refresh or not (os.path.exists(path) and os.path.getsize(path) > 0):
        cmd = [
            "go", "run", "./cmd/replay",
            "-capture", capture, "-view", "-view-at", hms,
        ]
        sys.stderr.write("running: %s\n" % " ".join(cmd))
        res = subprocess.run(
            cmd, cwd=repo_root, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, check=True, text=True)
        with open(path, "w") as f:
            f.write(res.stdout)
    with open(path) as f:
        return f.read()


def parse_view_table(stdout_text, hms, date):
    """Return {basket: (n, last, base, rvol, now)} from the LAST table in
    stdout whose clock line starts with the requested render second."""
    lines = stdout_text.splitlines()
    clock_re = re.compile(r"^%s ET  %s\b" % (re.escape(hms), re.escape(date)))
    start = None
    for i, line in enumerate(lines):
        if clock_re.match(line):
            start = i
    if start is None:
        raise SystemExit(
            "no table for render second %s %s in view stdout" % (date, hms))
    if start + 1 >= len(lines) or not lines[start + 1].startswith("BASKET"):
        raise SystemExit("table at %s: expected BASKET header line" % hms)
    rows = {}
    for line in lines[start + 2:]:
        parts = line.split()
        if len(parts) != 6:
            break  # end of table (basket names contain no spaces)
        name, n, last, base, rvol, now = parts
        rows[name] = {"n": int(n), "last": last, "base": base,
                      "rvol": rvol, "now": now}
    return rows


# --- python side -------------------------------------------------------------


def load_baskets(path):
    """{basket_name: sorted de-duplicated member list}. Benchmarks excluded."""
    with open(path) as f:
        cfg = json.load(f)
    return {name: sorted(set(b["members"]))
            for name, b in cfg["baskets"].items()}


def minute_windows(date, render_seconds):
    """For each render second, the $last minute: (epoch_start, minute_of_day).

    The view's $last minute is the minute before the render second's minute
    (session.MinuteStart(at) - 60). All arithmetic through zoneinfo ET."""
    out = {}
    for hms in render_seconds:
        h, m, s = (int(x) for x in hms.split(":"))
        dt = datetime(*[int(x) for x in date.split("-")], h, m, s, tzinfo=ET)
        at = int(dt.timestamp())
        prev_start = (at // 60) * 60 - 60
        prev_dt = datetime.fromtimestamp(prev_start, ET)
        out[hms] = (prev_start, prev_dt.hour * 60 + prev_dt.minute)
    return out


def sum_bucket_dollars(bucket_csv, symbols, windows):
    """One streaming pass over the bucket CSV.

    Returns {hms: {symbol: counted_dollars}} where counted =
    continuous + block + non_price_forming dollars over
    [start, start+60) for that render second's $last minute."""
    sec_to_hms = {}
    for hms, (start, _) in windows.items():
        for sec in range(start, start + 60):
            sec_to_hms[sec] = hms
    sums = {hms: {} for hms in windows}
    with open(bucket_csv, newline="") as f:
        for row in csv.DictReader(f):
            hms = sec_to_hms.get(int(row["second"]))
            if hms is None:
                continue
            sym = row["symbol"]
            if sym not in symbols:
                continue
            d = (float(row["continuous_dollars"])
                 + float(row["block_dollars"])
                 + float(row["non_price_forming_dollars"]))
            sums[hms][sym] = sums[hms].get(sym, 0.0) + d
    return sums


def load_profiles(prof_dir, symbols):
    """{symbol: {minute_of_day: median_dollars}}."""
    profiles = {}
    for sym in symbols:
        med = {}
        with open(os.path.join(prof_dir, sym + ".csv"), newline="") as f:
            for row in csv.DictReader(f):
                med[int(row["minute_of_day"])] = float(row["median_dollars"])
        profiles[sym] = med
    return profiles


# --- comparison / report -----------------------------------------------------


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--date", default="2026-08-14")
    ap.add_argument("--capture", default="data/capture/2026-08-14/stream.jsonl")
    ap.add_argument("--minutes", default=DEFAULT_MINUTES,
                    help="comma-separated render seconds, ET HH:MM:SS")
    ap.add_argument("--buckets", default=None,
                    help="bucket CSV (default data/buckets/<date>.trades-only.csv)")
    ap.add_argument("--profiles", default="data/profiles")
    ap.add_argument("--baskets",
                    default="docs/foundations/morning-tape-baskets-v2.json")
    ap.add_argument("--refresh-views", action="store_true",
                    help="re-run the Go replays even if cached stdout exists")
    args = ap.parse_args()

    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.abspath(os.path.join(script_dir, "..", ".."))
    outdir = os.path.join(script_dir, "output")
    os.makedirs(outdir, exist_ok=True)

    def rp(p):  # repo-relative -> absolute
        return p if os.path.isabs(p) else os.path.join(repo_root, p)

    bucket_csv = rp(args.buckets or
                    os.path.join("data", "buckets",
                                 args.date + ".trades-only.csv"))
    render_seconds = [s.strip() for s in args.minutes.split(",") if s.strip()]

    baskets = load_baskets(rp(args.baskets))
    all_syms = sorted({m for ms in baskets.values() for m in ms})
    windows = minute_windows(args.date, render_seconds)

    # View side (cached; runs sequentially when cache is cold).
    view = {}
    for hms in render_seconds:
        text = run_view(repo_root, args.capture, hms, outdir,
                        refresh=args.refresh_views)
        view[hms] = parse_view_table(text, hms, args.date)

    # Python side.
    bucket_sums = sum_bucket_dollars(bucket_csv, set(all_syms), windows)
    profiles = load_profiles(rp(args.profiles), all_syms)

    results = []  # one dict per basket x minute
    for hms in render_seconds:
        start, mod = windows[hms]
        vrows = view[hms]
        missing = sorted(set(baskets) - set(vrows))
        extra = sorted(set(vrows) - set(baskets))
        if missing or extra:
            raise SystemExit("basket set mismatch at %s: view missing %s, "
                             "view extra %s" % (hms, missing, extra))
        for name in sorted(baskets):
            members = baskets[name]
            py_last = sum(bucket_sums[hms].get(m, 0.0) for m in members)
            base_ok = all(mod in profiles[m] for m in members)
            py_base = (sum(profiles[m][mod] for m in members)
                       if base_ok else None)
            py_base_str = fmt_dollars(py_base) if base_ok else GAP
            if base_ok and py_base != 0:
                py_rvol = py_last / py_base
                py_rvol_str = f"{py_rvol:.2f}"
            else:
                py_rvol, py_rvol_str = None, GAP

            v = vrows[name]
            v_last_num = parse_dollars(v["last"])

            # base: exact string match (gap must agree too).
            base_pass = v["base"] == py_base_str

            # rvol: both gaps agree, else |view - python| <= 0.01.
            if v["rvol"] == GAP or py_rvol_str == GAP:
                rvol_pass = v["rvol"] == py_rvol_str
                rvol_diff = None
            else:
                rvol_diff = float(v["rvol"]) - py_rvol
                rvol_pass = abs(rvol_diff) <= 0.01 + 1e-12

            # $last: relative delta of view's parsed value vs raw python
            # value <= 0.5%; string equality tracked separately.
            last_str_eq = v["last"] == fmt_dollars(py_last)
            if py_last == 0:
                rel = 0.0 if (v_last_num or 0.0) == 0 else float("inf")
            else:
                rel = (v_last_num - py_last) / py_last
            last_pass = abs(rel) <= 0.005

            results.append({
                "hms": hms, "minute_start": start, "mod": mod,
                "basket": name, "n": v["n"], "members": len(members),
                "v_last": v["last"], "v_base": v["base"], "v_rvol": v["rvol"],
                "py_last": py_last, "py_last_str": fmt_dollars(py_last),
                "py_base_str": py_base_str, "py_rvol_str": py_rvol_str,
                "py_rvol": py_rvol, "rel": rel, "rvol_diff": rvol_diff,
                "base_pass": base_pass, "rvol_pass": rvol_pass,
                "last_pass": last_pass, "last_str_eq": last_str_eq,
            })

    # ---- report ----
    total = len(results)
    base_fail = [r for r in results if not r["base_pass"]]
    rvol_fail = [r for r in results if not r["rvol_pass"]]
    last_fail = [r for r in results if not r["last_pass"]]
    str_eq = sum(1 for r in results if r["last_str_eq"])
    finite = [r for r in results if r["rel"] != float("inf")]
    worst = max(finite, key=lambda r: abs(r["rel"]))
    mean_rel = sum(r["rel"] for r in finite) / len(finite)
    fails = base_fail + rvol_fail + last_fail
    all_pass = not fails

    def pf(ok):
        return "PASS" if ok else "FAIL"

    lines = []
    lines.append("# View-verify report — %s" % args.date)
    lines.append("")
    lines.append("View: `go run ./cmd/replay -capture %s -view -view-at T` "
                 "(capture-fed Go path)." % args.capture)
    lines.append("Python: `%s` + `%s/<SYM>.csv` (flat-file-fed stdlib path)."
                 % (os.path.relpath(bucket_csv, repo_root), args.profiles))
    lines.append("Baskets: `%s` (%d baskets, %d symbols)."
                 % (args.baskets, len(baskets), len(all_syms)))
    lines.append("")
    lines.append("Criteria: base exact string; |rvol view-py| <= 0.01; "
                 "$last relative delta <= 0.5% (view string parsed back to a "
                 "number vs raw python sum); gaps must agree.")
    for hms in render_seconds:
        start, mod = windows[hms]
        last_min = datetime.fromtimestamp(start, ET).strftime("%H:%M")
        lines.append("")
        lines.append("## Render %s ET — $last minute %s (minute_of_day %d)"
                     % (hms, last_min, mod))
        lines.append("")
        lines.append("| basket | view $last | view base | view rvol | "
                     "py $last | py base | py rvol | $last rel delta | "
                     "$last | base | rvol |")
        lines.append("|---|---|---|---|---|---|---|---|---|---|---|")
        for r in results:
            if r["hms"] != hms:
                continue
            lines.append(
                "| %s | %s | %s | %s | %s | %s | %s | %+.4f%% | %s | %s | %s |"
                % (r["basket"], r["v_last"], r["v_base"], r["v_rvol"],
                   r["py_last_str"], r["py_base_str"], r["py_rvol_str"],
                   r["rel"] * 100, pf(r["last_pass"]), pf(r["base_pass"]),
                   pf(r["rvol_pass"])))

    summary = []
    summary.append("== Summary ==")
    summary.append("checks: %d basket-minutes (%d baskets x %d minutes)"
                   % (total, len(baskets), len(render_seconds)))
    summary.append("base  (exact string): %d/%d pass"
                   % (total - len(base_fail), total))
    summary.append("rvol  (<= 0.01):      %d/%d pass"
                   % (total - len(rvol_fail), total))
    summary.append("$last (<= 0.5%%):      %d/%d pass"
                   % (total - len(last_fail), total))
    summary.append("$last string equality (informational): %d/%d (%.1f%%)"
                   % (str_eq, total, 100.0 * str_eq / total))
    summary.append("worst $last relative delta: %+.4f%% (%s @ %s)"
                   % (worst["rel"] * 100, worst["basket"], worst["hms"]))
    summary.append("mean signed $last relative delta: %+.4f%%"
                   % (mean_rel * 100))
    if all_pass:
        summary.append("RESULT: all criteria pass")
    else:
        summary.append("RESULT: %d FAILING checks:" % len(fails))
        for r in fails:
            summary.append(
                "  FAIL %s @ %s: view $last=%s base=%s rvol=%s | "
                "py $last=%.6f (%s) base=%s rvol=%s | rel=%+.4f%%"
                % (r["basket"], r["hms"], r["v_last"], r["v_base"],
                   r["v_rvol"], r["py_last"], r["py_last_str"],
                   r["py_base_str"], r["py_rvol_str"], r["rel"] * 100))

    lines.append("")
    lines.append("## Summary")
    lines.append("")
    lines.append("```")
    lines.extend(summary)
    lines.append("```")
    lines.append("")

    report_path = os.path.join(outdir, "report.md")
    with open(report_path, "w") as f:
        f.write("\n".join(lines))

    print("\n".join(summary))
    print("report: %s" % report_path)
    sys.exit(0 if all_pass else 1)


if __name__ == "__main__":
    main()
