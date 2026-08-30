package optbucket

import (
	"sync"
	"testing"

	"buddy-flow/internal/optingest"
)

// MO-4 F2: a reader that is not the feeder's peer (the equity render loop)
// hammers Window/Get/Bounds/Totals while ObserveOptionTrade writes. Run
// under -race; the store lock is the only thing standing between them.
func TestConcurrentReadersWhileWriting(t *testing.T) {
	s := newTestStore(t)
	const seconds = 600 // ten minutes of one-print seconds
	base := anchor(t, 0)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	syms := []string{"QQQ", "NVDA"}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			var sum Bucket
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				sym := syms[(i+r)%len(syms)]
				min := base/1_000_000_000 + int64(i%seconds)/60*60
				w := s.Window(sym, min, min+60)
				sum.Add(&w)
				if b := s.Get(sym, min); b != nil {
					b.Prints++ // a copy: mutating it must never touch the store
				}
				s.Bounds()
				s.Totals()
				_ = s.MaxSec.Load()
			}
		}(r)
	}

	for i := int64(0); i < seconds; i++ {
		for _, sym := range syms {
			s.ObserveOptionTrade(print(base+i*1_000_000_000, func(tr *optingest.OptionTrade) {
				tr.Underlying = sym
			}))
		}
	}
	close(stop)
	wg.Wait()

	prints, _ := s.Totals()
	if prints != int64(seconds*len(syms)) {
		t.Fatalf("prints = %d, want %d (a reader's Get copy leaked into the store?)", prints, seconds*len(syms))
	}
	if b := s.Get("QQQ", base/1_000_000_000); b == nil || b.Prints != 1 {
		t.Fatalf("Get after writes = %+v, want one print", b)
	}
	if w := s.Window("NVDA", base/1_000_000_000, base/1_000_000_000+60); w.Prints != 60 {
		t.Fatalf("first NVDA minute prints = %d, want 60", w.Prints)
	}
}
