package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// sessionCache keeps verified sessions in memory so most requests do not touch
// the database to prove who is calling.
//
// Before this, every authenticated request ran a three-table join and then an
// UPDATE to record last_seen_at: two serial round trips, one of them a write on
// a row that every request from the same user rewrote. Now the join runs at
// most once per sessionCacheTTL per session, and last-seen times are written in
// one batched UPDATE every sessionTouchEvery.
//
// The trade-off: a user deactivated by an administrator can keep using an
// already-verified session for up to sessionCacheTTL. Sign-out takes effect
// immediately because it drops the entry.
type sessionCache struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
	touched map[string]struct{}
}

type sessionEntry struct {
	user    AuthUser
	checked time.Time
}

const (
	sessionCacheTTL    = 30 * time.Second
	sessionTouchEvery  = 30 * time.Second
	maxCachedSessions  = 50000
	sessionTouchChunk  = 5000
	sessionTouchBudget = 20 * time.Second
)

func (c *sessionCache) get(hash string, now time.Time) (AuthUser, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[hash]
	if !ok || now.Sub(entry.checked) >= sessionCacheTTL || !now.Before(entry.user.ExpiresAt) {
		return AuthUser{}, false
	}
	c.touchLocked(hash)
	return entry.user, true
}

func (c *sessionCache) put(hash string, user AuthUser, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]sessionEntry{}
	}
	if len(c.entries) >= maxCachedSessions {
		for key, entry := range c.entries {
			if now.Sub(entry.checked) >= sessionCacheTTL {
				delete(c.entries, key)
			}
		}
		for key := range c.entries {
			if len(c.entries) < maxCachedSessions/2 {
				break
			}
			delete(c.entries, key)
		}
	}
	c.entries[hash] = sessionEntry{user: user, checked: now}
	c.touchLocked(hash)
}

func (c *sessionCache) drop(hash string) {
	c.mu.Lock()
	delete(c.entries, hash)
	delete(c.touched, hash)
	c.mu.Unlock()
}

func (c *sessionCache) touchLocked(hash string) {
	if c.touched == nil {
		c.touched = map[string]struct{}{}
	}
	c.touched[hash] = struct{}{}
}

func (c *sessionCache) takeTouched() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.touched) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(c.touched))
	for hash := range c.touched {
		hashes = append(hashes, hash)
	}
	c.touched = map[string]struct{}{}
	return hashes
}

// runSessionFlusher writes last_seen_at for every session used since the last
// pass, in one statement per chunk instead of one UPDATE per request.
func (a *App) runSessionFlusher(ctx context.Context) {
	ticker := time.NewTicker(sessionTouchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.flushSessionTouches(ctx)
		}
	}
}

func (a *App) flushSessionTouches(ctx context.Context) {
	hashes := a.sessions.takeTouched()
	for start := 0; start < len(hashes); start += sessionTouchChunk {
		end := min(start+sessionTouchChunk, len(hashes))
		flushCtx, cancel := context.WithTimeout(ctx, sessionTouchBudget)
		_, err := a.db.Exec(flushCtx, `UPDATE auth_sessions SET last_seen_at=NOW()
			WHERE token_hash = ANY($1)`, hashes[start:end])
		cancel()
		if err != nil {
			log.Printf("session last-seen flush failed: %v", err)
			return
		}
	}
}
