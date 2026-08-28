#!/usr/bin/env python3
"""Unit tests for tools/live_view_server.py (MO-7: two tabs).

    python3 -m unittest tools.test_live_view_server -v      # from the repo root
    python3 tools/test_live_view_server.py

Stdlib only; the server test binds an ephemeral localhost port and never
touches data/.
"""

import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.request
from http.server import ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import live_view_server as lvs  # noqa: E402

FRAME = lvs.FRAME_DELIM


class DefaultTabTest(unittest.TestCase):
    """default_tab is a pure function of the ET wall clock (T2)."""

    def test_boundary(self):
        self.assertEqual(lvs.default_tab(9, 29), "pre")   # 09:29:59 still premarket
        self.assertEqual(lvs.default_tab(9, 30), "session")  # 09:30:00 is the open

    def test_far_from_open(self):
        self.assertEqual(lvs.default_tab(0, 0), "pre")
        self.assertEqual(lvs.default_tab(4, 0), "pre")
        self.assertEqual(lvs.default_tab(8, 59), "pre")
        self.assertEqual(lvs.default_tab(10, 0), "session")
        self.assertEqual(lvs.default_tab(15, 59), "session")
        self.assertEqual(lvs.default_tab(23, 59), "session")


class FrameServerTest(unittest.TestCase):
    """/frame?tab=session|pre serve their respective logs, staleness per tab,
    ?basket= applies to the session tab only."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        d = cls.tmp.name
        cls.session_log = os.path.join(d, "live.log")
        cls.pre_log = os.path.join(d, "live-pre.log")
        cls.drill = os.path.join(d, "live-drill.txt")
        with open(cls.session_log, "wb") as f:
            f.write(FRAME + b"09:45:00 ET  2026-08-24   SESSION-FRAME\nBASKET  cum_share\nsemis_compute  12.3%\n\nfooter\n")
        with open(cls.pre_log, "wb") as f:
            f.write(FRAME + b"09:45:00 ET  2026-08-24   PRE-FRAME\nBASKET  pre_vol\nsemis_compute  $1.2M\n\nfooter\n")
        with open(cls.drill, "wb") as f:
            f.write(b"## basket semis_compute\n    NVDA  drill-row\n")
        # The premarket log is older than the session log: staleness must
        # be reported per tab, from each file's own mtime.
        old = time.time() - 120
        os.utime(cls.pre_log, (old, old))
        handler = lvs.make_handler(cls.session_log, cls.drill, cls.pre_log)
        cls.srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        cls.port = cls.srv.server_address[1]
        cls.thread = threading.Thread(target=cls.srv.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()
        cls.tmp.cleanup()

    def get(self, path):
        with urllib.request.urlopen("http://127.0.0.1:%d%s" % (self.port, path)) as r:
            return r.status, r.headers, r.read().decode()

    def test_session_tab(self):
        for path in ("/frame", "/frame?tab=session"):
            status, hdr, body = self.get(path)
            self.assertEqual(status, 200)
            self.assertIn("SESSION-FRAME", body)
            self.assertNotIn("PRE-FRAME", body)
            self.assertLess(float(hdr["X-Log-Age-Seconds"]), 60)

    def test_pre_tab(self):
        status, hdr, body = self.get("/frame?tab=pre")
        self.assertEqual(status, 200)
        self.assertIn("PRE-FRAME", body)
        self.assertNotIn("SESSION-FRAME", body)
        # staleness is the premarket log's own age (backdated 120s above)
        self.assertGreater(float(hdr["X-Log-Age-Seconds"]), 100)

    def test_basket_drill_session_only(self):
        _, _, body = self.get("/frame?tab=session&basket=semis_compute")
        self.assertIn("drill-row", body)
        self.assertIn('class="sel"', body)  # the selected basket is linked/highlighted
        _, _, body = self.get("/frame?tab=pre&basket=semis_compute")
        self.assertNotIn("drill-row", body)
        self.assertNotIn("<a href", body)  # no basket links on the premarket tab
        self.assertIn("PRE-FRAME", body)

    def test_bad_tab(self):
        with self.assertRaises(urllib.error.HTTPError) as cm:
            self.get("/frame?tab=nope")
        self.assertEqual(cm.exception.code, 400)

    def test_pre_tab_without_pre_log(self):
        handler = lvs.make_handler(self.session_log, self.drill, "")
        srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        t = threading.Thread(target=srv.serve_forever, daemon=True)
        t.start()
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/frame?tab=pre" % srv.server_address[1]) as r:
                self.assertIn("premarket tab not served", r.read().decode())
                self.assertIsNone(r.headers.get("X-Log-Age-Seconds"))
        finally:
            srv.shutdown()
            srv.server_close()

    def test_page_has_tabs(self):
        _, _, body = self.get("/")
        self.assertIn("PREMARKET", body)
        self.assertIn("SESSION", body)
        self.assertIn("America/New_York", body)


if __name__ == "__main__":
    unittest.main()
