package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fetch(c *responseCache, key string, ttl time.Duration, build func(context.Context) ([]byte, error), header http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, values := range header {
		request.Header[name] = values
	}
	recorder := httptest.NewRecorder()
	c.serve(recorder, request, key, ttl, build)
	return recorder
}

func TestCacheRunsOneQueryForManyConcurrentRequests(t *testing.T) {
	var c responseCache
	var builds atomic.Int32
	build := func(context.Context) ([]byte, error) {
		builds.Add(1)
		time.Sleep(50 * time.Millisecond)
		return []byte(`{"ok":true}`), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := fetch(&c, "k", time.Minute, build, nil); got.Code != 200 || got.Body.String() != `{"ok":true}` {
				t.Errorf("status %d body %q", got.Code, got.Body.String())
			}
		}()
	}
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Errorf("the query ran %d times for 200 concurrent requests, want once", n)
	}
	fetch(&c, "k", time.Minute, build, nil)
	if builds.Load() != 1 {
		t.Error("a request inside the TTL must not run the query again")
	}
}

func TestCacheAnswersNotModifiedAndGzip(t *testing.T) {
	var c responseCache
	big := []byte(`{"rows":"` + strings.Repeat("abcdefgh", 1000) + `"}`)
	build := func(context.Context) ([]byte, error) { return big, nil }
	first := fetch(&c, "k", time.Minute, build, http.Header{"Accept-Encoding": {"gzip"}})
	if first.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("a client that accepts gzip should get the compressed body")
	}
	reader, err := gzip.NewReader(bytes.NewReader(first.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(reader); !bytes.Equal(plain, big) {
		t.Error("the gzip body does not decompress to the original")
	}
	if first.Body.Len() >= len(big)/4 {
		t.Errorf("gzip body is %d bytes for %d, expected a large saving", first.Body.Len(), len(big))
	}
	if plain := fetch(&c, "k", time.Minute, build, nil); plain.Header().Get("Content-Encoding") != "" || plain.Body.Len() != len(big) {
		t.Error("a client without gzip must get the plain body")
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if again := fetch(&c, "k", time.Minute, build, http.Header{"If-None-Match": {etag}}); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("revalidation with a matching ETag: status %d, body %d bytes; want 304 and no body", again.Code, again.Body.Len())
	}
	if other := fetch(&c, "k", time.Minute, build, http.Header{"If-None-Match": {`"stale"`}}); other.Code != 200 {
		t.Errorf("a different ETag must get the full body, got %d", other.Code)
	}
}

func TestCacheServesStaleWhileRefreshingInTheBackground(t *testing.T) {
	var c responseCache
	var version atomic.Int32
	release := make(chan struct{})
	build := func(context.Context) ([]byte, error) {
		n := version.Add(1)
		if n > 1 {
			<-release // the refresh is slow
		}
		return []byte{byte('0' + n)}, nil
	}
	ttl := 20 * time.Millisecond
	if got := fetch(&c, "k", ttl, build, nil).Body.String(); got != "1" {
		t.Fatalf("first body = %q", got)
	}
	time.Sleep(40 * time.Millisecond) // expired, but inside the stale window
	started := time.Now()
	got := fetch(&c, "k", ttl, build, nil).Body.String()
	if got != "1" || time.Since(started) > 500*time.Millisecond {
		t.Errorf("body %q after %v: an expired entry should be served at once while the refresh runs", got, time.Since(started))
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fetch(&c, "k", time.Minute, build, nil).Body.String() == "2" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the background refresh never replaced the stale body")
}

func TestCacheInvalidationForcesAFreshQuery(t *testing.T) {
	var c responseCache
	var builds atomic.Int32
	build := func(context.Context) ([]byte, error) { return []byte{byte('0' + builds.Add(1))}, nil }
	fetch(&c, "k", time.Minute, build, nil)
	c.invalidate() // a person just changed something
	if got := fetch(&c, "k", time.Minute, build, nil).Body.String(); got != "2" {
		t.Errorf("after invalidation the next request must see fresh data, got %q", got)
	}
}

func TestCacheDoesNotKeepErrors(t *testing.T) {
	var c responseCache
	var calls atomic.Int32
	build := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("database unavailable")
		}
		return []byte("fine"), nil
	}
	if got := fetch(&c, "k", time.Minute, build, nil); got.Code != http.StatusInternalServerError {
		t.Errorf("a failed build: status %d, want 500", got.Code)
	}
	if got := fetch(&c, "k", time.Minute, build, nil); got.Code != 200 || got.Body.String() != "fine" {
		t.Errorf("the next request must try again: %d %q", got.Code, got.Body.String())
	}
}

func TestCacheKeysDoNotShareResponses(t *testing.T) {
	var c responseCache
	for _, key := range []string{"history|bank_a|2000", "history|bank_b|2000"} {
		body := key
		got := fetch(&c, key, time.Minute, func(context.Context) ([]byte, error) { return []byte(body), nil }, nil).Body.String()
		if got != key {
			t.Errorf("key %q returned %q", key, got)
		}
	}
}
