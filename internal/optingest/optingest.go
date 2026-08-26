// Package optingest is the typed pipeline for the Unusual Whales options
// tape (mini-spec 7.2) — the options twin of internal/ingest. One producer
// (live decode hook or replay), one consumer goroutine, an Observer seam
// for the bucket store. Deliberately independent of internal/ingest: the
// equity hot path stays untouched (plan §architecture).
package optingest

import (
	"sync/atomic"
)

// OptionTrade carries exactly the fields downstream stories consume (R2).
// Everything else on the wire stays in capture and can be re-decoded later.
type OptionTrade struct {
	ID           [16]byte // print uuid (dedupe key)
	Underlying   string   // underlying_symbol
	OptionSymbol string   // OSI, verification/debugging only
	ExecNs       int64    // executed_at ms → ns
	Expiry       string   // YYYY-MM-DD as on the wire; classifier parses+caches
	IsCall       bool
	Strike       float64
	Size         int64
	Price        float64
	Premium      float64 // vendor field verbatim — never recomputed (multiplier trap)
	OpenInterest int64   // prior-close OI (OCC nightly)
	UPrice       float64 // underlying price at execution
	UPriceOK     bool    // false when the wire sent "" (index names, D17)
	NbboBid      float64
	NbboAsk      float64
	TradeCode    string // raw OPRA condition string; sweep policy is 7.3's
	SideTag      string // "ask_side"|"bid_side"|"mid_side"|"no_side"|"" — raw tag, sign policy is 7.3's
}

// Observer receives each deduplicated print on the single consumer
// goroutine — same contract as ingest.Observer.
type Observer interface {
	ObserveOptionTrade(t *OptionTrade)
}

// DefaultQueueSize covers the observed worst second (5,649 prints at
// 09:30:01, 7.0) with two orders of magnitude of headroom, matching the
// equity pipeline's never-drop posture.
const DefaultQueueSize = 500_000

// dedupeWindow is the sliding-window size (R3): the last 2^20 print ids —
// ~30+ minutes at peak rate, far beyond any reconnect re-delivery span,
// at a bounded ~80MB instead of a session-lifetime map.
const dedupeWindow = 1 << 20

// Pipeline is the bounded queue between the frame source and the observer.
// Submit blocks (never drops); Run consumes until Close.
type Pipeline struct {
	ch  chan OptionTrade
	obs Observer

	// Dedupe ring: ids in arrival order; when full, the oldest id leaves
	// the set as a new one enters. Single-goroutine access (Submit side).
	seen    map[[16]byte]struct{}
	ring    [][16]byte
	ringPos int

	Processed     atomic.Int64
	Dupes         atomic.Int64
	MaxQueueDepth atomic.Int64
}

// NewPipeline sizes the queue (0 = DefaultQueueSize).
func NewPipeline(queueSize int) *Pipeline {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	return &Pipeline{
		ch:   make(chan OptionTrade, queueSize),
		seen: make(map[[16]byte]struct{}, dedupeWindow),
		ring: make([][16]byte, dedupeWindow),
	}
}

// SetObserver wires the consumer-side observer. Must be called before Run
// starts (same contract as ingest.Pipeline).
func (p *Pipeline) SetObserver(o Observer) { p.obs = o }

// Submit enqueues one print after the dedupe window check. Caller must be
// a single goroutine (the decode path — live hook or replay). Blocks when
// the queue is full: backpressure, never loss.
func (p *Pipeline) Submit(t OptionTrade) {
	if _, dup := p.seen[t.ID]; dup {
		p.Dupes.Add(1)
		return
	}
	// Ring eviction: overwrite the slot's previous occupant.
	old := p.ring[p.ringPos]
	if old != ([16]byte{}) {
		delete(p.seen, old)
	}
	p.ring[p.ringPos] = t.ID
	p.seen[t.ID] = struct{}{}
	p.ringPos = (p.ringPos + 1) % dedupeWindow

	if d := int64(len(p.ch)); d > p.MaxQueueDepth.Load() {
		p.MaxQueueDepth.Store(d)
	}
	p.ch <- t
}

// Close ends the stream; Run returns after draining.
func (p *Pipeline) Close() { close(p.ch) }

// Run consumes until Close, calling the observer for every print. Single
// consumer goroutine — observers need no locking of their own.
func (p *Pipeline) Run() {
	for t := range p.ch {
		if p.obs != nil {
			p.obs.ObserveOptionTrade(&t)
		}
		p.Processed.Add(1)
	}
}
