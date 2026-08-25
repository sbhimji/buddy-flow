#!/bin/sh
# Options-tape live dashboard as a separate read-only process (mini-spec 7.7):
# follows today's capture and writes ANSI table frames to the log that
# tools/live_view_server.py --port 8788 serves — the same format the trader
# already watches for the equity tape on :8787. Safe to kill/restart; the
# capture process (cmd/live-options) never knows.
cd /Users/saimbhimji/repo/buddy-flow || exit 1
DATE=$(TZ=America/New_York date +%F)
exec ./bin/replay-options -follow \
  -capture "data/capture-options/$DATE/stream.jsonl" \
  -profiles data/profiles-options \
  -refresh 5s
