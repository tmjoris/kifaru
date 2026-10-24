package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttackScenarioEvidenceIsReserved(t *testing.T) {
	app := &App{}
	report := attackReport("abc", false)
	if _, apiErr := app.process(context.Background(), report, "rest"); apiErr == nil ||
		!strings.Contains(apiErr.message, "reserved") {
		t.Fatalf("an outside caller must not set attack_scenario, got %v", apiErr)
	}
}

func TestPostgresAttackScenario(t *testing.T) {
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
	app := &App{
		db: db, standardRaw: raw, standard: standard,
		streams: map[string]map[chan []byte]struct{}{},
	}
	if err := app.initDB(ctx); err != nil {
		t.Fatal(err)
	}

	staff := AuthUser{Email: "staff@kifaru.co.ke", Role: "staff"}
	call := func(handler func(http.ResponseWriter, *http.Request), body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, staff))
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("attack scenario call failed: %d %s", recorder.Code, recorder.Body.String())
		}
		var state map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	reportCount := func() int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM reports
			WHERE transaction_ref LIKE 'ATTACK-%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	signal := func(state map[string]any, index int) map[string]any {
		t.Helper()
		signals, _ := state["signals"].([]any)
		if len(signals) <= index {
			t.Fatalf("expected signal %d in %#v", index, state["signals"])
		}
		return signals[index].(map[string]any)
	}

	// Without Kifaru the whole campaign plays out and nothing reaches the exchange.
	state := call(app.resetAttackScenario, `{"mode":"without"}`)
	for step := 1; step <= attackLastStep; step++ {
		state = call(app.advanceAttackScenario, "")
	}
	if state["status"] != "completed" || reportCount() != 0 || len(state["signals"].([]any)) != 0 {
		t.Fatalf("the run without Kifaru must not publish anything: %#v", state)
	}

	// With Kifaru the first bank's victim-confirmed report alerts the receiver
	// on its own, using the scenario's track record for the first bank.
	state = call(app.resetAttackScenario, `{"mode":"with"}`)
	if state["mode"] != "with" || state["status"] != "ready" {
		t.Fatalf("reset did not switch mode: %#v", state)
	}
	call(app.advanceAttackScenario, "")
	if reportCount() != 0 {
		t.Fatal("the first takeover completes before any bank has a reason to report")
	}
	state = call(app.advanceAttackScenario, "")
	first := signal(state, 0)
	if first["status"] != statusSingleSourceReview || first["alert_tier"] != alertTierReview ||
		first["alert_state"] != "sent" {
		t.Fatalf("the first bank's report should issue a single-source review: %#v", first)
	}
	if !strings.Contains(fmt.Sprint(first["explanation"]), "upheld 9 of its 9") {
		t.Fatalf("the explanation should cite the scenario track record: %s", first["explanation"])
	}
	if standing, err := reporterStanding(ctx, db, attackFirstSender); err != nil || standing.Upheld != 0 {
		t.Fatalf("scenario standing must not leak into real standing: %+v %v", standing, err)
	}

	state = call(app.advanceAttackScenario, "")
	if first = signal(state, 0); first["alert_state"] != "actioned" || first["alert_outcome"] != "held" {
		t.Fatalf("the receiver should hold the beneficiary account: %#v", first)
	}

	// The second bank's lower score corroborates and escalates the held alert.
	state = call(app.advanceAttackScenario, "")
	first, second := signal(state, 0), signal(state, 1)
	if second["status"] != statusCorroborated || second["alert_id"] == "" {
		t.Fatalf("the second bank's report should be corroborated: %#v", second)
	}
	if first["status"] != statusCorroborated || first["alert_tier"] != alertTierCorroborated ||
		first["alert_outcome"] != "held" {
		t.Fatalf("the first alert should escalate in place and stay held: %#v", first)
	}
	if !containsString(toStringSlice(second["reason_codes"]), "LINK:device_switch") {
		t.Fatalf("the attacker's new phone should be linked: %v", second["reason_codes"])
	}
	if containsString(toStringSlice(second["reason_codes"]), "TRUST:reporter_good_standing") {
		t.Fatalf("the second bank has no scenario track record: %v", second["reason_codes"])
	}

	state = call(app.advanceAttackScenario, "")
	first, second = signal(state, 0), signal(state, 1)
	if state["status"] != "completed" || first["alert_outcome"] != "recovered" ||
		second["alert_outcome"] != "recovered" {
		t.Fatalf("both transfers should be recovered: %#v", state)
	}
	if standing, err := reporterStanding(ctx, db, attackFirstSender); err != nil || standing.Upheld != 0 {
		t.Fatalf("scripted receiver outcomes must not build real standing: %+v %v", standing, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, staff))
	recorder := httptest.NewRecorder()
	app.advanceAttackScenario(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("a completed scenario must not advance: %d", recorder.Code)
	}

	call(app.resetAttackScenario, `{"mode":"without"}`)
	var leftovers int
	if err := db.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM reports WHERE transaction_ref LIKE 'ATTACK-%') +
		(SELECT COUNT(*) FROM alerts a JOIN reports r ON r.report_id=a.report_id
		 WHERE r.transaction_ref LIKE 'ATTACK-%')`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 || reportCount() != 0 {
		t.Fatalf("reset should remove every scenario record, %d left", leftovers)
	}
}
