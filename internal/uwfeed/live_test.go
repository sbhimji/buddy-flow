package uwfeed

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Real ack frame captured 2026-08-18 (discovery-scripts/verify-uw) — the
// wire truth the classifier must recognize.
const realAck = `["option_trades:AAPL",{"response":{},"status":"ok"}]`

func testExpected() map[string]bool {
	return map[string]bool{
		"option_trades:AAPL": true,
		"option_trades:BAD":  true,
		"option_trades:NVDA": true,
	}
}

func TestClassifyFrameShapes(t *testing.T) {
	cases := []struct {
		name, frame                      string
		data, ok, non, unkShape, unkChan int64
		wantCh, wantStatus               string
	}{
		{"real ack ok", realAck, 0, 1, 0, 0, 0, "option_trades:AAPL", "ok"},
		{"ack error", `["option_trades:BAD",{"response":{},"status":"error"}]`, 0, 0, 1, 0, 0, "option_trades:BAD", "error"},
		{"data tuple", `["option_trades:NVDA",{"id":"x","executed_at":1787000000000,"premium":"595.00"}]`, 1, 0, 0, 0, 0, "option_trades:NVDA", ""},
		{"garbage", `{"ev":"status"}`, 0, 0, 0, 1, 0, "", ""},
		{"not json", `hello`, 0, 0, 0, 1, 0, "", ""},
		{"wrong arity", `["a","b","c"]`, 0, 0, 0, 1, 0, "", ""},
		{"non-string channel", `[42,{"id":"x"}]`, 0, 0, 0, 1, 0, "", ""},
		// A non-object payload is protocol drift, never Data — folding it
		// into Data would blind the cmd tripwire (review fix).
		{"string body", `["option_trades:AAPL","what"]`, 0, 0, 0, 1, 0, "", ""},
		{"number body", `["option_trades:AAPL",7]`, 0, 0, 0, 1, 0, "", ""},
		{"array body", `["option_trades:AAPL",[1,2]]`, 0, 0, 0, 1, 0, "", ""},
		// A well-formed frame for a channel we never joined is the
		// universe-drift tripwire (review fix).
		{"unjoined channel data", `["option_trades:TSLA",{"id":"x"}]`, 0, 0, 0, 0, 1, "option_trades:TSLA", ""},
		{"unjoined channel ack", `["flow-alerts",{"response":{},"status":"ok"}]`, 0, 0, 0, 0, 1, "flow-alerts", ""},
	}
	for _, c := range cases {
		var s LiveStats
		ch, status := classifyFrame([]byte(c.frame), testExpected(), &s)
		if s.Frames != 1 || s.Data != c.data || s.AcksOK != c.ok || s.AcksNonOK != c.non ||
			s.UnknownShape != c.unkShape || s.UnknownChannel != c.unkChan {
			t.Errorf("%s: stats = %+v", c.name, s)
		}
		if ch != c.wantCh || status != c.wantStatus {
			t.Errorf("%s: (ch, status) = (%q, %q), want (%q, %q)", c.name, ch, status, c.wantCh, c.wantStatus)
		}
	}
}

// One rejected symbol never kills the connection; a connection where every
// join was rejected with none succeeding is fatal (C2 refined).
func TestAllJoinsRejected(t *testing.T) {
	cases := []struct {
		ok, nonOK, total int
		want             bool
	}{
		{0, 189, 189, true},
		{1, 188, 189, false}, // one success = entitled; bad symbols only
		{0, 188, 189, false}, // not all acks in yet
		{0, 190, 189, true},  // re-acks past total still all-rejected
		{189, 0, 189, false},
		{0, 0, 0, false}, // no symbols, no verdict
	}
	for _, c := range cases {
		if got := allJoinsRejected(c.ok, c.nonOK, c.total); got != c.want {
			t.Errorf("allJoinsRejected(%d,%d,%d) = %v, want %v", c.ok, c.nonOK, c.total, got, c.want)
		}
	}
}

// C1: the token must never be loggable — the redacted form is the only one
// permitted near the capture stream, and dialURL is the only place the real
// token appears.
func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		DefaultURL:                      DefaultURL + "?token=REDACTED",
		DefaultURL + "?token=secret123": DefaultURL + "?token=REDACTED",
		DefaultURL + "?token=s3c&x=1":   DefaultURL + "?token=REDACTED&x=1",
		DefaultURL + "?a=1&token=s3c":   DefaultURL + "?a=1&token=REDACTED",
		// Duplicate token parameters must ALL collapse (review fix: the
		// old first-occurrence redaction leaked the second copy).
		DefaultURL + "?token=s3c&token=secret123": DefaultURL + "?token=REDACTED",
		// Fragments survive; the token inside the query still dies.
		DefaultURL + "?token=s3c#frag": DefaultURL + "?token=REDACTED#frag",
		// No-token URL with an existing query must not grow a second "?".
		DefaultURL + "?x=1": DefaultURL + "?token=REDACTED&x=1",
	}
	for in, want := range cases {
		got := RedactURL(in)
		if got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "s3c") || strings.Contains(got, "secret123") {
			t.Errorf("RedactURL(%q) leaks the token: %q", in, got)
		}
	}
	if got := RedactURL("://not a url"); strings.Contains(got, "not a url") || !strings.Contains(got, "redacted") {
		t.Errorf("unparseable URL not safely handled: %q", got)
	}
}

func TestDialURLAppendsToken(t *testing.T) {
	if got := dialURL(DefaultURL, "k+y"); got != DefaultURL+"?token=k%2By" {
		t.Errorf("dialURL = %q", got)
	}
	// url.Values.Encode sorts parameters; placement changes, the token is
	// correctly a query parameter either way.
	if got := dialURL(DefaultURL+"?x=1", "k"); got != DefaultURL+"?token=k&x=1" {
		t.Errorf("dialURL with existing query = %q", got)
	}
	// The query must land BEFORE any fragment (review fix: naive append
	// put the token after "#", where servers never see it).
	if got := dialURL(DefaultURL+"#frag", "k"); got != DefaultURL+"?token=k#frag" {
		t.Errorf("dialURL with fragment = %q", got)
	}
}

// The join message is marshaled, not Sprintf'd: a trader-edited symbol with
// JSON metacharacters must produce valid protocol JSON, never a malformed
// frame (review fix).
func TestJoinMessageEscaping(t *testing.T) {
	msg, err := json.Marshal(joinMsg{Channel: `option_trades:A"B\C`, MsgType: "join"})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Channel string `json:"channel"`
		MsgType string `json:"msg_type"`
	}
	if err := json.Unmarshal(msg, &back); err != nil {
		t.Fatalf("join message is not valid JSON: %v (%s)", err, msg)
	}
	if back.Channel != `option_trades:A"B\C` || back.MsgType != "join" {
		t.Errorf("round-trip = %+v", back)
	}
}

// Capture is unconditional: RunLive without a writer must refuse before any
// network activity (mirror of the 1.2 contract).
func TestRunLiveRequiresCapture(t *testing.T) {
	_, err := RunLive(LiveOptions{Until: time.Now().Add(time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "capture writer is required") {
		t.Fatalf("err = %v, want capture-required refusal", err)
	}
}

// Pre-open silence must not cycle the connection into the open (the
// 2026-08-20 parity finding): the pre-open deadline lands just after
// 09:30, in-session is 30s, and off-session is long.
func TestReadDeadlinePolicy(t *testing.T) {
	cases := map[string]time.Duration{
		"2026-08-20T09:20:00-04:00": 10*time.Minute + 30*time.Second, // until 09:30:30
		"2026-08-20T09:29:50-04:00": 40 * time.Second,                // still spans the open
		"2026-08-20T09:30:00-04:00": inSessionDeadline,
		"2026-08-20T09:30:01-04:00": inSessionDeadline,
		"2026-08-20T16:59:00-04:00": inSessionDeadline,
		"2026-08-20T17:05:00-04:00": offSessionDeadline,
		"2026-08-20T02:00:00-04:00": offSessionDeadline, // far from the open: capped
		"2026-08-20T09:29:59-04:00": 31 * time.Second,   // still lands at 09:30:30
	}
	for in, want := range cases {
		tm, err := time.Parse(time.RFC3339, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := readDeadline(tm); got != want {
			t.Errorf("readDeadline(%s) = %v, want %v", in, got, want)
		}
	}
}
