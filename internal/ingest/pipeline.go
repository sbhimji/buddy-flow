package ingest

import (
	"sync/atomic"
)

// CaptureWriter is the 1.2 seam. The live feeder hands every raw vendor
// frame here BEFORE decoding (capture-before-parse: a parser fault can never
// lose data). 1.1 ships only NopCapture; story 1.2 implements the real
// per-session append files. File feeders replay data that is already on
// disk, so they use NopCapture by construction.
type CaptureWriter interface {
	Append(raw []byte) error
}

// NopCapture discards frames (replay sources; tests).
type NopCapture struct{}

func (NopCapture) Append([]byte) error { return nil }

// DefaultQueueSize buffers ~15s of the 120k msg/sec floor (~2M messages,
// ~500MB at Msg size). Sized so that even a pathological multi-second
// consumer stall cannot force a drop; the queue-depth counter tells us if
// we ever get near that (1.5 monitoring).
const DefaultQueueSize = 2_000_000

// Pipeline is the bounded queue plus the single consumer goroutine that
// owns all SymbolState writes. Submit blocks when the queue is full —
// backpressure, never drops: the feeder (and ultimately the vendor's TCP
// window) slows down instead. Latency may degrade; data may not vanish.
type Pipeline struct {
	table *Table
	queue chan Msg

	// Monitoring counters (atomics: readable live without blocking).
	Processed     atomic.Int64 // messages fully applied
	MaxQueueDepth atomic.Int64 // high-water mark, sampled at submit
	CondOverflow  atomic.Int64 // messages whose condition list exceeded maxConditions
}

// NewPipeline builds a pipeline over the symbol table. queueSize <= 0 uses
// DefaultQueueSize.
func NewPipeline(table *Table, queueSize int) *Pipeline {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	return &Pipeline{table: table, queue: make(chan Msg, queueSize)}
}

// Table exposes the symbol table (dev view, tests, spot checks).
func (p *Pipeline) Table() *Table { return p.table }

// Submit enqueues one message. Blocks on a full queue (backpressure).
// Called by feeder goroutines; safe for multiple concurrent feeders
// (trades and quotes files stream in parallel).
func (p *Pipeline) Submit(m Msg) {
	if d := int64(len(p.queue)); d > p.MaxQueueDepth.Load() {
		p.MaxQueueDepth.Store(d) // benign race: approximate high-water mark
	}
	p.queue <- m
}

// Close signals that no further Submit calls will happen. Run drains the
// queue and returns.
func (p *Pipeline) Close() { close(p.queue) }

// Run consumes until Close, applying every message to its symbol state.
// Exactly one Run goroutine may exist per pipeline — SymbolState's
// concurrency model depends on it.
func (p *Pipeline) Run() {
	for m := range p.queue {
		switch m.Kind {
		case KindTrade:
			m.Trade.State.applyTrade(&m.Trade)
		case KindQuote:
			m.Quote.State.applyQuote(&m.Quote)
		}
		p.Processed.Add(1)
	}
}
