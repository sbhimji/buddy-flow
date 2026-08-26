// Package uwfeed feeds the Unusual Whales options tape (mini-spec 7.1).
// live.go mirrors internal/feed/live.go: one connection carries per-ticker
// option_trades channels for the whole universe; the read loop appends each
// raw frame to capture BEFORE classifying it (capture-before-parse). Full
// decode is 7.2's — this file only counts frame shapes (C4).
//
// The mirroring of feed's reconnect/watcher machinery is deliberate, not
// accidental: the plan trades DRY for zero blast radius on the proven
// equity feeder. Extraction of a shared session runner is backlogged until
// this feeder has its own live mileage.
package uwfeed

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/session"
)

// DefaultURL is the UW socket endpoint; the token is appended as a query
// parameter at dial time and never appears in logs or control records (C1).
const DefaultURL = "wss://api.unusualwhales.com/socket"

// LiveOptions configures one live options session.
type LiveOptions struct {
	URL     string // default DefaultURL
	APIKey  string
	Symbols []string // universe; one option_trades:<sym> join per symbol
	Until   time.Time
	Stop    <-chan struct{} // optional: close to end the session early
	Capture *capture.Writer // required: capture is unconditional (mini-spec)
	Log     func(format string, args ...any)
	// Frame, when set, receives every captured frame after classification —
	// the 7.2 decode seam. The read loop never depends on it.
	Frame func(recvNs int64, frame []byte)
}

// FatalError marks failures that must end the session instead of triggering
// the reconnect loop: capture cannot write, rejected credentials, and a
// connection whose every join was rejected — reconnecting against a server
// that refuses everything thrashes all day with zero data (the 1.2 review
// #1/#2 lesson, C2).
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

func fatal(err error) error { return &FatalError{Err: err} }

// LiveStats reports what one live session did. Frame shapes per C4: a join
// ack is ["chan",{"response":…,"status":…}]; a data frame is ["chan",{…}]
// without a status; a frame whose payload is not a JSON object, or whose
// channel we never joined, trips its own counter — both are protocol-drift
// tripwires, never silently folded into Data.
type LiveStats struct {
	Frames         int64
	Data           int64
	AcksOK         int64
	AcksNonOK      int64
	UnknownShape   int64 // frame not a [channel, object] tuple
	UnknownChannel int64 // well-formed frame for a channel we never joined
	Reconnects     int64
}

// RunLive connects, joins the universe channels, and pumps frames until
// Until or a permanent error. Reconnects with capped backoff on socket
// errors (re-joining everything — join state is per-connection), writing
// disconnect/reconnect control records around each gap.
func RunLive(opt LiveOptions) (LiveStats, error) {
	var stats LiveStats
	if opt.URL == "" {
		opt.URL = DefaultURL
	}
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	if opt.Capture == nil {
		return stats, fmt.Errorf("capture writer is required — capture is unconditional (mini-spec 7.1)")
	}

	// The joined-channel set drives the unknown-channel tripwire (the
	// options analog of feed's unknown-symbol counter).
	expected := make(map[string]bool, len(opt.Symbols))
	for _, s := range opt.Symbols {
		expected["option_trades:"+s] = true
	}

	backoff := time.Second
	first := true
	for time.Now().Before(opt.Until) && !stopClosed(opt.Stop) {
		if !first {
			stats.Reconnects++
			opt.Capture.Control("reconnect", fmt.Sprintf("attempt after backoff %s", backoff))
		}
		connStart := time.Now()
		err := runOneConnection(&opt, expected, &stats)
		if time.Now().After(opt.Until) || stopClosed(opt.Stop) {
			return stats, nil
		}
		var fe *FatalError
		if errors.As(err, &fe) {
			opt.Capture.Control("fatal", err.Error())
			return stats, err // no reconnect: retrying cannot help (C2)
		}
		if err != nil {
			// A connection that lived a while before failing was healthy —
			// restart the backoff ladder (the 1.2 review #8 lesson).
			if time.Since(connStart) > time.Minute {
				backoff = time.Second
			}
			opt.Capture.Control("disconnect", err.Error())
			opt.Log("socket error: %v — reconnecting in %s", err, backoff)
			wait := backoff
			if rem := time.Until(opt.Until); rem < wait {
				wait = rem
			}
			if wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-t.C:
				case <-opt.Stop: // nil channel blocks forever; timer still fires
				}
				t.Stop()
			}
			if backoff *= 2; backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		first = false
	}
	return stats, nil
}

// runOneConnection handles a single dial→join→pump cycle.
func runOneConnection(opt *LiveOptions, expected map[string]bool, stats *LiveStats) error {
	conn, resp, err := websocket.DefaultDialer.Dial(dialURL(opt.URL, opt.APIKey), nil)
	if err != nil {
		// Only an auth-shaped rejection is fatal (C2). A 429 during a
		// reconnect storm is transient — the backoff ladder is its remedy,
		// and ending the day's capture over it would be self-inflicted.
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return fatal(fmt.Errorf("handshake rejected (%s) — check UNUSUAL_WHALES_API_KEY: %w", resp.Status, err))
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	// Watcher: closing the conn is the only way to unblock ReadMessage; the
	// deliberate flag distinguishes watcher closes from genuine faults (the
	// 1.2 pattern, including its 08-13 deadline-close lesson).
	var deliberate atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-opt.Stop: // nil channel blocks forever — the other cases still fire
			deliberate.Store(true)
			conn.Close()
		case <-time.After(time.Until(opt.Until)):
			deliberate.Store(true)
			conn.Close()
		case <-watchDone:
		}
	}()
	opt.Capture.Control("connect", RedactURL(opt.URL)) // C1: never the token

	// Join everything up front; acks arrive interleaved with data and are
	// classified in the read loop. 7.0: 189 joins on one connection all ack.
	// json.Marshal, not Sprintf: the symbol list is trader-edited config and
	// must not be able to produce malformed protocol JSON.
	for _, s := range opt.Symbols {
		msg, err := json.Marshal(joinMsg{Channel: "option_trades:" + s, MsgType: "join"})
		if err != nil {
			return fatal(fmt.Errorf("marshal join for %q: %w", s, err))
		}
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return fmt.Errorf("send join: %w", err)
		}
	}
	opt.Log("joined %d option_trades channels", len(opt.Symbols))

	// Read deadline: a silent dead socket becomes a reconnect instead of a
	// hang. In-session (options print continuously) 30s of silence is a
	// fault. OUTSIDE the session silence is normal, and cycling there is NOT
	// harmless: on 2026-08-20 a pre-open reconnect straddled 09:30:00 and
	// the 189 re-joins were in flight while the opening burst hit — 10,379
	// prints lost in the one minute that matters most (7.5 parity finding).
	// So the deadline is long when no prints are expected.
	var acksOK, acksNonOK int
	for {
		if time.Now().After(opt.Until) || stopClosed(opt.Stop) {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(readDeadline(time.Now())))
		_, frame, err := conn.ReadMessage()
		recvNs := time.Now().UnixNano()
		if err != nil {
			if deliberate.Load() || time.Now().After(opt.Until) || stopClosed(opt.Stop) {
				return nil // deliberate close by the watcher, not a fault
			}
			return fmt.Errorf("read: %w", err)
		}
		if err := opt.Capture.Append(recvNs, frame); err != nil {
			// A session that cannot record must not run: fatal, not a blip.
			return fatal(fmt.Errorf("capture append: %w", err))
		}
		if ch, st := classifyFrame(frame, expected, stats); st != "" {
			if st == "ok" {
				acksOK++
			} else {
				acksNonOK++
				opt.Log("non-ok join ack: %s -> %s", ch, st)
			}
			// One rejected symbol must not kill the rest (C2) — but a
			// connection where EVERY join was rejected is entitlement/token
			// revocation, and reconnecting would capture acks-only garbage
			// all day with no error signal. Fatal.
			if allJoinsRejected(acksOK, acksNonOK, len(opt.Symbols)) {
				return fatal(fmt.Errorf("all %d joins rejected (last: %s -> %s) — entitlement or token revoked?", len(opt.Symbols), ch, st))
			}
		}
		if opt.Frame != nil {
			opt.Frame(recvNs, frame)
		}
	}
}

// joinMsg is the UW Phoenix-style join message.
type joinMsg struct {
	Channel string `json:"channel"`
	MsgType string `json:"msg_type"`
}

// allJoinsRejected reports the fatal condition: every join on this
// connection acked non-ok and none succeeded.
func allJoinsRejected(ok, nonOK, total int) bool {
	return total > 0 && ok == 0 && nonOK >= total
}

// classifyFrame counts one frame's shape (C4) and returns the channel and
// status when the frame is an ack ("" status otherwise). No field decode —
// that is 7.2's decoder; the frame is already safely in capture. A payload
// that is not a JSON object is UnknownShape, never Data — folding protocol
// drift into the data count would blind the tripwire.
func classifyFrame(frame []byte, expected map[string]bool, stats *LiveStats) (channel, status string) {
	stats.Frames++
	var tuple []json.RawMessage
	if json.Unmarshal(frame, &tuple) != nil || len(tuple) != 2 {
		stats.UnknownShape++
		return "", ""
	}
	var ch string
	if json.Unmarshal(tuple[0], &ch) != nil {
		stats.UnknownShape++
		return "", ""
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(tuple[1], &body); err != nil {
		stats.UnknownShape++
		return "", ""
	}
	if expected != nil && !expected[ch] {
		stats.UnknownChannel++
		return ch, ""
	}
	if body.Status != "" {
		if body.Status == "ok" {
			stats.AcksOK++
		} else {
			stats.AcksNonOK++
		}
		return ch, body.Status
	}
	stats.Data++
	return ch, ""
}

// dialURL sets the token query parameter on the endpoint — the ONLY place
// the real token joins the URL. net/url placement keeps the query ahead of
// any fragment and escapes the token correctly.
func dialURL(base, key string) string {
	u, err := url.Parse(base)
	if err != nil {
		// An unparseable -url will fail the dial loudly anyway; the naive
		// form keeps the error message pointed at the real problem.
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		return base + sep + "token=" + url.QueryEscape(key)
	}
	q := u.Query()
	q.Set("token", key)
	u.RawQuery = q.Encode()
	return u.String()
}

// RedactURL is the only form of the endpoint that may be logged or written
// to the capture stream (C1). The token parameter is set to REDACTED —
// url.Values.Set collapses duplicate token= parameters, so no copy of a
// real token survives; fragments and other parameters are preserved. The
// parameter is set even when absent: the dial URL carried one.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable endpoint; token redacted>"
	}
	q := u.Query()
	q.Set("token", "REDACTED")
	u.RawQuery = q.Encode()
	return u.String()
}

// stopClosed reports whether the optional stop channel is closed.
func stopClosed(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// Deadline policy. In-session (09:30–17:05 ET) 30s of silence is a fault.
// Before the open a healthy socket is silent for minutes — the deadline
// must NOT expire until just after the open, so a reconnect can never
// straddle 09:30:00 (the 2026-08-20 loss); but it must expire soon after
// the open so a socket that died pre-open is caught within ~30s of the
// first prints it should have delivered. Off-session otherwise: 15 min.
const (
	sessionStartMinute = 9*60 + 30 // 09:30 ET
	sessionEndMinute   = 17*60 + 5 // 17:05 ET
	inSessionDeadline  = 30 * time.Second
	offSessionDeadline = 15 * time.Minute
	postOpenGrace      = 30 * time.Second // pre-open deadline lands at 09:30:30
)

// readDeadline returns how long a silent socket is tolerated at wall time
// now.
func readDeadline(now time.Time) time.Duration {
	et := now.In(session.ET())
	m := et.Hour()*60 + et.Minute()
	if m >= sessionStartMinute && m < sessionEndMinute {
		return inSessionDeadline
	}
	if m < sessionStartMinute {
		open := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, session.ET()).Add(postOpenGrace)
		if d := open.Sub(et); d < offSessionDeadline {
			if d < inSessionDeadline {
				return inSessionDeadline
			}
			return d
		}
	}
	return offSessionDeadline
}
