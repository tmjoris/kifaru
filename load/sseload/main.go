// Opens N live-stream connections the way dashboard tabs do, holds them, and
// reports how many connected, were turned away (503) and how many frames arrived.
//
//   go run ./load/sseload -base https://kifaru-api.onrender.com -tokens tokens.json -n 10000 -rate 100
//
// tokens.json is a list of {"token":..., "role":..., "inst":...}; make it with
// load/make-tokens.mjs. Ramp slowly (-rate): opening thousands of TLS connections a
// second makes the client the bottleneck, not the server.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type tok struct {
	Token string `json:"token"`
	Role  string `json:"role"`
	Inst  string `json:"inst"`
}

func main() {
	n := flag.Int("n", 1000, "connections")
	base := flag.String("base", "http://127.0.0.1:8000", "")
	tokens := flag.String("tokens", "tokens.json", "")
	rate := flag.Int("rate", 400, "new connections per second")
	hold := flag.Duration("hold", 60*time.Second, "how long to hold after the last connect")
	flag.Parse()
	raw, _ := os.ReadFile(*tokens)
	var toks []tok
	_ = json.Unmarshal(raw, &toks)
	transport := &http.Transport{MaxIdleConns: 0, MaxConnsPerHost: 0, DisableCompression: true, ReadBufferSize: 4096, WriteBufferSize: 1024}
	client := &http.Client{Transport: transport}
	var connected, rejected, failed, ready, frames, dirty, batch atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration
	stop := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := toks[i%len(toks)]
			inst := t.Inst
			req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/stream?institution=%s", *base, inst), nil)
			req.Header.Set("Authorization", "Bearer "+t.Token)
			req.Header.Set("Accept", "text/event-stream")
			resp, err := client.Do(req)
			if err != nil {
				failed.Add(1)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == 503 {
				rejected.Add(1)
				return
			}
			if resp.StatusCode != 200 {
				failed.Add(1)
				return
			}
			connected.Add(1)
			r := bufio.NewReaderSize(resp.Body, 512)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				switch {
				case strings.HasPrefix(line, "event: ready"):
					ready.Add(1)
				case strings.HasPrefix(line, "event: batch"):
					batch.Add(1)
					frames.Add(1)
				case strings.HasPrefix(line, "event: dirty"):
					dirty.Add(1)
					frames.Add(1)
				case strings.HasPrefix(line, "data: {\"rows\""):
					if idx := strings.Index(line, "\"sent_at_ms\":"); idx > 0 {
						var ms int64
						fmt.Sscanf(line[idx+len("\"sent_at_ms\":"):], "%d", &ms)
						mu.Lock()
						lat = append(lat, time.Since(time.UnixMilli(ms)))
						mu.Unlock()
					}
				}
				select {
				case <-stop:
					return
				default:
				}
			}
		}(i)
		if *rate > 0 && i%*rate == *rate-1 {
			time.Sleep(time.Second)
			if rand.Intn(1) == 0 && (i+1)%(*rate*5) == 0 {
				fmt.Printf("  t=%3.0fs opened=%d connected=%d rejected=%d failed=%d\n", time.Since(start).Seconds(), i+1, connected.Load(), rejected.Load(), failed.Load())
			}
		}
	}
	fmt.Printf("all %d attempted in %.0fs: connected=%d rejected(503)=%d failed=%d\n", *n, time.Since(start).Seconds(), connected.Load(), rejected.Load(), failed.Load())
	time.Sleep(*hold)
	mu.Lock()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(float64(len(lat)-1)*q)]
	}
	_ = p
	fmt.Printf("held: connected=%d ready=%d rejected=%d failed=%d frames=%d (batch=%d dirty=%d)\n", connected.Load(), ready.Load(), rejected.Load(), failed.Load(), frames.Load(), batch.Load(), dirty.Load())
	mu.Unlock()
	close(stop)
	os.Exit(0)
}
