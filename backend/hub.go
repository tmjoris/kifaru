package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	rejected atomic.Uint64
	dropped  atomic.Uint64
}

type subscriber struct {
	topic string
	ch    chan []byte
	done  chan struct{}
	once  sync.Once
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
	subscriberBuffer = 8
	heartbeatEvery   = 25 * time.Second
)

func (h *streamHub) init() {
	if h.subs == nil {
		h.subs = map[string]map[*subscriber]struct{}{}
		h.dirty = map[string]struct{}{}
		h.ring = make([]ringFrame, ringCapacity)
		h.epoch = randomHex(4)
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

// emitLocked stores a frame in the replay ring and hands it to the topic's subscribers.
func (h *streamHub) emitLocked(topic, event string, payload []byte) {
	h.seq++
	frame := []byte(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", h.frameID(), event, payload))
	h.ring[h.ringNext] = ringFrame{seq: h.seq, topic: topic, frame: frame}
	h.ringNext = (h.ringNext + 1) % ringCapacity
	if h.ringSize < ringCapacity {
		h.ringSize++
	}
	for sub := range h.subs[topic] {
		select {
		case sub.ch <- frame:
		default:
			// This tab cannot keep up. Cut it loose rather than skip frames
			// silently: it reconnects with its last cursor and is replayed or reset.
			h.dropped.Add(1)
			sub.close()
		}
	}
}

func (s *subscriber) close() { s.once.Do(func() { close(s.done) }) }

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

// subscribe registers a stream and returns what it missed since `since`.
func (h *streamHub) subscribe(topic, since string) (sub *subscriber, replay [][]byte, reset bool, cursor string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	sub = &subscriber{topic: topic, ch: make(chan []byte, subscriberBuffer), done: make(chan struct{})}
	if since != "" {
		epoch, seqText, ok := strings.Cut(since, "-")
		seq, err := strconv.ParseUint(seqText, 10, 64)
		switch {
		case !ok || err != nil || epoch != h.epoch:
			reset = true // a different server run, or nonsense: start over
		case seq < h.seq:
			oldest := h.seq - uint64(h.ringSize) // frames older than this have been overwritten
			if seq < oldest {
				reset = true
			} else {
				for i := 0; i < h.ringSize; i++ {
					entry := h.ring[(h.ringNext-h.ringSize+i+ringCapacity)%ringCapacity]
					if entry.seq > seq && entry.topic == topic {
						replay = append(replay, entry.frame)
					}
				}
			}
		}
	}
	if h.subs[topic] == nil {
		h.subs[topic] = map[*subscriber]struct{}{}
	}
	h.subs[topic][sub] = struct{}{}
	h.total++
	return sub, replay, reset, h.epoch + "-" + strconv.FormatUint(h.seq, 10)
}

func (h *streamHub) unsubscribe(sub *subscriber) {
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
