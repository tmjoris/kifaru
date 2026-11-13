package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"hash/crc32"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// responseCache keeps finished JSON responses for a short time and makes
// concurrent requests for the same thing share one database query.
//
// Why it exists: a dashboard load costs a 2,000-row join, and every open tab
// used to run it on every stream event. With this cache the query runs at most
// once per key per TTL no matter how many people ask, a stale copy is served
// while a refresh runs, and the compressed bytes are built once per refresh
// rather than once per request.
type responseCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	flights map[string]*cacheFlight
	gen     atomic.Uint64
	hits    atomic.Uint64
	misses  atomic.Uint64
}

type cacheEntry struct {
	body []byte
	gz   []byte
	etag string
	at   time.Time
	gen  uint64
}

type cacheFlight struct {
	done  chan struct{}
	entry *cacheEntry
	err   error
}

const (
	maxCacheEntries  = 512
	compressMinBytes = 2048
	// A stale entry may be served this many TTLs past its expiry while a fresh
	// copy is being built.
	staleTTLs = 5
)

// invalidate makes every cached response stale at once. Call it after a change
// a person just made and expects to see straight away. High-volume report
// ingestion deliberately does not call it: those responses age out by TTL.
func (c *responseCache) invalidate() { c.gen.Add(1) }

// etagTable is CRC-32C, which modern CPUs compute in hardware. An ETag only has
// to notice that the body changed; a cryptographic hash of a 2 MB body cost
// more CPU than the query that produced it.
var etagTable = crc32.MakeTable(crc32.Castagnoli)

func buildEntry(body []byte, gen uint64) *cacheEntry {
	etag := `"` + strconv.FormatUint(uint64(crc32.Checksum(body, etagTable)), 36) + "-" + strconv.Itoa(len(body)) + `"`
	entry := &cacheEntry{body: body, etag: etag, at: time.Now(), gen: gen}
	if len(body) >= compressMinBytes {
		var buffer bytes.Buffer
		writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
		_, _ = writer.Write(body)
		if writer.Close() == nil && buffer.Len() < len(body) {
			entry.gz = buffer.Bytes()
		}
	}
	return entry
}

// serve answers from cache, or builds the response once and shares it.
func (c *responseCache) serve(w http.ResponseWriter, r *http.Request, key string, ttl time.Duration,
	build func(context.Context) ([]byte, error)) {
	now := time.Now()
	gen := c.gen.Load()
	c.mu.Lock()
	if c.entries == nil {
		c.entries, c.flights = map[string]*cacheEntry{}, map[string]*cacheFlight{}
	}
	entry := c.entries[key]
	if entry != nil && entry.gen == gen && now.Sub(entry.at) < ttl {
		c.mu.Unlock()
		c.hits.Add(1)
		writeCached(w, r, entry)
		return
	}
	if entry != nil && entry.gen == gen && now.Sub(entry.at) < ttl*staleTTLs && c.flights[key] == nil {
		// Expired but not too old: answer from the stale copy right away and
		// refresh it in the background, so nobody waits on the database.
		flight := &cacheFlight{done: make(chan struct{})}
		c.flights[key] = flight
		c.mu.Unlock()
		c.hits.Add(1)
		go c.refresh(key, gen, flight, build)
		writeCached(w, r, entry)
		return
	}
	if flight := c.flights[key]; flight != nil {
		// Someone is already refreshing this key. Serve what we have if it is
		// not too old, otherwise wait for the refresh.
		if entry != nil && entry.gen == gen && now.Sub(entry.at) < ttl*staleTTLs {
			c.mu.Unlock()
			c.hits.Add(1)
			writeCached(w, r, entry)
			return
		}
		c.mu.Unlock()
		select {
		case <-flight.done:
		case <-r.Context().Done():
			return
		}
		if flight.err != nil {
			writeError(w, http.StatusInternalServerError, flight.err.Error())
			return
		}
		writeCached(w, r, flight.entry)
		return
	}
	flight := &cacheFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()
	c.misses.Add(1)
	c.refresh(key, gen, flight, build)
	if flight.err != nil {
		writeError(w, http.StatusInternalServerError, flight.err.Error())
		return
	}
	writeCached(w, r, flight.entry)
}

// refresh runs the build for a key and publishes the result to everyone
// waiting on the flight. The build must not die with the first requester's
// connection, so it has its own deadline.
func (c *responseCache) refresh(key string, gen uint64, flight *cacheFlight, build func(context.Context) ([]byte, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	body, err := build(ctx)
	cancel()
	c.mu.Lock()
	delete(c.flights, key)
	if err == nil {
		flight.entry = buildEntry(body, gen)
		c.entries[key] = flight.entry
		if len(c.entries) > maxCacheEntries {
			c.evictLocked(time.Now())
		}
	}
	flight.err = err
	c.mu.Unlock()
	close(flight.done)
}

func (c *responseCache) evictLocked(now time.Time) {
	for key, entry := range c.entries {
		if now.Sub(entry.at) > time.Minute {
			delete(c.entries, key)
		}
	}
	for key := range c.entries {
		if len(c.entries) <= maxCacheEntries/2 {
			break
		}
		delete(c.entries, key)
	}
}

func writeCached(w http.ResponseWriter, r *http.Request, entry *cacheEntry) {
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("ETag", entry.etag)
	// private + no-cache: shared caches must not keep it, browsers may keep it
	// but must revalidate, and a revalidation that matches costs almost nothing.
	header.Set("Cache-Control", "private, no-cache")
	header.Add("Vary", "Accept-Encoding")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, entry.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := entry.body
	if entry.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body = entry.gz
		header.Set("Content-Encoding", "gzip")
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
