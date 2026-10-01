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

func TestValidateReportRejectsCleartextIdentifiers(t *testing.T) {
	report := ReportIn{
		ReportingInstitution: "bank_a",
		TransactionRef:       "TX-1",
		TransactionTimestamp: "2026-09-30T07:00:00Z",
		SubjectAccountHash:   "0712345678",
	}
	if err := validateReport(report); err == nil || !strings.Contains(err.Error(), "sha256:") {
		t.Fatalf("expected cleartext identifier rejection, got %v", err)
	}
}

func TestNormalizeDerivesBehaviouralCodes(t *testing.T) {
	_, standard, err := loadStandard()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{standard: standard}
	codes, err := app.normalize(ReportIn{Evidence: map[string]any{
		"flow_through_ratio":  0.97,
		"dwell_minutes":       4,
		"account_age_days":    3,
		"distinct_senders_7d": 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"MUL-440", "MUL-441", "MUL-442"} {
		if !containsString(codes, expected) {
			t.Fatalf("expected %s in %v", expected, codes)
		}
	}
}

func TestAlertType(t *testing.T) {
	if got := alertTypeFor(ReportIn{Amount: 0}); got != "advisory" {
		t.Fatalf("zero-value event should be advisory, got %s", got)
	}
	if got := alertTypeFor(ReportIn{Amount: 1500}); got != "hold" {
		t.Fatalf("transfer should request a hold, got %s", got)
	}
}

func TestDemoReportUsesSentinelShapeAndCampaignPairs(t *testing.T) {
	first, firstPayload, firstMeta := demoReport(1)
	second, secondPayload, secondMeta := demoReport(2)
	if first.DestinationAccountHash != second.DestinationAccountHash {
		t.Fatal("paired events should share one protected destination artefact")
	}
	if first.ReportingInstitution == second.ReportingInstitution {
		t.Fatal("paired events should come from different institutions")
	}
	if firstMeta["topic"] != "sentinel.security-alert" || secondMeta["topic"] != firstMeta["topic"] {
		t.Fatalf("unexpected demo topic: %#v %#v", firstMeta, secondMeta)
	}
	if firstPayload["ProviderName"] != "Microsoft Sentinel" || secondPayload["ProviderName"] != "Microsoft Sentinel" {
		t.Fatal("demo payload should follow the Microsoft Sentinel alert shape")
	}
	if first.Evidence["synthetic_stream"] != true || !strings.HasPrefix(first.DestinationAccountHash, "sha256:") {
		t.Fatal("demo report must be clearly synthetic and use protected identifiers")
	}
}

func TestPostgresPipeline(t *testing.T) {
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
	_, err = db.Exec(ctx, `DROP TABLE IF EXISTS
		auth_sessions,auth_users,demo_events,demo_stream_state,notifications,
		alert_actions,audit_log,config,knowledge_base,alerts,artefacts,
		validations,reports,institutions,schema_migrations CASCADE`)
	if err != nil {
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

	var demoUserCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM auth_users").Scan(&demoUserCount); err != nil {
		t.Fatal(err)
	}
	if demoUserCount != 5 {
		t.Fatalf("expected five seeded demo users, got %d", demoUserCount)
	}

	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/history?institution=psp_c", nil)
	unauthenticatedRecorder := httptest.NewRecorder()
	app.serveHTTP(unauthenticatedRecorder, unauthenticated)
	if unauthenticatedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("history without a session should fail, got %d", unauthenticatedRecorder.Code)
	}

	testPassword := "test-" + randomHex(12)
	testHash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE auth_users SET password_hash=$1
		WHERE email IN (
			'anthonyjordan@ncba.co.ke',
			'anthonyjordan@equitybank.co.ke',
			'anthonyjordan@kifaru.co.ke'
		)`,
		string(testHash)); err != nil {
		t.Fatal(err)
	}
	login := func(email, password, institution string) (*http.Cookie, string, string, int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"email": email, "password": password, "institution_code": institution,
		})
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		app.serveHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			return nil, "", "", recorder.Code
		}
		var payload struct {
			CSRFToken   string `json:"csrf_token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		var sessionCookie *http.Cookie
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == authCookieName {
				sessionCookie = cookie
			}
		}
		return sessionCookie, payload.CSRFToken, payload.AccessToken, recorder.Code
	}
	if _, _, _, status := login("anthonyjordan@equitybank.co.ke", "wrong-password", "psp_c"); status != http.StatusUnauthorized {
		t.Fatalf("invalid credentials should fail, got %d", status)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if _, _, _, status := login("anthonyjordan@ncba.co.ke", "wrong-password", "bank_a"); status != http.StatusUnauthorized {
			t.Fatalf("failed sign-in %d should be rejected, got %d", attempt+1, status)
		}
	}
	if _, _, _, status := login("anthonyjordan@ncba.co.ke", testPassword, "bank_a"); status != http.StatusTooManyRequests {
		t.Fatalf("locked account should reject the correct password, got %d", status)
	}
	if _, _, _, status := login("anthonyjordan@equitybank.co.ke", testPassword, ""); status != http.StatusForbidden {
		t.Fatalf("institution account on staff sign-in should fail, got %d", status)
	}
	if _, _, _, status := login("anthonyjordan@equitybank.co.ke", testPassword, "bank_b"); status != http.StatusForbidden {
		t.Fatalf("institution mismatch should fail, got %d", status)
	}
	if _, _, _, status := login("anthonyjordan@kifaru.co.ke", testPassword, "psp_c"); status != http.StatusForbidden {
		t.Fatalf("staff account on institution sign-in should fail, got %d", status)
	}
	equityCookie, equityCSRF, equityToken, status := login("anthonyjordan@equitybank.co.ke", testPassword, "psp_c")
	if status != http.StatusOK || equityCookie == nil || equityCSRF == "" || equityToken == "" {
		t.Fatalf("institution login failed: status=%d cookie=%v csrf=%q token=%q",
			status, equityCookie, equityCSRF, equityToken)
	}
	authorizedHistory := httptest.NewRequest(http.MethodGet, "/v1/history?institution=psp_c", nil)
	authorizedHistory.Header.Set("Authorization", "Bearer "+equityToken)
	authorizedHistoryRecorder := httptest.NewRecorder()
	app.serveHTTP(authorizedHistoryRecorder, authorizedHistory)
	if authorizedHistoryRecorder.Code != http.StatusOK {
		t.Fatalf("assigned institution history failed: %d %s",
			authorizedHistoryRecorder.Code, authorizedHistoryRecorder.Body.String())
	}
	cookieSession := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	cookieSession.AddCookie(equityCookie)
	cookieSessionRecorder := httptest.NewRecorder()
	app.serveHTTP(cookieSessionRecorder, cookieSession)
	if cookieSessionRecorder.Code != http.StatusOK {
		t.Fatalf("cookie session restore failed: %d %s",
			cookieSessionRecorder.Code, cookieSessionRecorder.Body.String())
	}
	var rawTokenCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM auth_sessions WHERE token_hash=$1",
		equityToken).Scan(&rawTokenCount); err != nil {
		t.Fatal(err)
	}
	if rawTokenCount != 0 {
		t.Fatal("raw bearer token was stored in PostgreSQL")
	}
	missingCSRF := httptest.NewRequest(http.MethodPost, "/v1/reports", bytes.NewReader([]byte(`{}`)))
	missingCSRF.Header.Set("Authorization", "Bearer "+equityToken)
	missingCSRFRecorder := httptest.NewRecorder()
	app.serveHTTP(missingCSRFRecorder, missingCSRF)
	if missingCSRFRecorder.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF should fail, got %d", missingCSRFRecorder.Code)
	}
	crossInstitution := httptest.NewRequest(http.MethodGet, "/v1/history?institution=bank_b", nil)
	crossInstitution.Header.Set("Authorization", "Bearer "+equityToken)
	crossInstitutionRecorder := httptest.NewRecorder()
	app.serveHTTP(crossInstitutionRecorder, crossInstitution)
	if crossInstitutionRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-institution history should fail, got %d", crossInstitutionRecorder.Code)
	}
	institutionAdmin := httptest.NewRequest(http.MethodGet, "/v1/admin/demo-stream", nil)
	institutionAdmin.Header.Set("Authorization", "Bearer "+equityToken)
	institutionAdminRecorder := httptest.NewRecorder()
	app.serveHTTP(institutionAdminRecorder, institutionAdmin)
	if institutionAdminRecorder.Code != http.StatusForbidden {
		t.Fatalf("institution user should not control the stream, got %d", institutionAdminRecorder.Code)
	}
	institutionStats := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	institutionStats.Header.Set("Authorization", "Bearer "+equityToken)
	institutionStatsRecorder := httptest.NewRecorder()
	app.serveHTTP(institutionStatsRecorder, institutionStats)
	if institutionStatsRecorder.Code != http.StatusForbidden {
		t.Fatalf("institution user should not access ecosystem statistics, got %d", institutionStatsRecorder.Code)
	}
	staffCookie, staffCSRF, staffToken, status := login("anthonyjordan@kifaru.co.ke", testPassword, "")
	if status != http.StatusOK || staffCookie == nil || staffCSRF == "" || staffToken == "" {
		t.Fatalf("staff login failed: status=%d cookie=%v csrf=%q token=%q",
			status, staffCookie, staffCSRF, staffToken)
	}
	staffStats := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	staffStats.Header.Set("Authorization", "Bearer "+staffToken)
	staffStatsRecorder := httptest.NewRecorder()
	app.serveHTTP(staffStatsRecorder, staffStats)
	if staffStatsRecorder.Code != http.StatusOK {
		t.Fatalf("staff stats failed: %d %s", staffStatsRecorder.Code, staffStatsRecorder.Body.String())
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	logoutRequest.Header.Set("Authorization", "Bearer "+staffToken)
	logoutRequest.Header.Set("X-Kifaru-CSRF", staffCSRF)
	logoutRecorder := httptest.NewRecorder()
	app.serveHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("staff logout failed: %d %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	loggedOutSession := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	loggedOutSession.Header.Set("Authorization", "Bearer "+staffToken)
	loggedOutSessionRecorder := httptest.NewRecorder()
	app.serveHTTP(loggedOutSessionRecorder, loggedOutSession)
	if loggedOutSessionRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out bearer token should be invalid, got %d", loggedOutSessionRecorder.Code)
	}

	hash := "sha256:" + strings.Repeat("a", 64)
	first := ReportIn{
		ReportingInstitution:   "bank_a",
		DestinationInstitution: "bank_b",
		TransactionRef:         "TX-CORRO-1",
		TransactionTimestamp:   time.Now().UTC().Format(time.RFC3339),
		DestinationAccountHash: hash,
		Amount:                 45000,
		RiskCodes:              []string{"MUL-440", "ATO-460", "VEL-429"},
	}
	firstResult, apiErr := app.process(ctx, first, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	firstID := firstResult["report_id"].(string)
	var firstStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "INSUFFICIENT_EVIDENCE" {
		t.Fatalf("expected held first report, got %s", firstStatus)
	}

	differentArtefactType := first
	differentArtefactType.ReportingInstitution = "bank_b"
	differentArtefactType.DestinationInstitution = "bank_a"
	differentArtefactType.TransactionRef = "TX-DIFFERENT-TYPE"
	differentArtefactType.SubjectAccountHash = hash
	differentArtefactType.DestinationAccountHash = "sha256:" + strings.Repeat("b", 64)
	if _, apiErr := app.process(ctx, differentArtefactType, "rest"); apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "INSUFFICIENT_EVIDENCE" {
		t.Fatalf("different artefact types must not corroborate; got %s", firstStatus)
	}

	second := first
	second.ReportingInstitution = "bank_b"
	second.DestinationInstitution = "bank_a"
	second.TransactionRef = "TX-CORRO-2"
	if _, apiErr := app.process(ctx, second, "rest"); apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "VALIDATED_FRAUD" {
		t.Fatalf("expected automatic revalidation to validate first report, got %s", firstStatus)
	}
	var validationCount, alertCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM validations WHERE report_id=$1", firstID).Scan(&validationCount); err != nil {
		t.Fatal(err)
	}
	if validationCount != 2 {
		t.Fatalf("expected original and replacement validation, got %d", validationCount)
	}
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM alerts WHERE report_id=$1", firstID).Scan(&alertCount); err != nil {
		t.Fatal(err)
	}
	if alertCount != 1 {
		t.Fatalf("expected one routed alert, got %d", alertCount)
	}

	replayed, apiErr := app.process(ctx, first, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if replayed["report_id"] != firstID || replayed["idempotent_replay"] != true {
		t.Fatalf("expected retry to return original report, got %#v", replayed)
	}
	var reportCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM reports").Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 3 {
		t.Fatalf("idempotent retry created another report; count=%d", reportCount)
	}

	var alertID string
	if err := db.QueryRow(ctx, "SELECT alert_id FROM alerts WHERE report_id=$1", firstID).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"state": "disputed"})
	request := httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@kcb.co.ke", Role: "institution", InstitutionCode: "bank_b",
	}))
	recorder := httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("dispute without comment should fail, got %d", recorder.Code)
	}
	body, _ = json.Marshal(map[string]string{"state": "acknowledged"})
	request = httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@equitybank.co.ke", Role: "institution", InstitutionCode: "psp_c",
	}))
	recorder = httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-receiving institution should not update an alert, got %d", recorder.Code)
	}
	body, _ = json.Marshal(map[string]string{"state": "disputed", "comment": "Known customer payment"})
	request = httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@kcb.co.ke", Role: "institution", InstitutionCode: "bank_b",
	}))
	recorder = httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("commented dispute failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var notificationCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM notifications
		WHERE event_type='alert.disputed' AND record_id=$1`, alertID).Scan(&notificationCount); err != nil {
		t.Fatal(err)
	}
	if notificationCount != 1 {
		t.Fatalf("expected reporting-institution notification, got %d", notificationCount)
	}

	badDestination := first
	badDestination.TransactionRef = "TX-ROLLBACK"
	badDestination.DestinationInstitution = "missing-institution"
	if _, apiErr := app.process(ctx, badDestination, "rest"); apiErr == nil {
		t.Fatal("expected unknown destination to fail atomically")
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM reports
		WHERE transaction_ref='TX-ROLLBACK'`).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 0 {
		t.Fatalf("failed transaction left a report behind")
	}

	var auditCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount == 0 {
		t.Fatal("expected validation and alert audit records")
	}

	firstDemo, emitted, err := app.produceDemoEvent(ctx, true)
	if err != nil || !emitted {
		t.Fatalf("first demo event failed: emitted=%v result=%#v err=%v", emitted, firstDemo, err)
	}
	firstDemoReportID := firstDemo["report_id"].(string)
	var firstDemoStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstDemoReportID).Scan(&firstDemoStatus); err != nil {
		t.Fatal(err)
	}
	if firstDemoStatus != "INSUFFICIENT_EVIDENCE" {
		t.Fatalf("first demo event should await corroboration, got %s", firstDemoStatus)
	}
	secondDemo, emitted, err := app.produceDemoEvent(ctx, true)
	if err != nil || !emitted {
		t.Fatalf("second demo event failed: emitted=%v result=%#v err=%v", emitted, secondDemo, err)
	}
	var firstDemoValidationCount int
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstDemoReportID).Scan(&firstDemoStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM validations
		WHERE report_id=$1`, firstDemoReportID).Scan(&firstDemoValidationCount); err != nil {
		t.Fatal(err)
	}
	if firstDemoStatus != "VALIDATED_FRAUD" || firstDemoValidationCount != 2 {
		t.Fatalf("paired demo event should revalidate the first report, status=%s validations=%d",
			firstDemoStatus, firstDemoValidationCount)
	}
	var demoEventCount, demoReportCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM demo_events").Scan(&demoEventCount); err != nil {
		t.Fatal(err)
	}
	if demoEventCount != 2 {
		t.Fatalf("expected two retained demo events, got %d", demoEventCount)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM reports
		WHERE evidence LIKE '%"synthetic_stream":true%'`).Scan(&demoReportCount); err != nil {
		t.Fatal(err)
	}
	if demoReportCount != 2 {
		t.Fatalf("expected two synthetic reports, got %d", demoReportCount)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/admin/demo-stream/reset", nil)
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "staff@kifaru.co.ke", Role: "staff",
	}))
	recorder = httptest.NewRecorder()
	app.resetDemoStream(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("demo reset failed: %d %s", recorder.Code, recorder.Body.String())
	}
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM demo_events").Scan(&demoEventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM reports
		WHERE evidence LIKE '%"synthetic_stream":true%'`).Scan(&demoReportCount); err != nil {
		t.Fatal(err)
	}
	if demoEventCount != 0 || demoReportCount != 0 {
		t.Fatalf("reset left synthetic data behind: events=%d reports=%d", demoEventCount, demoReportCount)
	}
}
