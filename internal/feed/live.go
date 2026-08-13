// live.go — the websocket feeder (mini-spec 1.2). One connection carries
// both channels for the whole universe; the read loop appends each raw frame
// to capture BEFORE decoding it (capture-before-parse), then decodes and
// submits through the same pipeline the file feeders use.
package feed

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/ingest"
)

// LiveOptions configures one live session.
type LiveOptions struct {
	URL     string // default wss://socket.massive.com/stocks
	APIKey  string
	Symbols []string // universe; subscription list is T.<s>+Q.<s> for each
	Until   time.Time
	Stop    <-chan struct{} // optional: close to end the session early (signal handler)
	Capture *capture.Writer // required: capture is unconditional (mini-spec)
	Log     func(format string, args ...any)
}

// FatalError marks failures that must end the session instead of triggering
// the reconnect loop: capture cannot write (a session that cannot record is
// a session that must not run) and rejected credentials (reconnecting with
// the same bad key thrashes all day with zero data — 2026-08-13 review #1/#2).
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

func fatal(err error) error { return &FatalError{Err: err} }

// LiveStats reports what one live session did.
type LiveStats struct {
	Frames     int64
	Trades     int64
	Quotes     int64
	Status     int64 // status/control frames from the vendor
	Unknown    int64 // data events for symbols outside the table (should be 0)
	Reconnects int64
	DecodeErrs int64
}

// RunLive connects, authenticates, subscribes, and pumps frames until Until
// or a permanent error. Reconnects with capped backoff on socket errors,
// writing disconnect/reconnect control records around each gap.
func RunLive(p *ingest.Pipeline, opt LiveOptions) (LiveStats, error) {
	var stats LiveStats
	if opt.URL == "" {
		opt.URL = "wss://socket.massive.com/stocks"
	}
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	if opt.Capture == nil {
		return stats, fmt.Errorf("capture writer is required — capture is unconditional (mini-spec 1.2)")
	}

	backoff := time.Second
	first := true
	for time.Now().Before(opt.Until) && !stopClosed(opt.Stop) {
		if !first {
			stats.Reconnects++
			opt.Capture.Control("reconnect", fmt.Sprintf("attempt after backoff %s", backoff))
		}
		connStart := time.Now()
		err := runOneConnection(p, &opt, &stats)
		if time.Now().After(opt.Until) || stopClosed(opt.Stop) {
			return stats, nil
		}
		var fe *FatalError
		if errors.As(err, &fe) {
			opt.Capture.Control("fatal", err.Error())
			return stats, err // no reconnect: retrying cannot help (review #1/#2)
		}
		if err != nil {
			// A connection that lived a while before failing was healthy —
			// restart the backoff ladder instead of paying an ever-longer
			// gap for each scattered disconnect across a day (review #8).
			if time.Since(connStart) > time.Minute {
				backoff = time.Second
			}
			opt.Capture.Control("disconnect", err.Error())
			opt.Log("socket error: %v — reconnecting in %s", err, backoff)
			// Interruptible wait: Ctrl-C and the -until deadline must both
			// cut the backoff short (review #7).
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

// runOneConnection handles a single dial→auth→subscribe→pump cycle.
func runOneConnection(p *ingest.Pipeline, opt *LiveOptions, stats *LiveStats) error {
	conn, _, err := websocket.DefaultDialer.Dial(opt.URL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	// Watcher: closing the conn is the only way to unblock a ReadMessage,
	// so the Until deadline and the Stop channel both act through it. The
	// deliberate flag lets the read loop tell a watcher-initiated close from
	// a genuine socket fault even when the clock sits a hair before Until
	// (observed on the 08-13 smoke run: deadline close at 16:29:59 logged as
	// a spurious socket error).
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
	opt.Capture.Control("connect", opt.URL)

	// Auth immediately; the subscribe waits for auth_success (below).
	auth := fmt.Sprintf(`{"action":"auth","params":"%s"}`, opt.APIKey)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(auth)); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	// A read deadline turns a silent dead socket into a reconnect instead of
	// a permanent hang; any live market second has traffic, so 30s of true
	// silence after hours just cycles the connection harmlessly.
	authed := false
	table := p.Table()
	for {
		if time.Now().After(opt.Until) || stopClosed(opt.Stop) {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
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
		stats.Frames++
		decodeFrame(frame, table, p, stats)

		// Handshake: subscribe once auth_success arrives (frame already
		// captured and counted above). String scan, not JSON parse — the
		// status frames are tiny and this runs only until authed.
		if !authed {
			text := string(frame)
			if strings.Contains(text, `"auth_failed"`) || strings.Contains(text, `"auth_timeout"`) {
				// Reconnecting with the same rejected key thrashes all day
				// with zero data on an unattended capture (review #2).
				return fatal(fmt.Errorf("authentication rejected (check MASSIVE_API_KEY): %s", text))
			}
			if strings.Contains(text, `"auth_success"`) {
				authed = true
				for _, msg := range subscribeMessages(opt.Symbols, 50) {
					if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
						return fmt.Errorf("send subscribe: %w", err)
					}
				}
				opt.Log("authenticated; subscribed %d symbols (T+Q)", len(opt.Symbols))
			}
		}
	}
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

// subscribeMessages chunks the T.+Q. topic list into subscribe actions.
func subscribeMessages(symbols []string, chunk int) []string {
	var topics []string
	for _, s := range symbols {
		topics = append(topics, "T."+s, "Q."+s)
	}
	var msgs []string
	for i := 0; i < len(topics); i += chunk {
		end := i + chunk
		if end > len(topics) {
			end = len(topics)
		}
		msgs = append(msgs, fmt.Sprintf(`{"action":"subscribe","params":"%s"}`, strings.Join(topics[i:end], ",")))
	}
	return msgs
}

// wire structs — field names per docs/foundations/data.md. Trade "c" is an
// int array; quote "c" is a single int on the wire (flat files join multiple
// with commas) — decoded via RawMessage to tolerate either shape.
type wsTrade struct {
	Sym  string  `json:"sym"`
	X    int32   `json:"x"`
	P    float64 `json:"p"`
	S    float64 `json:"s"`
	C    []int32 `json:"c"`
	T    int64   `json:"t"`  // SIP ts, ms
	Pt   int64   `json:"pt"` // participant ts, ms (absent from vendor client structs; we parse raw)
	Q    int64   `json:"q"`
	Z    int32   `json:"z"`
	Trfi int64   `json:"trfi"`
	Trft int64   `json:"trft"`
}

type wsQuote struct {
	Sym string          `json:"sym"`
	Bx  int32           `json:"bx"`
	Bp  float64         `json:"bp"`
	Bs  int64           `json:"bs"`
	Ax  int32           `json:"ax"`
	Ap  float64         `json:"ap"`
	As  int64           `json:"as"`
	C   json.RawMessage `json:"c"` // int or [int]
	T   int64           `json:"t"` // ms
	Q   int64           `json:"q"`
	Z   int32           `json:"z"`
}

// decodeFrame parses one websocket frame (a JSON array of events) and
// submits T/Q events to the pipeline. Shared verbatim by live and capture
// replay — replay exercises the real decoder (mini-spec 1.2). Non-data
// events (status, our _capture controls) count as Status. Decode failures
// count, never crash: the frame is already safely in capture.
func decodeFrame(frame []byte, table *ingest.Table, p *ingest.Pipeline, stats *LiveStats) {
	var events []json.RawMessage
	if err := json.Unmarshal(frame, &events); err != nil {
		stats.DecodeErrs++
		return
	}
	var msg ingest.Msg
	for _, raw := range events {
		var head struct {
			Ev string `json:"ev"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			stats.DecodeErrs++
			continue
		}
		switch head.Ev {
		case "T":
			var t wsTrade
			if err := json.Unmarshal(raw, &t); err != nil {
				stats.DecodeErrs++
				continue
			}
			st := table.Lookup(t.Sym)
			if st == nil {
				stats.Unknown++
				continue
			}
			msg.Kind = ingest.KindTrade
			tr := &msg.Trade
			tr.State = st
			tr.Price = t.P
			tr.Size = t.S
			tr.SipTs = t.T * 1_000_000 // ms → ns (canonical; types.go)
			tr.PartTs = t.Pt * 1_000_000
			tr.Seq = t.Q
			tr.Exchange = t.X
			tr.Tape = t.Z
			tr.NCond = 0
			for _, c := range t.C {
				if tr.NCond == ingest.MaxConditions {
					p.CondOverflow.Add(1)
					break
				}
				tr.Cond[tr.NCond] = c
				tr.NCond++
			}
			p.Submit(msg)
			stats.Trades++
		case "Q":
			var q wsQuote
			if err := json.Unmarshal(raw, &q); err != nil {
				stats.DecodeErrs++
				continue
			}
			st := table.Lookup(q.Sym)
			if st == nil {
				stats.Unknown++
				continue
			}
			msg.Kind = ingest.KindQuote
			qu := &msg.Quote
			qu.State = st
			qu.BidPrice = q.Bp
			qu.AskPrice = q.Ap
			qu.BidSize = q.Bs
			qu.AskSize = q.As
			qu.BidExch = q.Bx
			qu.AskExch = q.Ax
			qu.SipTs = q.T * 1_000_000
			qu.Seq = q.Q
			qu.NCond = decodeQuoteCond(q.C, &qu.Cond)
			p.Submit(msg)
			stats.Quotes++
		default:
			stats.Status++
		}
	}
}

// decodeQuoteCond tolerates both wire shapes for the quote condition field:
// a bare int ("c":1) or an array ("c":[1,81]).
func decodeQuoteCond(raw json.RawMessage, out *[ingest.MaxConditions]int32) int {
	if len(raw) == 0 {
		return 0
	}
	var one int32
	if err := json.Unmarshal(raw, &one); err == nil {
		out[0] = one
		return 1
	}
	var many []int32
	if err := json.Unmarshal(raw, &many); err == nil {
		n := 0
		for _, c := range many {
			if n == len(out) {
				break
			}
			out[n] = c
			n++
		}
		return n
	}
	return 0
}
