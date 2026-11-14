package main

import (
	"testing"
	"time"
)

func TestSessionCacheExpiresAndDropsEntries(t *testing.T) {
	var c sessionCache
	now := time.Now()
	user := AuthUser{Email: "a@bank", ExpiresAt: now.Add(time.Hour)}
	if _, ok := c.get("h", now); ok {
		t.Fatal("an empty cache must miss")
	}
	c.put("h", user, now)
	if got, ok := c.get("h", now.Add(sessionCacheTTL-time.Second)); !ok || got.Email != "a@bank" {
		t.Error("an entry inside the TTL must hit")
	}
	if _, ok := c.get("h", now.Add(sessionCacheTTL+time.Second)); ok {
		t.Error("an entry past the TTL must miss, so the database is asked again")
	}
	c.put("h", user, now)
	c.drop("h")
	if _, ok := c.get("h", now); ok {
		t.Error("signing out must drop the cached session at once")
	}
	c.put("short", AuthUser{ExpiresAt: now.Add(5 * time.Second)}, now)
	if _, ok := c.get("short", now.Add(10*time.Second)); ok {
		t.Error("a session past its own expiry must miss even inside the TTL")
	}
}

func TestSessionCacheBatchesLastSeenUpdates(t *testing.T) {
	var c sessionCache
	now := time.Now()
	for _, hash := range []string{"a", "b", "a", "a"} {
		c.put(hash, AuthUser{ExpiresAt: now.Add(time.Hour)}, now)
		c.get(hash, now)
	}
	if hashes := c.takeTouched(); len(hashes) != 2 {
		t.Errorf("%d sessions to write, want 2: four requests become one update per session", len(hashes))
	}
	if hashes := c.takeTouched(); len(hashes) != 0 {
		t.Error("touched sessions must be cleared once taken")
	}
	c.put("c", AuthUser{ExpiresAt: now.Add(time.Hour)}, now)
	c.drop("c")
	if hashes := c.takeTouched(); len(hashes) != 0 {
		t.Error("a dropped session has nothing left to write")
	}
}

func TestSessionCacheStaysBounded(t *testing.T) {
	var c sessionCache
	now := time.Now()
	for i := 0; i < maxCachedSessions+500; i++ {
		c.put(string(rune(i))+"-"+time.Duration(i).String(), AuthUser{ExpiresAt: now.Add(time.Hour)}, now)
	}
	c.mu.Lock()
	size := len(c.entries)
	c.mu.Unlock()
	if size > maxCachedSessions {
		t.Errorf("cache holds %d sessions, want at most %d", size, maxCachedSessions)
	}
}
