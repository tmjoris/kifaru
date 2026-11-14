// Usage: copy load/tokens.json next to where you run it (it reads ./tokens.json), then
//   go run ./load/rawdl -n 1000 -limit 500 -dur 90s
//
// A light-weight dashboard crowd: it downloads and discards compressed bodies, with no
// decompression or parsing, so the test machine's CPU is not the limit.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type tok struct{ Token, Role, Inst string }

func main() {
	n := flag.Int("n", 1000, "virtual users")
	base := flag.String("base", "https://kifaru-api.onrender.com", "")
	limit := flag.Int("limit", 500, "history rows")
	dur := flag.Duration("dur", 90*time.Second, "")
	flag.Parse()
	raw, _ := os.ReadFile("tokens.json")
	var toks []tok
	_ = json.Unmarshal(raw, &toks)
	tr := &http.Transport{MaxIdleConnsPerHost: *n, MaxConnsPerHost: 0, DisableCompression: true, ReadBufferSize: 64 << 10}
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	var bytes, ok, fail atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration
	stop := time.Now().Add(*dur)
	var wg sync.WaitGroup
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := toks[i%len(toks)]
			time.Sleep(time.Duration(rand.Int63n(int64(10 * time.Second))))
			for time.Now().Before(stop) {
				req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/history?institution=%s&limit=%d", *base, t.Inst, *limit), nil)
				req.Header.Set("Authorization", "Bearer "+t.Token)
				req.Header.Set("Accept-Encoding", "gzip")
				start := time.Now()
				resp, err := client.Do(req)
				if err == nil {
					var c int64
					c, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					bytes.Add(c)
					if resp.StatusCode != 200 {
						err = fmt.Errorf("status %d", resp.StatusCode)
					}
				}
				if err != nil {
					fail.Add(1)
				} else {
					ok.Add(1)
					mu.Lock()
					lat = append(lat, time.Since(start))
					mu.Unlock()
				}
				time.Sleep(time.Duration(5+rand.Intn(10)) * time.Second)
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration { return lat[int(float64(len(lat)-1)*q)].Round(time.Millisecond) }
	fmt.Printf("raw crowd n=%d limit=%d: ok=%d failed=%d  data=%.0f MB (%.0f Mbit/s avg)  latency p50 %v p95 %v p99 %v max %v\n",
		*n, *limit, ok.Load(), fail.Load(), float64(bytes.Load())/1e6, float64(bytes.Load())*8/1e6/dur.Seconds(), p(.5), p(.95), p(.99), lat[len(lat)-1].Round(time.Millisecond))
}
