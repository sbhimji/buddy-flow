#!/usr/bin/env python3
"""Serve the live trader view as a web page, read-only, from data/live.log.

The launchd live job (-view) writes ANSI table frames to data/live.log.
This server tails that file — never writes, never signals the capture —
extracts the most recent complete frame, converts ANSI colors to HTML,
and serves it at / with 1-second polling. Stdlib only.

    python3 tools/live_view_server.py            # http://<this-mac>:8787
    python3 tools/live_view_server.py --port 9000 --log data/live.log
    http://<this-mac>:8787/?basket=semis_compute          # ticker rows under that basket
    http://<this-mac>:8787/?basket=semis_compute,quantum  # several open; ?basket=all

The drill-down (ticker-view-v0) comes from the drill file cmd/live -view
-drill writes (every basket's member rows, "## basket <name>" sections);
this server only reads it. Without the flag the query renders a note.

Stopgap for terminal-less mornings; the durable fix is the follow-mode
replay view (see docs/backlog.md).
"""

import argparse
import html
import os
import re
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

FRAME_DELIM = b"\x1b[H\x1b[2J"  # cursor-home + clear-screen, printed at each frame start
TAIL_BYTES = 400_000            # plenty for several frames
SGR = re.compile(r"\x1b\[([0-9;]*)m")

PAGE = """<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=0.5">
<title>Morning Tape</title>
<style>
  body { background:#0b0e12; color:#d8dee9; margin:0; padding:12px; }
  pre  { font:12px/1.45 "SF Mono", Menlo, Consolas, monospace; white-space:pre; overflow-x:auto; }
  .b   { font-weight:bold; }
  .f30{color:#4c566a} .f31{color:#ff6b6b} .f32{color:#5af78e} .f33{color:#f3f99d}
  .f34{color:#57c7ff} .f35{color:#ff6ac1} .f36{color:#9aedfe} .f37{color:#d8dee9}
  .f90{color:#6b7386} .f91{color:#ff9b9b} .f92{color:#9df7be} .f93{color:#f7fbb0}
  .f94{color:#9bd7ff} .f95{color:#ff9ada} .f96{color:#c4f4ff} .f97{color:#eceff4}
  .inv { background:#d8dee9; color:#0b0e12; }
  #age { color:#6b7386; font:11px Menlo, monospace; padding-bottom:6px; }
  #nav { font:11px Menlo, monospace; color:#6b7386; padding-bottom:6px; }
  a { color:#57c7ff; text-decoration:none; } a:hover { text-decoration:underline; }
  a.sel { color:#f3f99d; font-weight:bold; }
  #age.stale { color:#ff6b6b; font-weight:bold; }
</style></head>
<body><div id="nav">click a basket name to open/close its tickers · <a href="?basket=all">all</a> · <a href="?">none</a></div><div id="age">connecting…</div><pre id="t"></pre>
<script>
const basket = new URLSearchParams(location.search).get('basket') || '';
async function tick() {
  try {
    const r = await fetch('/frame' + (basket ? '?basket=' + encodeURIComponent(basket) : ''), {cache: 'no-store'});
    const age = parseFloat(r.headers.get('X-Log-Age-Seconds') || 'NaN');
    document.getElementById('t').innerHTML = await r.text();
    const el = document.getElementById('age');
    if (isNaN(age)) { el.textContent = 'log age unknown'; el.className = ''; }
    else {
      el.textContent = 'log written ' + age.toFixed(1) + 's ago';
      el.className = age > 10 ? 'stale' : '';
      if (age > 10) el.textContent += ' — view may be stale (capture stopped or market quiet)';
    }
  } catch (e) {
    document.getElementById('age').textContent = 'server unreachable: ' + e;
    document.getElementById('age').className = 'stale';
  }
}
tick(); setInterval(tick, 1000);
</script></body></html>
"""


def last_frame(path):
    """Return (frame_bytes, age_seconds) for the newest complete frame."""
    size = os.path.getsize(path)
    with open(path, "rb") as f:
        f.seek(max(0, size - TAIL_BYTES))
        chunk = f.read()
    age = time.time() - os.path.getmtime(path)
    frames = chunk.split(FRAME_DELIM)
    # frames[0] is a partial head (or pre-frame output); prefer the last
    # delimited frame, falling back one if the newest looks torn mid-write.
    if len(frames) >= 3 and not frames[-1].endswith(b"\n"):
        return frames[-2], age
    if len(frames) >= 2:
        return frames[-1], age
    return chunk, age


def ansi_to_html(data):
    s = data.decode("utf-8", "replace")
    out, pos = [], 0
    st = {"bold": False, "fg": None, "inv": False}

    def emit(text):
        if not text:
            return
        cls = []
        if st["bold"]:
            cls.append("b")
        if st["fg"]:
            cls.append("f%d" % st["fg"])
        if st["inv"]:
            cls.append("inv")
        esc = html.escape(text)
        out.append('<span class="%s">%s</span>' % (" ".join(cls), esc) if cls else esc)

    for m in SGR.finditer(s):
        emit(s[pos : m.start()])
        pos = m.end()
        for p in (m.group(1) or "0").split(";"):
            n = int(p) if p else 0
            if n == 0:
                st.update(bold=False, fg=None, inv=False)
            elif n == 1:
                st["bold"] = True
            elif n == 22:
                st["bold"] = False
            elif n == 7:
                st["inv"] = True
            elif n == 27:
                st["inv"] = False
            elif 30 <= n <= 37 or 90 <= n <= 97:
                st["fg"] = n
            elif n == 39:
                st["fg"] = None
    emit(s[pos:])
    return "".join(out)


BASKET_LINE = re.compile(r"^([A-Za-z0-9_]+)(?=\s)", re.M)


def link_baskets(html_text, selected):
    """Wrap each basket name (first token of a top-level table row) in a
    link that toggles it in the selection (comma list in ?basket=). Table
    rows are the lines after the BASKET header up to the first blank line;
    drill-down rows are indented and skipped."""
    lines = html_text.split("\n")
    out, in_table = [], False
    for ln in lines:
        if ln.startswith("BASKET"):
            in_table = True
            out.append(ln)
            continue
        if in_table and ln == "":
            in_table = False
        if in_table and ln and not ln.startswith(" ") and not ln.startswith("<"):
            m = BASKET_LINE.match(ln)
            if m:
                name = m.group(1)
                on = name in selected or "all" in selected
                nxt = [b for b in selected if b not in (name, "all")]
                if not on:
                    nxt.append(name)
                href = "?basket=" + ",".join(nxt) if nxt else "?"
                cls = ' class="sel"' if on else ""
                ln = '<a href="%s"%s>%s</a>' % (href, cls, name) + ln[m.end():]
        out.append(ln)
    return "\n".join(out)


def drill_sections(drill_path):
    """Parse the drill file into {basket: body_bytes} (marker line dropped).
    Returns (sections, note); note is set when the file cannot be served."""
    if not drill_path:
        return {}, b"(drill-down not served: start this server with --drill <file> and cmd/live -view -drill <file>)"
    try:
        with open(drill_path, "rb") as f:
            data = f.read()
    except OSError as e:
        return {}, ("(drill file unreadable: %s)" % e).encode()
    out = {}
    for part in data.split(b"## basket ")[1:]:
        name, _, body = part.partition(b"\n")
        out[name.strip().decode()] = body.rstrip(b"\n")
    return out, b""


def splice_drill(frame, selected, drill_path):
    """Insert each selected basket's ticker rows directly under that
    basket's line in the table (never after the footer). "all" selects
    every basket. Works on the raw ANSI bytes; conversion happens after."""
    if not selected:
        return frame
    sections, note = drill_sections(drill_path)
    want_all = "all" in selected
    lines = frame.split(b"\n")
    out, in_table, hit = [], False, False
    for ln in lines:
        out.append(ln)
        plain = SGR.sub("", ln.decode("utf-8", "replace"))
        if plain.startswith("BASKET"):
            in_table = True
            continue
        if in_table and plain.strip() == "":
            in_table = False
        if not in_table or not plain or plain.startswith(" "):
            continue
        m = BASKET_LINE.match(plain)
        if not m:
            continue
        name = m.group(1)
        if not (want_all or name in selected):
            continue
        hit = True
        if note:
            out.append(b"    " + note)
        elif name in sections:
            out.append(sections[name])
        else:
            out.append(b"    (no rows for %s in the drill file yet)" % name.encode())
    if not hit and not want_all:
        out.append(b"(no basket named %s in the table)" % ",".join(selected).encode())
    return b"\n".join(out)


def make_handler(log_path, drill_path):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            path, _, query = self.path.partition("?")
            selected = []
            for kv in query.split("&"):
                k, _, v = kv.partition("=")
                if k == "basket":
                    selected = [b for b in urllib.parse.unquote(v).split(",") if b]
            if path == "/":
                body = PAGE.encode()
                ctype = "text/html; charset=utf-8"
                age = None
            elif path == "/frame":
                try:
                    frame, age = last_frame(log_path)
                    frame = splice_drill(frame, selected, drill_path)
                    body = link_baskets(ansi_to_html(frame), selected).encode()
                except OSError as e:
                    body = html.escape("cannot read %s: %s" % (log_path, e)).encode()
                    age = None
                ctype = "text/html; charset=utf-8"
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            if age is not None:
                self.send_header("X-Log-Age-Seconds", "%.1f" % age)
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):  # silence per-request stderr noise
            pass

    return Handler


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--log", default=os.path.join(os.path.dirname(__file__), "..", "data", "live.log"))
    ap.add_argument("--port", type=int, default=8787)
    ap.add_argument("--bind", default="0.0.0.0", help="0.0.0.0 = reachable on your LAN")
    ap.add_argument("--drill", default="", help="drill file written by cmd/live -view -drill (serves ?basket=NAME)")
    args = ap.parse_args()
    log_path = os.path.abspath(args.log)
    drill_path = os.path.abspath(args.drill) if args.drill else ""
    srv = ThreadingHTTPServer((args.bind, args.port), make_handler(log_path, drill_path))
    print("serving %s on http://%s:%d (read-only tail)" % (log_path, args.bind, args.port))
    srv.serve_forever()


if __name__ == "__main__":
    main()
