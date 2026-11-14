package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// streamHub pushes changes to open dashboards instead of making every tab ask
// again.
//
// How a tab stays current:
//  1. It loads /v1/history once. The response carries a cursor: the position in
//     the event stream when that snapshot was read.
//  2. It opens /v1/stream with that cursor. The hub replays anything it missed
//     since, then pushes new changes as they happen.
//  3. A change arrives as the full history row, so the tab merges it into what
//     it already shows. Nothing is fetched again.
//
// Three things keep this affordable on a tenth of a CPU:
//   - Changed rows are collected and sent on a tick, one encoded frame per
//     topic that every subscriber shares. The tick lengthens as the audience
//     grows, because writing a frame to 10,000 sockets costs real CPU.
//   - If more rows change in one tick than are worth sending, the hub sends a
//     single "dirty" hint instead and the tab refetches the (cached) snapshot at
//     a random moment, so a burst of reports cannot turn into a refetch storm.
//   - A tab that reconnects is replayed from a ring of recent frames. If it has
//     been away longer than the ring remembers, it is told to reload.
//
// Holding a stream open must cost almost nothing, because 10,000 of them have to
// fit in 512 MB. Served by net/http, each cost about 36 KB (a goroutine, a
// buffered reader and writer, and the response state): 360 MB at 10,000. So a
// stream takes over its connection (Hijack) and keeps only the net.Conn. There
// is no goroutine and no buffer per stream; a few delivery workers write each
// shared frame to every connection, and a dead tab is noticed when a write or
// a heartbeat to it fails.
type streamHub struct {
	mu       sync.Mutex
	epoch    string
	seq      uint64
	subs     map[string]map[*subscriber]struct{}
	total    int
	ring     []ringFrame
	ringNext int
	ringSize int
	dirty    map[string]struct{}
	overflow bool
	outbox   chan deliveryJob
	rejected atomic.Uint64
	dropped  atomic.Uint64
}

// subscriber is one open stream. It is just a connection and a few fields.
type subscriber struct {
	topic   string
	conn    net.Conn
	expires time.Time // when the user's session ends
	slot    int       // which second of the heartbeat cycle this stream is pinged in
	closed  atomic.Bool
}

type deliveryJob struct {
	targets []*subscriber
	payload []byte // already chunk-encoded, shared by every target
}

type ringFrame struct {
	seq   uint64
	topic string
	frame []byte
}

const (
	ringCapacity     = 4096
	maxPushRows      = 120
	baseTick         = time.Second
	perSubscriberTax = 400 * time.Microsecond
	maxTick          = 10 * time.Second
	heartbeatEvery   = 25 * time.Second
	writeTimeout     = 2 * time.Second
	deliveryWorkers  = 4
	deliveryChunk    = 256
	outboxCapacity   = 2048
)

func (h *streamHub) init() {
	if h.subs == nil {
		h.subs = map[string]map[*subscriber]struct{}{}
		h.dirty = map[string]struct{}{}
		h.ring = make([]ringFrame, ringCapacity)
		h.epoch = randomHex(4)
		h.outbox = make(chan deliveryJob, outboxCapacity)
		go h.deliverLoop()
		go h.heartbeatLoop()
	}
}

// cursor is the stream position a snapshot taken now is consistent with.
func (h *streamHub) cursor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	return h.epoch + "-" + strconv.FormatUint(h.seq, 10)
}

func (h *streamHub) frameID() string { return h.epoch + "-" + strconv.FormatUint(h.seq, 10) }

// emitLocked stores a frame in the replay ring and queues it for delivery to
// the topic's subscribers. Nothing is written to a socket while the lock is held.
func (h *streamHub) emitLocked(topic, event string, payload []byte) {
	h.seq++
	frame := []byte(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", h.frameID(), event, payload))
	h.ring[h.ringNext] = ringFrame{seq: h.seq, topic: topic, frame: frame}
	h.ringNext = (h.ringNext + 1) % ringCapacity
	if h.ringSize < ringCapacity {
		h.ringSize++
	}
	if len(h.subs[topic]) == 0 {
		return
	}
	targets := make([]*subscriber, 0, len(h.subs[topic]))
	for sub := range h.subs[topic] {
		targets = append(targets, sub)
	}
	select {
	case h.outbox <- deliveryJob{targets: targets, payload: chunked(frame)}:
	default:
		// Delivery cannot keep up at all. Cut these tabs loose; they reconnect
		// with their last cursor and are replayed or told to reload.
		h.dropped.Add(uint64(len(targets)))
		for _, sub := range targets {
			go h.detach(sub)
		}
	}
}

// chunked wraps data as one HTTP/1.1 chunk. It is computed once per frame and
// shared by every subscriber.
func chunked(data []byte) []byte {
	out := make([]byte, 0, len(data)+12)
	out = strconv.AppendInt(out, int64(len(data)), 16)
	out = append(out, '\r', '\n')
	out = append(out, data...)
	return append(out, '\r', '\n')
}

// deliverLoop writes queued frames to connections, in order, a few workers wide.
func (h *streamHub) deliverLoop() {
	for job := range h.outbox {
		h.writeAll(job.targets, job.payload)
	}
}

// writeAll writes one payload to every target, split across workers so one slow
// client delays only its own shard. A connection that cannot take a write
// within the timeout is closed: its tab reconnects and is replayed.
func (h *streamHub) writeAll(targets []*subscriber, payload []byte) {
	shards := min(deliveryWorkers, (len(targets)+deliveryChunk-1)/deliveryChunk)
	if shards <= 1 {
		h.writeShard(targets, payload)
		return
	}
	var wg sync.WaitGroup
	per := (len(targets) + shards - 1) / shards
	for start := 0; start < len(targets); start += per {
		wg.Add(1)
		go func(part []*subscriber) {
			defer wg.Done()
			h.writeShard(part, payload)
		}(targets[start:min(start+per, len(targets))])
	}
	wg.Wait()
}

func (h *streamHub) writeShard(targets []*subscriber, payload []byte) {
	for _, sub := range targets {
		if sub.closed.Load() {
			continue
		}
		_ = sub.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := sub.conn.Write(payload); err != nil {
			h.detach(sub)
		}
	}
}

// heartbeatLoop pings each stream every heartbeatEvery, a slice of them each
// second, so 10,000 streams never write in the same instant. It also ends
// streams whose session has expired, and finds tabs that have gone away.
func (h *streamHub) heartbeatLoop() {
	ping := chunked([]byte(": keep-alive\n\n"))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		slot := int(time.Now().Unix() % int64(heartbeatEvery/time.Second))
		now := time.Now()
		var due, expired []*subscriber
		h.mu.Lock()
		for _, group := range h.subs {
			for sub := range group {
				switch {
				case !sub.expires.IsZero() && now.After(sub.expires):
					expired = append(expired, sub)
				case sub.slot == slot:
					due = append(due, sub)
				}
			}
		}
		h.mu.Unlock()
		for _, sub := range expired {
			h.detach(sub)
		}
		if len(due) > 0 {
			h.writeAll(due, ping)
		}
	}
}

// publish sends a small event (an alert, a demo event) to the named topics.
func (h *streamHub) publish(topics []string, event string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	for _, topic := range topics {
		h.emitLocked(topic, event, encoded)
	}
}

// markDirty records that a report's row changed. The flusher turns the set
// into one batched push on its next tick.
func (h *streamHub) markDirty(reportID string) {
	if reportID == "" || reportID == "<nil>" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if h.overflow {
		return
	}
	h.dirty[reportID] = struct{}{}
	if len(h.dirty) > maxPushRows {
		h.overflow = true
		h.dirty = map[string]struct{}{}
	}
}

func (h *streamHub) takeDirty() (ids []string, overflow bool, audience int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	overflow = h.overflow
	for id := range h.dirty {
		ids = append(ids, id)
	}
	h.dirty = map[string]struct{}{}
	h.overflow = false
	return ids, overflow, h.total
}

// tick is how long changes are gathered before one push. It grows with the
// audience because the cost of a broadcast does.
func (h *streamHub) tick() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return min(baseTick+time.Duration(h.total)*perSubscriberTax, maxTick)
}

// subscribe prepares a stream: it works out what the tab missed since `since`
// and returns it, but does not make the stream live yet. The caller writes the
// ready event and the replay to the connection, then calls attach.
func (h *streamHub) subscribe(topic, since string, expires time.Time) (sub *subscriber, replay [][]byte, reset bool, cursor string, through uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	sub = &subscriber{topic: topic, expires: expires, slot: rand.IntN(int(heartbeatEvery / time.Second))}
	if since != "" {
		epoch, seqText, ok := strings.Cut(since, "-")
		seq, err := strconv.ParseUint(seqText, 10, 64)
		switch {
		case !ok || err != nil || epoch != h.epoch:
			reset = true // a different server run, or nonsense: start over
		case seq < h.seq:
			if seq < h.seq-uint64(h.ringSize) { // the frames it needs have been overwritten
				reset = true
			} else {
				replay, _ = h.framesAfterLocked(topic, seq)
			}
		}
	}
	return sub, replay, reset, h.epoch + "-" + strconv.FormatUint(h.seq, 10), h.seq
}

// framesAfterLocked returns the ring frames for a topic newer than seq, and the
// newest sequence number in the ring.
func (h *streamHub) framesAfterLocked(topic string, seq uint64) ([][]byte, uint64) {
	var frames [][]byte
	for i := 0; i < h.ringSize; i++ {
		entry := h.ring[(h.ringNext-h.ringSize+i+ringCapacity)%ringCapacity]
		if entry.seq > seq && entry.topic == topic {
			frames = append(frames, entry.frame)
		}
	}
	return frames, h.seq
}

// attach makes a prepared stream live. Frames emitted while the ready event and
// replay were being written are written first, so the tab never sees frames out
// of order or misses one. It returns false if the connection failed.
func (h *streamHub) attach(sub *subscriber, writtenThrough uint64, write func([]byte) bool) bool {
	for {
		h.mu.Lock()
		if h.seq-writtenThrough > uint64(h.ringSize) {
			h.mu.Unlock()
			return false // the ring wrapped while we were catching up: reconnect and reset
		}
		frames, latest := h.framesAfterLocked(sub.topic, writtenThrough)
		if len(frames) == 0 {
			if h.subs[sub.topic] == nil {
				h.subs[sub.topic] = map[*subscriber]struct{}{}
			}
			h.subs[sub.topic][sub] = struct{}{}
			h.total++
			h.mu.Unlock()
			return true
		}
		h.mu.Unlock()
		for _, frame := range frames {
			if !write(chunked(frame)) {
				return false
			}
		}
		writtenThrough = latest
	}
}

// detach removes a stream and closes its connection. It is safe to call twice.
func (h *streamHub) detach(sub *subscriber) {
	if !sub.closed.CompareAndSwap(false, true) {
		return
	}
	_ = sub.conn.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub.topic][sub]; !ok {
		return
	}
	delete(h.subs[sub.topic], sub)
	if len(h.subs[sub.topic]) == 0 {
		delete(h.subs, sub.topic)
	}
	h.total--
}

func (h *streamHub) audience() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// ---- admission: how many streams, and how much memory -----------------------------

func maxStreams() int { return envInt("STREAM_MAX", 12000) }

// memoryPressure is true when the container is close to its memory limit. New
// streams are refused then, because an out-of-memory kill would drop every
// connected user at once.
func memoryPressure() bool {
	limit := cgroupMemoryBytes()
	if limit <= 0 {
		return false
	}
	for _, path := range []string{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory/memory.usage_in_bytes"} {
		if used, err := readNumber(path); err == nil && used > 0 {
			return float64(used) > float64(limit)*0.88
		}
	}
	return false
}

// ---- the flusher --------------------------------------------------------------------

// runHubFlusher turns changed report ids into pushed rows on the hub's tick.
func (a *App) runHubFlusher(ctx context.Context) {
	for {
		timer := time.NewTimer(a.hub.tick())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		a.flushHub(ctx)
	}
}

func (a *App) flushHub(ctx context.Context) {
	ids, overflow, audience := a.hub.takeDirty()
	if len(ids) == 0 && !overflow {
		return
	}
	if overflow || audience == 0 {
		// Too many changes to send row by row, or nobody is listening. Either
		// way leave a marker, so a tab that replays across it reloads its snapshot.
		a.hub.publishDirty()
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rows, err := a.pushRows(pushCtx, ids)
	if err != nil {
		log.Printf("stream push: %v", err)
		a.hub.publishDirty()
		return
	}
	a.hub.publishRows(rows)
}

// publishDirty tells every topic's tabs that their snapshot is stale.
func (h *streamHub) publishDirty() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	topics := map[string]struct{}{"*": {}}
	for topic := range h.subs {
		topics[topic] = struct{}{}
	}
	for topic := range topics {
		h.emitLocked(topic, "dirty", []byte("{}"))
	}
}

type pushedRow struct {
	reporting, destination string
	full, redacted         []byte
}

// pushRows loads the current history row for each id, in one query. Each row
// comes back twice: whole, for the reporting institution and staff, and with
// the customer hash blanked, for the receiving institution.
func (a *App) pushRows(ctx context.Context, ids []string) ([]pushedRow, error) {
	rows, err := a.db.Query(ctx, `SELECT h.reporting_institution,h.destination_institution,
		to_json(h)::text, jsonb_set(to_jsonb(h),'{subject_customer_hash}','""')::text
		FROM (`+historySelect("r.subject_customer_hash")+`
		FROM (SELECT * FROM reports WHERE report_id = ANY($1)) r
		LEFT JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		LEFT JOIN alerts a ON a.alert_id=v.alert_id) h`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pushedRow
	for rows.Next() {
		var row pushedRow
		var full, redacted string
		if err := rows.Scan(&row.reporting, &row.destination, &full, &redacted); err != nil {
			return nil, err
		}
		row.full, row.redacted = []byte(full), []byte(redacted)
		out = append(out, row)
	}
	return out, rows.Err()
}

// publishRows sends one frame per topic holding every changed row that topic is
// allowed to see. The frame is encoded once and shared by all its subscribers.
func (h *streamHub) publishRows(rows []pushedRow) {
	byTopic := map[string][][]byte{"*": nil}
	for _, row := range rows {
		byTopic["*"] = append(byTopic["*"], row.full)
		if row.reporting != "" {
			byTopic[row.reporting] = append(byTopic[row.reporting], row.full)
		}
		if row.destination != "" && row.destination != row.reporting {
			byTopic[row.destination] = append(byTopic[row.destination], row.redacted)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	for topic, parts := range byTopic {
		if len(parts) == 0 {
			continue
		}
		payload := make([]byte, 0, 16+len(parts)*512)
		payload = append(payload, `{"rows":[`...)
		for i, part := range parts {
			if i > 0 {
				payload = append(payload, ',')
			}
			payload = append(payload, part...)
		}
		payload = append(payload, `]}`...)
		h.emitLocked(topic, "batch", payload)
	}
}
