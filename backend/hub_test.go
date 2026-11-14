package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testStream is the client end of a stream connection, read the way a browser
// reads a chunked event stream.
type testStream struct {
	t      *testing.T
	client net.Conn
	reader *bufio.Reader
	sub    *subscriber
}

// openStream does what the HTTP handler does after it has hijacked the
// connection: prepare, write the replay, then attach.
func openStream(t *testing.T, hub *streamHub, topic, since string, expires time.Time) (*testStream, [][]byte, bool, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	sub, replay, reset, cursor, through := hub.subscribe(topic, since, expires)
	sub.conn = server
	write := func(chunk []byte) bool { _, err := server.Write(chunk); return err == nil }
	for _, frame := range replay {
		write(chunked(frame))
	}
	if !hub.attach(sub, through, write) {
		t.Fatal("attach failed")
	}
	return &testStream{t: t, client: client, reader: bufio.NewReader(client), sub: sub}, replay, reset, cursor
}

// next reads one chunk (one event frame) from the stream.
func (s *testStream) next() string {
	s.t.Helper()
	_ = s.client.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := s.reader.ReadString('\n')
	if err != nil {
		s.t.Fatalf("no frame arrived: %v", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
	if err != nil {
		s.t.Fatalf("not a chunk size: %q", line)
	}
	data := make([]byte, size+2)
	if _, err := io.ReadFull(s.reader, data); err != nil {
		s.t.Fatal(err)
	}
	return string(data[:size])
}

// nothing asserts that no frame arrives within a short time.
func (s *testStream) nothing() {
	s.t.Helper()
	_ = s.client.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if line, err := s.reader.ReadString('\n'); err == nil {
		s.t.Errorf("an unexpected frame arrived: %q", line)
	}
}

func TestHubReplaysWhatAReconnectingTabMissed(t *testing.T) {
	var hub streamHub
	first, _, _, cursor := openStream(t, &hub, "bank_a", "", time.Time{})
	hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": 1})
	first.next()
	hub.detach(first.sub) // the tab goes away...
	hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": 2})
	hub.publish([]string{"bank_b"}, "alert", map[string]any{"n": 99}) // another topic: never replayed
	hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": 3})

	// ...and comes back with the cursor of its first snapshot.
	_, replay, reset, _ := openStream(t, &hub, "bank_a", cursor, time.Time{})
	if reset {
		t.Fatal("a recent cursor must be replayed, not reset")
	}
	if len(replay) != 3 {
		t.Fatalf("replayed %d frames, want the 3 for bank_a", len(replay))
	}
	for i, want := range []string{`"n":1`, `"n":2`, `"n":3`} {
		if !strings.Contains(string(replay[i]), want) {
			t.Errorf("replay[%d] = %q, want it to hold %s", i, replay[i], want)
		}
	}
}

func TestHubResetsATabItCannotReplay(t *testing.T) {
	var hub streamHub
	_, _, _, cursor := openStream(t, &hub, "bank_a", "", time.Time{})
	for i := 0; i < ringCapacity+10; i++ {
		hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": i})
	}
	if _, replay, reset, _ := openStream(t, &hub, "bank_a", cursor, time.Time{}); !reset || len(replay) != 0 {
		t.Errorf("a cursor older than the ring: reset=%v replay=%d, want reset and nothing replayed", reset, len(replay))
	}
	for name, since := range map[string]string{"another server run": "deadbeef-5", "garbage": "banana", "bad number": hub.epoch + "-x"} {
		if _, _, reset, _ := openStream(t, &hub, "bank_a", since, time.Time{}); !reset {
			t.Errorf("%s: want reset", name)
		}
	}
	if _, replay, reset, _ := openStream(t, &hub, "bank_a", hub.cursor(), time.Time{}); reset || len(replay) != 0 {
		t.Error("a tab that is fully up to date needs neither replay nor reset")
	}
}

func TestHubDeliversFramesInOrder(t *testing.T) {
	var hub streamHub
	tab, _, _, _ := openStream(t, &hub, "bank_a", "", time.Time{})
	for i := 0; i < 300; i++ {
		hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": i})
	}
	for i := 0; i < 300; i++ {
		if frame := tab.next(); !strings.Contains(frame, fmt.Sprintf(`"n":%d}`, i)) {
			t.Fatalf("frame %d out of order: %q", i, frame)
		}
	}
}

func TestHubDropsATabWhoseConnectionIsGone(t *testing.T) {
	var hub streamHub
	gone, _, _, _ := openStream(t, &hub, "bank_a", "", time.Time{})
	stays, _, _, _ := openStream(t, &hub, "bank_a", "", time.Time{})
	if hub.audience() != 2 {
		t.Fatalf("audience = %d, want 2", hub.audience())
	}
	gone.client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for hub.audience() != 1 && time.Now().Before(deadline) {
		hub.publish([]string{"bank_a"}, "alert", map[string]any{"n": 1})
		time.Sleep(20 * time.Millisecond)
		stays.next()
	}
	if hub.audience() != 1 {
		t.Fatalf("audience = %d: a closed tab must be removed once a write to it fails", hub.audience())
	}
}

func TestHubEndsAStreamWhoseSessionHasExpired(t *testing.T) {
	var hub streamHub
	expiring, _, _, _ := openStream(t, &hub, "bank_a", "", time.Now().Add(300*time.Millisecond))
	openStream(t, &hub, "bank_a", "", time.Time{})
	deadline := time.Now().Add(4 * time.Second)
	for hub.audience() != 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if hub.audience() != 1 {
		t.Fatalf("audience = %d, want the expired stream gone and the other kept", hub.audience())
	}
	_ = expiring
}

func TestHubTickLengthensWithTheAudience(t *testing.T) {
	var hub streamHub
	if got := hub.tick(); got != baseTick {
		t.Fatalf("empty hub tick = %v, want %v", got, baseTick)
	}
	hub.mu.Lock()
	hub.init()
	hub.total = 2500
	hub.mu.Unlock()
	if got := hub.tick(); got != baseTick+2500*perSubscriberTax {
		t.Errorf("2,500 tabs: tick = %v", got)
	}
	hub.mu.Lock()
	hub.total = 1_000_000
	hub.mu.Unlock()
	if got := hub.tick(); got != maxTick {
		t.Errorf("a huge audience must be capped at %v, got %v", maxTick, got)
	}
}

func TestHubTooManyChangesBecomeOneDirtyHint(t *testing.T) {
	var hub streamHub
	for i := 0; i <= maxPushRows+5; i++ {
		hub.markDirty(fmt.Sprintf("rpt-%d", i))
	}
	ids, overflow, _ := hub.takeDirty()
	if !overflow || len(ids) != 0 {
		t.Fatalf("overflow=%v ids=%d, want an overflow and no individual ids", overflow, len(ids))
	}
	hub.markDirty("rpt-1")
	hub.markDirty("rpt-1")
	hub.markDirty("")
	hub.markDirty("<nil>")
	if ids, overflow, _ := hub.takeDirty(); overflow || len(ids) != 1 {
		t.Errorf("after a reset: ids=%v overflow=%v, want the one distinct id", ids, overflow)
	}
}

func TestHubPushesEachTopicOnlyWhatItMaySee(t *testing.T) {
	var hub streamHub
	reporter, _, _, _ := openStream(t, &hub, "bank_a", "", time.Time{})
	receiver, _, _, _ := openStream(t, &hub, "bank_b", "", time.Time{})
	staff, _, _, _ := openStream(t, &hub, "*", "", time.Time{})
	bystander, _, _, _ := openStream(t, &hub, "bank_c", "", time.Time{})
	hub.publishRows([]pushedRow{{
		reporting: "bank_a", destination: "bank_b",
		full:     []byte(`{"report_id":"r1","subject_customer_hash":"SECRET"}`),
		redacted: []byte(`{"report_id":"r1","subject_customer_hash":""}`),
	}})
	if frame := reporter.next(); !strings.Contains(frame, "SECRET") || !strings.Contains(frame, "event: batch") {
		t.Errorf("the reporting bank must see its own customer hash: %q", frame)
	}
	if frame := receiver.next(); strings.Contains(frame, "SECRET") || !strings.Contains(frame, `"report_id":"r1"`) {
		t.Errorf("the receiving bank must get the row without the customer hash: %q", frame)
	}
	if frame := staff.next(); !strings.Contains(frame, "SECRET") {
		t.Errorf("staff see everything: %q", frame)
	}
	bystander.nothing()
}

// With a real database: ingest a report and check the right tabs get its row.
func TestPostgresPushesAnIngestedReport(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, `DROP TABLE IF EXISTS
		user_access_requests,auth_sessions,auth_users,demo_events,demo_stream_state,notifications,
		attack_scenario_state,guided_demo_state,alert_actions,audit_log,config,knowledge_base,alerts,artefacts,
		validations,reports,institutions,schema_migrations CASCADE`); err != nil {
		t.Fatal(err)
	}
	raw, standard, err := loadStandard()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, standardRaw: raw, standard: standard}
	if err := app.initDB(ctx); err != nil {
		t.Fatal(err)
	}

	customerHash := "sha256:" + strings.Repeat("a", 64)
	reporter, _, _, _ := openStream(t, &app.hub, "bank_a", "", time.Time{})
	receiver, _, _, _ := openStream(t, &app.hub, "bank_b", "", time.Time{})
	staff, _, _, _ := openStream(t, &app.hub, "*", "", time.Time{})
	bystander, _, _, _ := openStream(t, &app.hub, "bank_c", "", time.Time{})
	snapshotCursor := app.hub.cursor()

	result, apiErr := app.process(ctx, ReportIn{
		ReportingInstitution: "bank_a", DestinationInstitution: "bank_b",
		TransactionRef: "TX-PUSH-1", TransactionTimestamp: time.Now().UTC().Format(time.RFC3339),
		SubjectCustomerHash: customerHash, DestinationAccountHash: "sha256:" + strings.Repeat("b", 64),
		Amount: 45000, RiskCodes: []string{"MUL-440", "ATO-460", "VEL-429"},
		Evidence: map[string]any{"account_age_days": 3, "distinct_senders_7d": 8, "is_new_device": true, "is_new_beneficiary": true, "velocity_1h": 6},
	}, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	reportID := fmt.Sprint(result["report_id"])
	app.hub.markDirty(reportID) // what submitReport does after a commit
	app.flushHub(ctx)

	rowOf := func(frame string) map[string]any {
		t.Helper()
		_, data, _ := strings.Cut(frame, "data: ")
		var payload struct {
			Rows []map[string]any `json:"rows"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &payload); err != nil || len(payload.Rows) != 1 {
			t.Fatalf("not one pushed row: %v\n%s", err, frame)
		}
		return payload.Rows[0]
	}
	if row := rowOf(reporter.next()); row["report_id"] != reportID || row["subject_customer_hash"] != customerHash || row["status"] == nil {
		t.Errorf("reporter's row = %v", row)
	}
	if row := rowOf(receiver.next()); row["report_id"] != reportID || row["subject_customer_hash"] != "" {
		t.Errorf("receiver must get the row with the customer hash blanked: %v", row)
	}
	if row := rowOf(staff.next()); row["subject_customer_hash"] != customerHash {
		t.Errorf("staff row = %v", row)
	}
	bystander.nothing()

	// A tab that took its snapshot before the report existed catches up by replay.
	_, replay, reset, _ := openStream(t, &app.hub, "bank_b", snapshotCursor, time.Time{})
	if reset || len(replay) != 1 || rowOf(string(replay[0]))["report_id"] != reportID {
		t.Errorf("replay for a late subscriber: reset=%v frames=%d", reset, len(replay))
	}
}
