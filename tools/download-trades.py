# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "boto3",
# ]
# ///
"""Download the trailing N trading days of trades flat files (2.1 bootstrap).

Walks weekdays backward from --through, keeps dates whose file exists at the
vendor (a missing object = market holiday), downloads to
data/flat-files/trades/<date>.csv.gz, skipping files already present with the
right size. Quotes files are deliberately not fetched — profiles are
volume-only (mini-spec 2.1 D1).

  uv run tools/download-trades.py --through 2026-08-13 --count 20
"""
import argparse
import datetime as dt
import os
import sys
import threading
import time
from pathlib import Path

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def env_var(name):
    """Environment first, then .env at the repo root — same contract as the
    Go apiKey() (KEY=VALUE lines, quotes stripped)."""
    if v := os.environ.get(name):
        return v
    env_file = Path(__file__).resolve().parent.parent / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            k, _, v = line.strip().partition("=")
            if k == name and v:
                return v.strip().strip("'\"")
    sys.exit(f"{name} not set (environment or .env at repo root)")

ap = argparse.ArgumentParser()
ap.add_argument("--through", required=True, help="most recent date to include (YYYY-MM-DD)")
ap.add_argument("--count", type=int, default=20, help="trading days to fetch (default 20)")
ap.add_argument("--out", default="data/flat-files/trades", help="output directory")
args = ap.parse_args()

# Session/endpoint per tools/download.py (same account).
session = boto3.Session(
    aws_access_key_id=env_var("AWS_ACCESS_KEY_ID"),
    aws_secret_access_key=env_var("AWS_SECRET_ACCESS_KEY"),
)
s3 = session.client("s3", endpoint_url="https://files.massive.com",
                    config=Config(signature_version="s3v4"))
BUCKET = "flatfiles"

out_dir = Path(args.out)
out_dir.mkdir(parents=True, exist_ok=True)

day = dt.date.fromisoformat(args.through)
found, probed = [], 0
while len(found) < args.count and probed < 3 * args.count:
    if day.weekday() < 5:  # Mon..Fri
        probed += 1
        key = f"us_stocks_sip/trades_v1/{day:%Y}/{day:%m}/{day}.csv.gz"
        try:
            size = s3.head_object(Bucket=BUCKET, Key=key)["ContentLength"]
            found.append((day, key, size))
        except ClientError as e:
            code = e.response["Error"]["Code"]
            if code in ("404", "NoSuchKey", "NotFound"):
                print(f"{day}: no file at vendor (holiday?) — skipping")
            else:
                raise
    day -= dt.timedelta(days=1)

if len(found) < args.count:
    print(f"WARNING: only found {len(found)}/{args.count} trading days", file=sys.stderr)

found.reverse()  # oldest first, nicer progress reading
total = sum(sz for _, _, sz in found)
print(f"{len(found)} trading days, {total / 1e9:.1f} GB total ({found[0][0]} .. {found[-1][0]})")

def make_progress(day, size):
    """Per-day progress line (pattern from tools/download.py): boto3 calls the
    callback from multiple transfer threads with each chunk's byte count."""
    start = time.monotonic()
    downloaded = 0
    lock = threading.Lock()

    def progress(chunk):
        nonlocal downloaded
        with lock:
            downloaded += chunk
            elapsed = time.monotonic() - start
            speed = downloaded / elapsed if elapsed > 0 else 0
            pct = downloaded / size * 100
            eta = (size - downloaded) / speed if speed > 0 else 0
            sys.stdout.write(
                f"\r{day}: {downloaded / 1e9:5.2f} / {size / 1e9:.2f} GB"
                f"  {pct:5.1f}%  {speed / 1e6:6.1f} MB/s"
                f"  elapsed {elapsed:4.0f}s  eta {eta:4.0f}s"
            )
            sys.stdout.flush()

    return progress, start


for day, key, size in found:
    dest = out_dir / f"{day}.csv.gz"
    if dest.exists() and dest.stat().st_size == size:
        print(f"{day}: already present ({size / 1e9:.2f} GB) — skipping")
        continue
    progress, start = make_progress(day, size)
    s3.download_file(BUCKET, key, str(dest), Callback=progress)
    elapsed = time.monotonic() - start
    print(f"\r{day}: done — {size / 1e9:.2f} GB in {elapsed:.0f}s"
          f" ({size / 1e6 / elapsed:.1f} MB/s avg)" + " " * 20)

print("all downloads complete")
