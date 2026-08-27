# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "boto3",
# ]
# ///
import os
import sys
import threading
import time
from pathlib import Path

import boto3
from botocore.config import Config


def env_var(name):
  """Environment first, then .env at the repo root — same contract as the
  Go apiKey() (KEY=VALUE lines, quotes stripped)."""
  if v := os.environ.get(name):
    return v
  env_file = Path(__file__).resolve().parent.parent / '.env'
  if env_file.exists():
    for line in env_file.read_text().splitlines():
      k, _, v = line.strip().partition('=')
      if k == name and v:
        return v.strip().strip('\'"')
  sys.exit(f"{name} not set (environment or .env at repo root)")


# Initialize a session using your credentials
session = boto3.Session(
  aws_access_key_id=env_var('AWS_ACCESS_KEY_ID'),
  aws_secret_access_key=env_var('AWS_SECRET_ACCESS_KEY'),
)

# Create a client with your session and specify the endpoint
s3 = session.client(
  's3',
  endpoint_url='https://files.massive.com',
  config=Config(signature_version='s3v4'),
)

# Specify the bucket name
bucket_name = 'flatfiles'

# Specify the S3 object key name
object_key = 'flatfiles/us_stocks_sip/quotes_v1/2026/08/2026-08-11.csv.gz'

# Remove the bucket name (e.g. 'flatfiles/') prefix if present in object_key
if object_key.startswith(bucket_name + '/'):
  object_key = object_key[len(bucket_name + '/'):]

# Specify the local file name and path to save the downloaded file
local_file_name = object_key.split('/')[-1]  # e.g., '2025-06-12.csv.gz'
local_file_path = './' + local_file_name

# Print the file being downloaded
print(f"Downloading file '{object_key}' from bucket '{bucket_name}'...")

# Total size up front so progress can show percent and ETA
total_bytes = s3.head_object(Bucket=bucket_name, Key=object_key)['ContentLength']

# Progress callback: boto3 calls this from multiple transfer threads with the
# byte count of each chunk, hence the lock
start = time.monotonic()
downloaded = 0
lock = threading.Lock()

def progress(chunk):
  global downloaded
  with lock:
    downloaded += chunk
    elapsed = time.monotonic() - start
    speed = downloaded / elapsed if elapsed > 0 else 0
    pct = downloaded / total_bytes * 100
    eta = (total_bytes - downloaded) / speed if speed > 0 else 0
    sys.stdout.write(
      f"\r{downloaded / 1e9:6.2f} / {total_bytes / 1e9:.2f} GB"
      f"  {pct:5.1f}%  {speed / 1e6:6.1f} MB/s"
      f"  elapsed {elapsed:5.0f}s  eta {eta:5.0f}s"
    )
    sys.stdout.flush()

# Download the file
s3.download_file(bucket_name, object_key, local_file_path, Callback=progress)

elapsed = time.monotonic() - start
print(f"\nDone: {total_bytes / 1e9:.2f} GB in {elapsed:.0f}s"
      f" ({total_bytes / 1e6 / elapsed:.1f} MB/s average)")