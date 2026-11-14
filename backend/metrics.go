package main

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// serverMetrics counts what the server itself experiences, so a load test can be
// read from the inside: was the process short of CPU, memory or database
// connections, or was the slowness somewhere else?
type serverMetrics struct {
	started  time.Time
	inflight atomic.Int64
	requests atomic.Uint64
	slow     atomic.Uint64 // requests that took over a second inside the handler
	shed     atomic.Uint64 // 503s the server chose to send
}

var metrics = serverMetrics{started: time.Now()}

// observe wraps a handler with request counting. It measures handler time only,
// which is the point: time spent queued in front of the server is not in it.
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.inflight.Add(1)
		metrics.requests.Add(1)
		start := time.Now()
		defer func() {
			metrics.inflight.Add(-1)
			if time.Since(start) > time.Second {
				metrics.slow.Add(1)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cgroupCPUStat reads how much the container has been held back by its CPU quota.
func cgroupCPUStat() map[string]int64 {
	out := map[string]int64{}
	raw, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if value, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			out[fields[0]] = value
		}
	}
	return out
}

func (a *App) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	pool := a.db.Stat()
	cpu := cgroupCPUStat()
	used, _ := readNumber("/sys/fs/cgroup/memory.current")
	a.cache.mu.Lock()
	cacheEntries := len(a.cache.entries)
	a.cache.mu.Unlock()
	a.sessions.mu.Lock()
	sessionEntries := len(a.sessions.entries)
	a.sessions.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime_seconds": int(time.Since(metrics.started).Seconds()),
		"requests": map[string]any{
			"total": metrics.requests.Load(), "in_flight": metrics.inflight.Load(),
			"slow_over_1s": metrics.slow.Load(), "shed_503": metrics.shed.Load(), "login_slots_busy": len(loginSlots), "ingest_slots_busy": len(ingestSlots),
		},
		"database_pool": map[string]any{
			"max": pool.MaxConns(), "open": pool.TotalConns(), "busy": pool.AcquiredConns(), "idle": pool.IdleConns(),
			// If acquire_wait_seconds grows under load, requests are queueing for a connection.
			"acquires": pool.AcquireCount(), "acquires_that_waited": pool.EmptyAcquireCount(),
			"acquire_wait_seconds": pool.AcquireDuration().Seconds(), "canceled_while_waiting": pool.CanceledAcquireCount(),
		},
		"streams": map[string]any{
			"open": a.hub.audience(), "max": maxStreams(), "turned_away": a.hub.rejected.Load(), "cut_loose": a.hub.dropped.Load(),
		},
		"caches": map[string]any{
			"responses": cacheEntries, "hits": a.cache.hits.Load(), "misses": a.cache.misses.Load(), "sessions": sessionEntries,
		},
		"memory": map[string]any{
			"container_used_mib": used >> 20, "container_limit_mib": cgroupMemoryBytes() >> 20,
			"go_heap_mib": mem.HeapAlloc >> 20, "go_sys_mib": mem.Sys >> 20, "gc_cycles": mem.NumGC,
			"gc_pause_total_ms": mem.PauseTotalNs / 1e6, "goroutines": runtime.NumGoroutine(),
		},
		// throttled_usec is time the container was stopped by its CPU quota. If it
		// climbs during a slow test, the platform CPU limit is the bottleneck.
		"cpu": map[string]any{
			"gomaxprocs": runtime.GOMAXPROCS(0), "quota_cores": cgroupCPUs(),
			"used_usec": cpu["usage_usec"], "periods": cpu["nr_periods"], "throttled_periods": cpu["nr_throttled"],
			"throttled_usec": cpu["throttled_usec"],
		},
	})
}
