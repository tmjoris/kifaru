package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestTrendDaysFollowKenyanTime(t *testing.T) {
	// 22:30 UTC on 2 November is 01:30 on 3 November in Nairobi.
	late := time.Date(2026, 11, 2, 22, 30, 0, 0, time.UTC)
	if got := dayStart(late).Format("2006-01-02"); got != "2026-11-03" {
		t.Fatalf("dayStart(%s) = %s, want 2026-11-03", late, got)
	}
	today := dayStart(late)
	events := []trendEvent{
		{kind: "report", at: today.Add(-time.Minute)},    // last minute of yesterday
		{kind: "report", at: today, decision: "blocked"}, // first second of today
		{kind: "alert", at: today.Add(time.Hour), tier: alertTierCorroborated, destination: "sha256:a"},
		{kind: "alert", at: today.Add(2 * time.Hour), tier: alertTierCorroborated, destination: "sha256:a"},
		{kind: "alert", at: today.Add(3 * time.Hour), tier: alertTierReview, destination: "sha256:b"},
		{kind: "outcome", at: today.Add(4 * time.Hour), outcome: "released"},
		{kind: "outcome", at: today.Add(5 * time.Hour), outcome: "recovered"},
	}
	day := countEvents(events, today, today.AddDate(0, 0, 1))
	want := trendCounts{Reports: 1, Alerts: 3, Corroborated: 2, SingleSourceReview: 1,
		Released: 1, Recovered: 1, Blocked: 2, Allowed: 1, Incidents: 1}
	if day != want {
		t.Fatalf("today = %+v\nwant    %+v", day, want)
	}
	if previous := countEvents(events, today.AddDate(0, 0, -1), today); previous.Reports != 1 || previous.Alerts != 0 {
		t.Fatalf("yesterday = %+v, want one report", previous)
	}
}

func TestSeedHistoryEvidenceIsReserved(t *testing.T) {
	app := &App{}
	report := ReportIn{Evidence: map[string]any{"seeded_history": true}}
	if _, apiErr := app.process(context.Background(), report, "rest"); apiErr == nil ||
		!strings.Contains(apiErr.message, "reserved") {
		t.Fatalf("a caller outside the seed must not set seeded_history: %+v", apiErr)
	}
}

func TestPlannedHistoryCoversEveryDayAndTier(t *testing.T) {
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, trendZone)
	plan := planTrendHistory(now, seedHistoryDays)
	days := map[string]bool{}
	kinds := map[string]int{}
	for _, incident := range plan {
		if incident.at.After(now) {
			t.Fatalf("incident in the future: %s", incident.at)
		}
		days[incident.at.Format("2006-01-02")] = true
		kinds[incident.kind]++
	}
	if len(days) != seedHistoryDays+1 {
		t.Fatalf("history covers %d days, want %d", len(days), seedHistoryDays+1)
	}
	for _, kind := range []string{"pair", "review", "notice", "awaiting"} {
		if kinds[kind] == 0 {
			t.Fatalf("history has no %s incidents: %v", kind, kinds)
		}
	}
}

func TestPostgresTrends(t *testing.T) {
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

	password := "test-" + randomHex(12)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE auth_users SET password_hash=$1`, string(hash)); err != nil {
		t.Fatal(err)
	}
	type session struct {
		cookie *http.Cookie
		csrf   string
	}
	login := func(email, institution string) session {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"email": email, "password": password, "institution_code": institution})
		recorder := httptest.NewRecorder()
		app.serveHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("login %s: %d %s", email, recorder.Code, recorder.Body.String())
		}
		var payload struct {
			CSRFToken string `json:"csrf_token"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == authCookieName {
				return session{cookie, payload.CSRFToken}
			}
		}
		t.Fatalf("login %s set no session cookie", email)
		return session{}
	}
	call := func(as session, method, path string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(method, path, nil)
		request.AddCookie(as.cookie)
		request.Header.Set("X-Kifaru-CSRF", as.csrf)
		recorder := httptest.NewRecorder()
		app.serveHTTP(recorder, request)
		var payload map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		return recorder.Code, payload
	}
	count := func(payload map[string]any, period, key string) int {
		t.Helper()
		value, _ := numberValue(payload["comparison"].(map[string]any)[period].(map[string]any)[key])
		return int(value)
	}
	staff := login("anthonyjordan@kifaru.co.ke", "")
	ncba := login("anthonyjordan@ncba.co.ke", "bank_a")
	kcb := login("anthonyjordan@kcb.co.ke", "bank_b")

	// One real report from NCBA into Equity today, and one KCB report moved
	// to the last minute of yesterday in Nairobi.
	submit := func(reporter, ref string) string {
		t.Helper()
		result, apiErr := app.process(ctx, ReportIn{
			ReportingInstitution: reporter, TransactionRef: ref,
			TransactionTimestamp:   time.Now().UTC().Format(time.RFC3339),
			DestinationAccountHash: demoHash("trend-mule:" + ref), DestinationInstitution: "psp_c",
			Amount: 40000, BankRiskScore: 0.9, RiskCodes: []string{"ATO-460", "MUL-440"},
			Evidence: map[string]any{"is_new_device": true, "is_new_beneficiary": true,
				"account_age_days": 3, "distinct_senders_7d": 2, "bank_decision": "blocked"},
		}, "rest")
		if apiErr != nil {
			t.Fatal(apiErr.message)
		}
		return result["report_id"].(string)
	}
	submit("bank_a", "TREND-TODAY")
	yesterday := submit("bank_b", "TREND-YESTERDAY")
	lastMinute := dayStart(time.Now()).Add(-time.Minute).UTC().Format("2006-01-02T15:04:05Z")
	if _, err := db.Exec(ctx, `UPDATE reports SET submitted_at=$2 WHERE report_id=$1`, yesterday, lastMinute); err != nil {
		t.Fatal(err)
	}

	// Tenant isolation: an institution sees only itself, and cannot ask for
	// another institution or for the whole ecosystem.
	status, own := call(ncba, http.MethodGet, "/v1/stats/trends")
	if status != http.StatusOK || own["scope"] != "institution" || own["institution"] != "bank_a" {
		t.Fatalf("NCBA's own trends: %d %v", status, own)
	}
	if count(own, "today", "reports") != 1 || count(own, "today", "blocked") != 1 || own["ranking"] != nil {
		t.Fatalf("NCBA should see its one report and no ranking: %v", own["comparison"])
	}
	for _, path := range []string{"/v1/stats/trends?institution=bank_b", "/v1/stats/trends?institution=*"} {
		if status, _ := call(ncba, http.MethodGet, path); status != http.StatusForbidden {
			t.Fatalf("NCBA reading %s: status %d, want 403", path, status)
		}
	}
	_, other := call(kcb, http.MethodGet, "/v1/stats/trends")
	if count(other, "today", "reports") != 0 || count(other, "yesterday", "reports") != 1 {
		t.Fatalf("KCB's report belongs to yesterday: %v", other["comparison"])
	}
	if status, _ := call(ncba, http.MethodGet, "/v1/stats/trends?days=500"); status != http.StatusUnprocessableEntity {
		t.Fatalf("days=500: status %d, want 422", status)
	}

	// Staff see the ecosystem, with a ranking and one bucket per day.
	status, all := call(staff, http.MethodGet, "/v1/stats/trends?days=30")
	if status != http.StatusOK || all["scope"] != "ecosystem" || all["ranking"] == nil {
		t.Fatalf("staff trends: %d %v", status, all)
	}
	series := all["series"].([]any)
	if len(series) != 30 || series[29].(map[string]any)["day"] != dayStart(time.Now()).Format("2006-01-02") {
		t.Fatalf("series has %d days ending %v", len(series), series[len(series)-1])
	}
	if count(all, "today", "reports") != 1 || count(all, "yesterday", "reports") != 1 {
		t.Fatalf("staff comparison: %v", all["comparison"])
	}
	if _, scoped := call(staff, http.MethodGet, "/v1/stats/trends?institution=bank_b"); scoped["scope"] != "institution" {
		t.Fatalf("staff can narrow to one institution: %v", scoped)
	}

	// Only staff can seed or reset the history.
	if status, _ := call(ncba, http.MethodPost, "/v1/admin/trend-history/seed"); status != http.StatusForbidden {
		t.Fatalf("an institution seeding history: status %d, want 403", status)
	}
	standingBefore := map[string]ReporterStanding{}
	for _, code := range seedHistoryInstitutions {
		standingBefore[code], _ = reporterStanding(ctx, db, code)
	}
	if status, _ := call(ncba, http.MethodGet, "/v1/admin/trend-history"); status != http.StatusForbidden {
		t.Fatalf("an institution reading the seed status: status %d, want 403", status)
	}
	// The seed runs in the background: it answers 202 at once, refuses a second
	// seed or a reset until it finishes, and its status then holds the summary.
	seed := func(checkBusy bool) map[string]any {
		t.Helper()
		status, started := call(staff, http.MethodPost, "/v1/admin/trend-history/seed")
		if status != http.StatusAccepted || started["running"] != true {
			t.Fatalf("seed: %d %v", status, started)
		}
		if checkBusy {
			if status, _ := call(staff, http.MethodPost, "/v1/admin/trend-history/seed"); status != http.StatusConflict {
				t.Fatalf("a second seed while one runs: status %d, want 409", status)
			}
			if status, _ := call(staff, http.MethodPost, "/v1/admin/trend-history/reset"); status != http.StatusConflict {
				t.Fatalf("a reset while the seed runs: status %d, want 409", status)
			}
		}
		for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			_, job := call(staff, http.MethodGet, "/v1/admin/trend-history")
			if job["running"] == false {
				if job["error"] != "" {
					t.Fatalf("seed failed: %v", job["error"])
				}
				return job["last"].(map[string]any)
			}
		}
		t.Fatal("the seed did not finish in five minutes")
		return nil
	}
	seeded := seed(true)
	reports, _ := numberValue(seeded["reports"])
	alerts, _ := numberValue(seeded["alerts"])
	outcomes, _ := numberValue(seeded["outcomes"])
	if reports < 150 || alerts < 80 || outcomes < 40 {
		t.Fatalf("seed wrote too little history: %v", seeded)
	}

	// Seeded records went through the pipeline: every tier appears, only
	// hashes are stored, and they are dated across the last 60 days.
	var tiers, unhashed, spanDays int
	if err := db.QueryRow(ctx, `SELECT COUNT(DISTINCT a.alert_tier) FROM alerts a
		JOIN reports r ON r.report_id=a.report_id WHERE r.evidence LIKE $1`, seededEvidence).Scan(&tiers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE evidence LIKE $1 AND NOT (
		subject_account_hash LIKE 'sha256:%' AND destination_account_hash LIKE 'sha256:%'
		AND subject_customer_hash LIKE 'sha256:%')`, seededEvidence).Scan(&unhashed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(DISTINCT LEFT(submitted_at,10)) FROM reports
		WHERE evidence LIKE $1`, seededEvidence).Scan(&spanDays); err != nil {
		t.Fatal(err)
	}
	if tiers != 3 || unhashed != 0 || spanDays < seedHistoryDays-1 {
		t.Fatalf("seeded history: %d tiers, %d unhashed reports, %d days", tiers, unhashed, spanDays)
	}
	for code, before := range standingBefore {
		if after, _ := reporterStanding(ctx, db, code); after != before {
			t.Fatalf("seeded outcomes changed %s's standing from %+v to %+v", code, before, after)
		}
	}
	var notifications int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 0 {
		t.Fatalf("seeded decisions left %d notifications", notifications)
	}
	_, all = call(staff, http.MethodGet, "/v1/stats/trends?days=30")
	active := 0
	for _, day := range all["series"].([]any) {
		if reports, _ := numberValue(day.(map[string]any)["reports"]); reports > 0 {
			active++
		}
	}
	ranking := all["ranking"].([]any)
	if active < 28 || len(ranking) < len(seedHistoryInstitutions) ||
		count(all, "last_month", "reports") == 0 || count(all, "this_month", "incidents") == 0 && count(all, "last_month", "incidents") == 0 {
		t.Fatalf("trends after seeding: %d active days, ranking %v, comparison %v", active, ranking, all["comparison"])
	}

	// Seeding again replaces the history instead of adding to it.
	again := seed(false)
	if removed, _ := numberValue(again["removed_reports"]); removed != reports {
		t.Fatalf("second seed removed %v reports, want %v", removed, reports)
	}

	// Reset removes every seeded record and leaves real reports alone.
	status, reset := call(staff, http.MethodPost, "/v1/admin/trend-history/reset")
	if status != http.StatusOK {
		t.Fatalf("reset: %d %v", status, reset)
	}
	var leftover, kept, kb, audited int
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE evidence LIKE $1`, seededEvidence).Scan(&leftover)
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE transaction_ref LIKE 'TREND-%'`).Scan(&kept)
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM knowledge_base WHERE label=$1`, seedHistoryLabel).Scan(&kb)
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'trend_history.%'`).Scan(&audited)
	if leftover != 0 || kept != 2 || kb != 0 || audited != 3 {
		t.Fatalf("after reset: %d seeded reports, %d real reports, %d seeded list entries, %d audit entries",
			leftover, kept, kb, audited)
	}
}
