// verify-feed: go/no-go smoke test for the Massive stocks websocket.
//
// Checks exactly three things:
//   1. Connectivity  — the real-time socket connects and messages flow
//   2. Entitlement   — auth + subscriptions accepted (gateway status messages)
//   3. Latency       — SIP timestamp -> local receipt, against the 5s budget
//
// Deeper data analysis (condition codes, TRF share, field coverage) belongs on
// historical flat files, not here.
//
// Usage:
//   cd discovery-scripts/verify-feed && go run . [-ticker NVDA] [-duration 5m]
//   (MASSIVE_API_KEY from env or ../../.env)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/joho/godotenv"
	massivews "github.com/massive-com/client-go/v3/websocket"
	"github.com/sirupsen/logrus"
)

type stats struct {
	trades, quotes int64
	statusMsgs     []string
	latencyMs      []float64
}

func (s *stats) record(raw json.RawMessage) {
	if len(raw) > 0 && raw[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(raw, &batch) == nil {
			for _, r := range batch {
				s.recordOne(r)
			}
			return
		}
	}
	s.recordOne(raw)
}

func (s *stats) recordOne(raw json.RawMessage) {
	var m struct {
		Ev string  `json:"ev"`
		T  float64 `json:"t"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	switch m.Ev {
	case "status":
		if len(s.statusMsgs) < 20 {
			s.statusMsgs = append(s.statusMsgs, string(raw))
		}
		return
	case "T":
		s.trades++
	case "Q":
		s.quotes++
	}
	if m.T > 0 {
		s.latencyMs = append(s.latencyMs, float64(time.Now().UnixMilli())-m.T)
	}
}

func main() {
	ticker := flag.String("ticker", "NVDA", "symbol to subscribe")
	duration := flag.Duration("duration", 5*time.Minute, "how long to run")
	envFile := flag.String("env", "../../.env", "path to .env file with MASSIVE_API_KEY")
	flag.Parse()

	_ = godotenv.Load(*envFile) // existing env vars win; missing file is fine
	if os.Getenv("MASSIVE_API_KEY") == "" {
		fmt.Fprintf(os.Stderr, "MASSIVE_API_KEY not set and not found in %s\n", *envFile)
		os.Exit(2)
	}

	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)

	c, err := massivews.New(massivews.Config{
		APIKey:  os.Getenv("MASSIVE_API_KEY"),
		Feed:    massivews.RealTime,
		Market:  massivews.Stocks,
		RawData: true,
		Log:     log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		os.Exit(1)
	}
	defer c.Close()

	for _, topic := range []massivews.Topic{massivews.StocksTrades, massivews.StocksQuotes} {
		if err := c.Subscribe(topic, *ticker); err != nil {
			fmt.Fprintln(os.Stderr, "subscribe:", err)
			os.Exit(1)
		}
	}
	if err := c.Connect(); err != nil {
		fmt.Fprintln(os.Stderr, "connect FAILED (connectivity/entitlement):", err)
		os.Exit(1)
	}
	fmt.Printf("Connected. Watching %s (trades+quotes) for %s ...\n", *ticker, *duration)

	s := &stats{}
	deadline := time.After(*duration)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-tick.C:
			fmt.Printf("  ... trades=%d quotes=%d\n", s.trades, s.quotes)
		case err := <-c.Error():
			fmt.Fprintln(os.Stderr, "stream error:", err)
			break loop
		case out, more := <-c.Output():
			if !more {
				break loop
			}
			if raw, ok := out.(json.RawMessage); ok {
				s.record(raw)
			}
		}
	}

	fmt.Println("\n===== RESULTS:", *ticker, "=====")
	fmt.Println("\nGateway/status messages (auth + subscription acks = entitlement):")
	for _, m := range s.statusMsgs {
		fmt.Println(" ", m)
	}
	fmt.Printf("\nMessages: trades=%d quotes=%d\n", s.trades, s.quotes)
	if len(s.latencyMs) > 0 {
		sort.Float64s(s.latencyMs)
		p := func(q float64) float64 { return s.latencyMs[int(q*float64(len(s.latencyMs)-1))] }
		fmt.Printf("SIP->receipt latency ms: p50=%.0f p95=%.0f p99=%.0f max=%.0f  (budget: 5000 print->pixel)\n",
			p(.5), p(.95), p(.99), s.latencyMs[len(s.latencyMs)-1])
	}

	if s.trades > 0 && s.quotes > 0 {
		fmt.Println("\nRESULT: OK — connected, entitled, streaming")
		return
	}
	fmt.Println("\nRESULT: INCOMPLETE — no trades or no quotes (market closed? entitlement? see status messages above)")
	os.Exit(1)
}
