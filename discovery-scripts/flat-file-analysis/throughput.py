# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "pandas",
# ]
# ///
"""Throughput analysis for story 1.1 (see docs/temp/phase-1-handoff.md).

Streams the full-session trades and quotes flat files, filters to the
189-symbol universe, and buckets message counts per 1-second. All prints and
quotes count — throughput is an ingest-load measurement, so no print-inclusion
policy is applied.

Outputs (in output/):
  per-second-<date>.csv   et_time, trades, quotes, total for every session second
  per-symbol-<date>.csv   session + 09:30-10:30 counts and Q:T ratio per symbol
  summary-<date>.md       the numbers the 1.1 mini-spec needs

Usage:
  uv run throughput.py                  # full run (trades ~minutes, quotes longer)
  uv run throughput.py --limit 5000000  # smoke test on first N rows of each file
"""

import argparse
import json
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

import pandas as pd

REPO = Path(__file__).resolve().parents[2]
BASKETS_JSON = REPO / "docs/foundations/morning-tape-baskets-v2.json"
DATE = "2026-08-11"
TRADES_GZ = REPO / f"data/flat-files/trades/{DATE}.csv.gz"
QUOTES_GZ = REPO / f"data/flat-files/quotes/{DATE}.csv.gz"
OUT_DIR = Path(__file__).resolve().parent / "output"

ET = ZoneInfo("America/New_York")
CHUNK_ROWS = 2_000_000

# Session bounds 04:00-20:00 ET for the file's date, as epoch seconds UTC.
SESSION_START = int(datetime.fromisoformat(f"{DATE}T04:00:00").replace(tzinfo=ET).timestamp())
SESSION_END = int(datetime.fromisoformat(f"{DATE}T20:00:00").replace(tzinfo=ET).timestamp())
N_SECONDS = SESSION_END - SESSION_START

OPEN_0930 = int(datetime.fromisoformat(f"{DATE}T09:30:00").replace(tzinfo=ET).timestamp())
H_1030 = OPEN_0930 + 3600


def load_universe() -> set[str]:
    d = json.loads(BASKETS_JSON.read_text())
    syms: set[str] = set()
    for grp, lst in d["benchmarks"].items():
        if grp == "bond_gate_futures":  # ZN/ZB backlogged, not equities
            continue
        syms.update(lst)
    for bk in d["baskets"].values():
        syms.update(bk["members"])
    return syms


def et_str(epoch_s: int) -> str:
    return datetime.fromtimestamp(epoch_s, ET).strftime("%H:%M:%S")


def stream_counts(path: Path, universe: set[str], label: str, limit: int | None):
    """One pass over a flat file. Returns (per_second_counts, per_symbol_session,
    per_symbol_open_hour, total_rows_read, universe_rows)."""
    per_sec = [0] * N_SECONDS
    per_sym: dict[str, int] = {}
    per_sym_open: dict[str, int] = {}
    rows_read = 0
    uni_rows = 0
    t0 = time.monotonic()

    reader = pd.read_csv(
        path,
        usecols=["ticker", "sip_timestamp"],
        dtype={"ticker": "string", "sip_timestamp": "int64"},
        chunksize=CHUNK_ROWS,
    )
    for chunk in reader:
        rows_read += len(chunk)
        mask = chunk["ticker"].isin(universe)
        hit = chunk[mask]
        uni_rows += len(hit)
        secs = (hit["sip_timestamp"] // 1_000_000_000).astype("int64")
        idx = secs - SESSION_START
        in_session = (idx >= 0) & (idx < N_SECONDS)
        for i, n in idx[in_session].value_counts().items():
            per_sec[i] += int(n)
        for sym, n in hit["ticker"].value_counts().items():
            per_sym[sym] = per_sym.get(sym, 0) + int(n)
        open_hit = hit[(secs >= OPEN_0930) & (secs < H_1030)]
        for sym, n in open_hit["ticker"].value_counts().items():
            per_sym_open[sym] = per_sym_open.get(sym, 0) + int(n)
        elapsed = time.monotonic() - t0
        print(
            f"\r{label}: {rows_read:,} rows read, {uni_rows:,} universe,"
            f" {elapsed:.0f}s ({rows_read / elapsed / 1e6:.1f}M rows/s)",
            end="",
            flush=True,
        )
        if limit and rows_read >= limit:
            break
    print()
    return per_sec, per_sym, per_sym_open, rows_read, uni_rows


def rolling_peak(per_sec: list[int], window: int) -> tuple[float, int]:
    """Max average msg/sec over any `window`-second span. Returns (rate, start_epoch)."""
    best, best_i = -1, 0
    s = sum(per_sec[:window])
    best, best_i = s, 0
    for i in range(window, len(per_sec)):
        s += per_sec[i] - per_sec[i - window]
        if s > best:
            best, best_i = s, i - window + 1
    return best / window, SESSION_START + best_i


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--limit", type=int, default=None, help="stop after N rows per file (smoke test)")
    args = ap.parse_args()

    universe = load_universe()
    print(f"Universe: {len(universe)} symbols")
    OUT_DIR.mkdir(exist_ok=True)

    t_sec, t_sym, t_sym_open, t_rows, t_uni = stream_counts(TRADES_GZ, universe, "trades", args.limit)
    q_sec, q_sym, q_sym_open, q_rows, q_uni = stream_counts(QUOTES_GZ, universe, "quotes", args.limit)

    combined = [t + q for t, q in zip(t_sec, q_sec)]

    # --- per-second CSV ---
    ps_path = OUT_DIR / f"per-second-{DATE}.csv"
    with ps_path.open("w") as f:
        f.write("et_time,trades,quotes,total\n")
        for i in range(N_SECONDS):
            f.write(f"{et_str(SESSION_START + i)},{t_sec[i]},{q_sec[i]},{combined[i]}\n")

    # --- per-symbol CSV ---
    sym_path = OUT_DIR / f"per-symbol-{DATE}.csv"
    # secondary key: symbol — ties (esp. zero-count symbols) would otherwise
    # order by set iteration, breaking run-to-run reproducibility
    all_syms = sorted(universe, key=lambda s: (-(t_sym.get(s, 0) + q_sym.get(s, 0)), s))
    with sym_path.open("w") as f:
        f.write("symbol,trades_session,quotes_session,trades_0930_1030,quotes_0930_1030,qt_ratio_session,qt_ratio_0930_1030\n")
        for s in all_syms:
            ts, qs = t_sym.get(s, 0), q_sym.get(s, 0)
            to, qo = t_sym_open.get(s, 0), q_sym_open.get(s, 0)
            rs = f"{qs / ts:.2f}" if ts else ""
            ro = f"{qo / to:.2f}" if to else ""
            f.write(f"{s},{ts},{qs},{to},{qo},{rs},{ro}\n")

    # --- summary ---
    def peaks(series, name):
        p1 = max(series)
        p1_at = SESSION_START + series.index(p1)
        p10, p10_at = rolling_peak(series, 10)
        p60, p60_at = rolling_peak(series, 60)
        open_i, close_i = OPEN_0930 - SESSION_START, H_1030 - SESSION_START
        sustained = sum(series[open_i:close_i]) / 3600
        return (
            f"### {name}\n\n"
            f"| stat | msg/sec | at (ET) |\n|---|---|---|\n"
            f"| peak 1s | {p1:,} | {et_str(p1_at)} |\n"
            f"| peak 10s | {p10:,.0f} | {et_str(p10_at)} |\n"
            f"| peak 60s | {p60:,.0f} | {et_str(p60_at)} |\n"
            f"| 09:30–10:30 sustained avg | {sustained:,.0f} | — |\n"
        )

    hist_start = OPEN_0930 - 5 - SESSION_START  # 09:29:55
    hist_end = OPEN_0930 + 300 - SESSION_START  # 09:35:00
    hist_lines = ["| ET | trades | quotes | total |", "|---|---|---|---|"]
    for i in range(hist_start, hist_end):
        hist_lines.append(
            f"| {et_str(SESSION_START + i)} | {t_sec[i]:,} | {q_sec[i]:,} | {combined[i]:,} |"
        )

    ratio_lines = ["| symbol | T 09:30–10:30 | Q 09:30–10:30 | Q:T |", "|---|---|---|---|"]
    for s in all_syms[:10] + all_syms[-10:]:
        to, qo = t_sym_open.get(s, 0), q_sym_open.get(s, 0)
        ratio_lines.append(f"| {s} | {to:,} | {qo:,} | {qo / to:.2f} |" if to else f"| {s} | 0 | {qo:,} | — |")

    limit_note = f"\n**⚠ SMOKE TEST — first {args.limit:,} rows per file only. Numbers not valid.**\n" if args.limit else ""
    summary = f"""# Throughput analysis — {DATE} session
{limit_note}
Feeds the story 1.1 mini-spec (floor was an estimated ≥50k msg/sec).
All universe prints/quotes counted; no print-inclusion policy applied
(throughput measures ingest load, not classification).

- Universe: {len(universe)} symbols (members + benchmarks, ZN/ZB skipped)
- Trades file: {t_rows:,} rows read, {t_uni:,} universe ({t_uni / t_rows:.1%})
- Quotes file: {q_rows:,} rows read, {q_uni:,} universe ({q_uni / q_rows:.1%})

{peaks(t_sec, "Trades")}
{peaks(q_sec, "Quotes")}
{peaks(combined, "Combined (trades + quotes) — the number vs the 50k floor")}
## Q:T ratios, 09:30–10:30 (top 10 / bottom 10 by session message count)

{chr(10).join(ratio_lines)}

## Open histogram 09:29:55–09:35:00 (per 1-second bucket)

{chr(10).join(hist_lines)}
"""
    sum_path = OUT_DIR / f"summary-{DATE}.md"
    sum_path.write_text(summary)
    print(f"\nWrote {ps_path}\nWrote {sym_path}\nWrote {sum_path}\n")
    # Echo the headline numbers to stdout
    for name, series in (("trades", t_sec), ("quotes", q_sec), ("combined", combined)):
        p10, _ = rolling_peak(series, 10)
        open_i, close_i = OPEN_0930 - SESSION_START, H_1030 - SESSION_START
        print(f"{name:9s} peak1s={max(series):,} peak10s={p10:,.0f}/s open-hour avg={sum(series[open_i:close_i]) / 3600:,.0f}/s")


if __name__ == "__main__":
    main()
