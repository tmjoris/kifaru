package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

func TestSignalEvidenceContractRejectsUnknownAndUnsupportedClaims(t *testing.T) {
	_, standard, err := loadStandard()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{standard: standard}
	if _, err := app.normalize(ReportIn{RiskCodes: []string{"UNKNOWN-999"}}); err == nil {
		t.Fatal("unknown risk codes must be rejected")
	}
	if _, err := app.normalize(ReportIn{BankRuleIDs: []string{"UNKNOWN_RULE"}}); err == nil {
		t.Fatal("unknown bank rules must be rejected")
	}
	report := ReportIn{
		Amount:    5000,
		RiskCodes: []string{"BEN-450"},
		Evidence:  map[string]any{},
	}
	codes, err := app.normalize(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.validateCodeEvidence(report, codes); err == nil ||
		!strings.Contains(err.Error(), "is_new_beneficiary") {
		t.Fatalf("missing evidence should be rejected, got %v", err)
	}
	report.Evidence["is_new_beneficiary"] = true
	if err := app.validateCodeEvidence(report, codes); err != nil {
		t.Fatalf("complete evidence was rejected: %v", err)
	}
}

func TestReportValidationRejectsRawNarrativeAndDeviceIdentifiers(t *testing.T) {
	base := ReportIn{
		ReportingInstitution: "bank_a",
		TransactionRef:       "TX-VALIDATION",
		TransactionTimestamp: time.Now().UTC().Format(time.RFC3339),
		Evidence:             map[string]any{},
	}
	withEmail := base
	withEmail.Narrative = "Contact jane@example.com about this report"
	if err := validateReport(withEmail); err == nil || !strings.Contains(err.Error(), "raw email") {
		t.Fatalf("narrative PII should be rejected, got %v", err)
	}
	withDevice := base
	withDevice.Evidence = map[string]any{"device_profile": "raw-device-id"}
	if err := validateReport(withDevice); err == nil || !strings.Contains(err.Error(), "device_profile") {
		t.Fatalf("raw device profile should be rejected, got %v", err)
	}
}

func TestAlertType(t *testing.T) {
	if got := alertTypeFor(ReportIn{Amount: 0}); got != "advisory" {
		t.Fatalf("zero-value event should be advisory, got %s", got)
	}
	if got := alertTypeFor(ReportIn{Amount: 1500}); got != "review" {
		t.Fatalf("transfer should request receiving-institution review, got %s", got)
	}
}

func TestNormalizeDerivesDeviceAndNetworkCodes(t *testing.T) {
	_, standard, err := loadStandard()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{standard: standard}
	cases := []struct {
		evidence map[string]any
		expected string
	}{
		{map[string]any{"ip_country_changed": true}, "IP-404"},
		{map[string]any{"vpn_proxy_tor": "true"}, "IP-404"},
		{map[string]any{"is_emulator": true}, "IP-403"},
		{map[string]any{"is_rooted": 1}, "IP-403"},
	}
	for _, item := range cases {
		codes, err := app.normalize(ReportIn{Evidence: item.evidence})
		if err != nil {
			t.Fatal(err)
		}
		if !containsString(codes, item.expected) {
			t.Fatalf("expected %s from %v, got %v", item.expected, item.evidence, codes)
		}
	}
	if _, err := app.normalize(ReportIn{Evidence: map[string]any{"vpn_proxy_tor": false}}); err == nil {
		t.Fatal("a false network flag must not produce a risk code")
	}
}

func TestReportFromCSVRejectsCleartextIdentifiers(t *testing.T) {
	if _, err := reportFromCSV(map[string]string{
		"reporting_bank": "NCBA", "receiving_bank": "KCB", "customer_ref": "Jane Wanjiku",
	}); err == nil || !strings.Contains(err.Error(), "customer_ref") {
		t.Fatalf("expected cleartext customer_ref rejection, got %v", err)
	}
	if _, err := reportFromCSV(map[string]string{
		"reporting_bank": "NCBA", "receiving_bank": "KCB", "device_profile": "raw-device-id",
	}); err == nil || !strings.Contains(err.Error(), "device_profile") {
		t.Fatalf("expected cleartext device_profile rejection, got %v", err)
	}
	hash := "sha256:" + strings.Repeat("c", 64)
	report, err := reportFromCSV(map[string]string{
		"reporting_bank": "NCBA", "receiving_bank": "KCB", "transaction_id": "TX-9",
		"subject_customer_hash": hash, "destination_account_hash": hash, "amount": "1200",
		"device_profile": hash, "new_device": "true", "new_beneficiary": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportingInstitution != "bank_a" || report.DestinationInstitution != "bank_b" {
		t.Fatalf("unexpected institutions %s -> %s", report.ReportingInstitution, report.DestinationInstitution)
	}
	if report.SubjectCustomerHash != hash || report.SubjectAccountHash != hash || report.DestinationAccountHash != hash {
		t.Fatal("hashed identifiers must pass through unchanged")
	}
	if report.Evidence["device_profile"] != hash || report.Evidence["is_new_device"] != true ||
		report.Evidence["is_new_beneficiary"] != true || !containsString(report.RiskCodes, "ATO-460") {
		t.Fatalf("CSV evidence was not preserved and derived correctly: %#v %v",
			report.Evidence, report.RiskCodes)
	}
}

func TestKenyanBanksDirectoryIsComplete(t *testing.T) {
	if len(kenyanBanks) != 40 {
		t.Fatalf("expected 37 commercial banks, 1 mortgage finance institution and 2 mobile money providers, got %d", len(kenyanBanks))
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, bank := range kenyanBanks {
		counts[bank.Type]++
		for _, key := range []string{"code:" + bank.Code, "id:" + bank.ID, "name:" + bank.Name, "ref:" + bank.Ref} {
			if seen[key] {
				t.Fatalf("duplicate %s", key)
			}
			seen[key] = true
		}
		if bank.LegalName == "" || bank.Threshold <= 0 {
			t.Fatalf("incomplete entry %+v", bank)
		}
	}
	if counts["bank"] != 37 || counts["mortgage"] != 1 || counts["psp"] != 2 {
		t.Fatalf("unexpected institution types %v", counts)
	}
	for code, name := range map[string]string{"bank_a": "NCBA", "bank_b": "KCB", "psp_c": "Equity Bank", "sacco_d": "I&M Bank"} {
		if got := displayInstitution(code); got != name {
			t.Fatalf("existing data for %s must keep %s, got %s", code, name, got)
		}
	}
}

func TestInstitutionCodeMatchesAnyName(t *testing.T) {
	cases := map[string]string{
		"KCB": "bank_b", "NCBA Bank Kenya PLC": "bank_a", "i&m bank": "sacco_d", " Equity Bank ": "psp_c",
		"COOP": "ke:co-operative-bank-of-kenya", "Premier Bank Limited": "ke:premier-bank",
		"ke:uba-kenya": "ke:uba-kenya", "Unknown Bank": "Unknown Bank",
	}
	for input, expected := range cases {
		if got := institutionCode(input); got != expected {
			t.Fatalf("institutionCode(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestDemoCampaignsRotateThroughEveryBank(t *testing.T) {
	reported := map[string]bool{}
	received := map[string]bool{}
	for campaign := int64(0); campaign < int64(len(kenyanBanks)); campaign++ {
		reporters, destination := demoInstitutions(campaign)
		if reporters[0] == reporters[1] || destination == reporters[0] || destination == reporters[1] {
			t.Fatalf("campaign %d reuses a bank: %v -> %s", campaign, reporters, destination)
		}
		reported[reporters[0]], reported[reporters[1]], received[destination] = true, true, true
	}
	if len(reported) != len(kenyanBanks) || len(received) != len(kenyanBanks) {
		t.Fatalf("every bank should report and receive: reported=%d received=%d of %d",
			len(reported), len(received), len(kenyanBanks))
	}
}

func demoOffsets(t *testing.T, eventType string) (int64, int64) {
	t.Helper()
	for offset := int64(1); offset < 100; offset += 2 {
		if _, _, metadata := demoReport(offset); metadata["event_type"] == eventType {
			return offset, offset + 1
		}
	}
	t.Fatalf("no demo campaign for %s", eventType)
	return 0, 0
}

func TestDemoDeviceSwitchCampaignChangesDeviceAndNetwork(t *testing.T) {
	firstOffset, secondOffset := demoOffsets(t, "device_network_switch")
	first, _, _ := demoReport(firstOffset)
	second, _, _ := demoReport(secondOffset)
	if first.DestinationAccountHash != second.DestinationAccountHash {
		t.Fatal("both institutions should see the same cash-out destination")
	}
	if first.Evidence["device_profile"] == second.Evidence["device_profile"] {
		t.Fatal("the fraudster should appear on a different device at each institution")
	}
	if first.Evidence["ip_country_changed"] != true || second.Evidence["vpn_proxy_tor"] != true {
		t.Fatal("each institution should see a network change")
	}
	simSwap, _, _ := demoReport(1)
	simSwapPartner, _, _ := demoReport(2)
	if simSwap.Evidence["device_profile"] != simSwapPartner.Evidence["device_profile"] {
		t.Fatal("other campaigns keep one device for both institutions")
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
		user_access_requests,auth_sessions,auth_users,demo_events,demo_stream_state,notifications,
		guided_demo_state,alert_actions,audit_log,config,knowledge_base,alerts,artefacts,
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
	var institutionCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM institutions WHERE active=1").Scan(&institutionCount); err != nil {
		t.Fatal(err)
	}
	if institutionCount != len(kenyanBanks)+1 {
		t.Fatalf("expected every bank plus the external network, got %d", institutionCount)
	}

	airtel := demoLoginInstitutions()[len(demoLoginInstitutions())-1]
	var airtelUserID string
	if err := db.QueryRow(ctx, `SELECT user_id FROM auth_users
		WHERE institution_code=$1 AND display_name=$2`,
		airtel.Code, airtel.DemoName).Scan(&airtelUserID); err != nil {
		t.Fatal(err)
	}
	const retiredAirtelEmail = "lucynaserian@legacy.co.ke"
	if _, err := db.Exec(ctx, `UPDATE auth_users SET email=$1 WHERE user_id=$2`,
		retiredAirtelEmail, airtelUserID); err != nil {
		t.Fatal(err)
	}
	if err := app.seedDemoUsers(ctx); err != nil {
		t.Fatal(err)
	}
	var migratedAirtelUserID string
	if err := db.QueryRow(ctx, `SELECT user_id FROM auth_users WHERE email=$1`,
		demoInstitutionEmail(airtel)).Scan(&migratedAirtelUserID); err != nil {
		t.Fatal(err)
	}
	if migratedAirtelUserID != airtelUserID {
		t.Fatalf("email migration changed the Airtel user ID from %q to %q",
			airtelUserID, migratedAirtelUserID)
	}

	var demoUserCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM auth_users").Scan(&demoUserCount); err != nil {
		t.Fatal(err)
	}
	expectedDemoUsers := len(demoLoginInstitutions())*(len(repeatingDemoIdentities)+1) + 1
	if demoUserCount != expectedDemoUsers {
		t.Fatalf("expected %d seeded demo users, got %d",
			expectedDemoUsers, demoUserCount)
	}
	for _, institution := range demoLoginInstitutions() {
		var institutionUserCount int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM auth_users
			WHERE role='institution' AND institution_code=$1`, institution.Code).
			Scan(&institutionUserCount); err != nil {
			t.Fatal(err)
		}
		if institutionUserCount != len(repeatingDemoIdentities)+1 {
			t.Fatalf("%s has %d demo users, want %d", institution.Name,
				institutionUserCount, len(repeatingDemoIdentities)+1)
		}

		identities := append([]demoIdentity{{Key: "primary", Name: institution.DemoName}},
			repeatingDemoIdentities...)
		for _, identity := range identities {
			email := demoEmailForName(identity.Name, institution)
			if identity.Key == "primary" {
				email = demoInstitutionEmail(institution)
			}
			localPart, _, ok := strings.Cut(email, "@")
			if !ok || strings.ContainsAny(localPart, "+-.") {
				t.Fatalf("demo email contains a name separator: %s", email)
			}
			var displayName, institutionCode string
			if err := db.QueryRow(ctx, `SELECT display_name,institution_code
				FROM auth_users WHERE email=$1`, email).
				Scan(&displayName, &institutionCode); err != nil {
				t.Fatalf("missing demo user %s for %s: %v",
					identity.Name, institution.Name, err)
			}
			if displayName != identity.Name || institutionCode != institution.Code {
				t.Fatalf("incorrect demo user for %s: name=%q institution=%q",
					institution.Name, displayName, institutionCode)
			}
		}
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
	if _, err := db.Exec(ctx, `UPDATE auth_users SET password_hash=$1`, string(testHash)); err != nil {
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
	mobileMoney := demoLoginInstitutions()[len(demoLoginInstitutions())-1]
	if mobileMoney.Code != "ke:airtel-money-kenya" {
		t.Fatalf("unexpected final demo institution %q", mobileMoney.Code)
	}
	mobileCookie, _, mobileToken, status := login(
		demoEmailForName(repeatingDemoIdentities[0].Name, mobileMoney),
		testPassword, mobileMoney.Code)
	if status != http.StatusOK || mobileCookie == nil || mobileToken == "" {
		t.Fatalf("mobile-money login failed: status=%d cookie=%v token=%q",
			status, mobileCookie, mobileToken)
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
	if err := app.seedDemoUsers(ctx); err != nil {
		t.Fatal(err)
	}
	var failedAttempts int
	var lockedUntil *time.Time
	if err := db.QueryRow(ctx, `SELECT failed_attempts,locked_until FROM auth_users
		WHERE email='anthonyjordan@ncba.co.ke'`).Scan(&failedAttempts, &lockedUntil); err != nil {
		t.Fatal(err)
	}
	if failedAttempts != 0 || lockedUntil != nil {
		t.Fatalf("demo reseed did not clear lockout: attempts=%d locked_until=%v",
			failedAttempts, lockedUntil)
	}
	if _, err := db.Exec(ctx, `UPDATE auth_users SET password_hash=$1`, string(testHash)); err != nil {
		t.Fatal(err)
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
	institutionWildcard := httptest.NewRequest(http.MethodGet, "/v1/history?institution=*", nil)
	institutionWildcard.Header.Set("Authorization", "Bearer "+equityToken)
	institutionWildcardRecorder := httptest.NewRecorder()
	app.serveHTTP(institutionWildcardRecorder, institutionWildcard)
	if institutionWildcardRecorder.Code != http.StatusForbidden {
		t.Fatalf("institution user should not access ecosystem history, got %d",
			institutionWildcardRecorder.Code)
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
	staffHistory := httptest.NewRequest(http.MethodGet, "/v1/history?institution=*&limit=2000", nil)
	staffHistory.Header.Set("Authorization", "Bearer "+staffToken)
	staffHistoryRecorder := httptest.NewRecorder()
	app.serveHTTP(staffHistoryRecorder, staffHistory)
	if staffHistoryRecorder.Code != http.StatusOK {
		t.Fatalf("staff ecosystem history failed: %d %s",
			staffHistoryRecorder.Code, staffHistoryRecorder.Body.String())
	}

	invalidAliasBody, _ := json.Marshal(map[string]string{"alias": "bad.alias"})
	invalidAliasRequest := httptest.NewRequest(
		http.MethodPost, "/v1/user-requests", bytes.NewReader(invalidAliasBody))
	invalidAliasRequest.Header.Set("Authorization", "Bearer "+equityToken)
	invalidAliasRequest.Header.Set("X-Kifaru-CSRF", equityCSRF)
	invalidAliasRecorder := httptest.NewRecorder()
	app.serveHTTP(invalidAliasRecorder, invalidAliasRequest)
	if invalidAliasRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid alias should fail, got %d %s",
			invalidAliasRecorder.Code, invalidAliasRecorder.Body.String())
	}

	createAccessRequest := func(alias string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"alias": alias})
		request := httptest.NewRequest(
			http.MethodPost, "/v1/user-requests", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+equityToken)
		request.Header.Set("X-Kifaru-CSRF", equityCSRF)
		recorder := httptest.NewRecorder()
		app.serveHTTP(recorder, request)
		var payload struct {
			RequestID string `json:"request_id"`
			Email     string `json:"email"`
		}
		if recorder.Code == http.StatusCreated {
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
		}
		return recorder, payload.RequestID
	}

	accessRequestRecorder, accessRequestID := createAccessRequest("newanalyst9")
	if accessRequestRecorder.Code != http.StatusCreated {
		t.Fatalf("user access request failed: %d %s",
			accessRequestRecorder.Code, accessRequestRecorder.Body.String())
	}
	if !strings.Contains(accessRequestRecorder.Body.String(), `"email":"newanalyst9@equity.co.ke"`) {
		t.Fatalf("request did not derive the Equity domain: %s", accessRequestRecorder.Body.String())
	}
	duplicateAccessRecorder, _ := createAccessRequest("newanalyst9")
	if duplicateAccessRecorder.Code != http.StatusConflict {
		t.Fatalf("duplicate pending request should fail, got %d", duplicateAccessRecorder.Code)
	}
	existingAccountRecorder, _ := createAccessRequest("johnkamau")
	if existingAccountRecorder.Code != http.StatusConflict {
		t.Fatalf("existing account request should fail, got %d %s",
			existingAccountRecorder.Code, existingAccountRecorder.Body.String())
	}

	equityRequests := httptest.NewRequest(http.MethodGet, "/v1/user-requests", nil)
	equityRequests.Header.Set("Authorization", "Bearer "+equityToken)
	equityRequestsRecorder := httptest.NewRecorder()
	app.serveHTTP(equityRequestsRecorder, equityRequests)
	var equityRequestsPayload struct {
		EmailDomain string              `json:"email_domain"`
		Requests    []UserAccessRequest `json:"requests"`
	}
	if err := json.Unmarshal(equityRequestsRecorder.Body.Bytes(), &equityRequestsPayload); err != nil {
		t.Fatal(err)
	}
	if equityRequestsRecorder.Code != http.StatusOK ||
		equityRequestsPayload.EmailDomain != "equity.co.ke" ||
		len(equityRequestsPayload.Requests) != 1 {
		t.Fatalf("unexpected Equity request list: %d %s",
			equityRequestsRecorder.Code, equityRequestsRecorder.Body.String())
	}

	airtelRequests := httptest.NewRequest(http.MethodGet, "/v1/user-requests", nil)
	airtelRequests.Header.Set("Authorization", "Bearer "+mobileToken)
	airtelRequestsRecorder := httptest.NewRecorder()
	app.serveHTTP(airtelRequestsRecorder, airtelRequests)
	var airtelRequestsPayload struct {
		EmailDomain string              `json:"email_domain"`
		Requests    []UserAccessRequest `json:"requests"`
	}
	if err := json.Unmarshal(airtelRequestsRecorder.Body.Bytes(), &airtelRequestsPayload); err != nil {
		t.Fatal(err)
	}
	if airtelRequestsRecorder.Code != http.StatusOK ||
		airtelRequestsPayload.EmailDomain != "airtel.co.ke" ||
		len(airtelRequestsPayload.Requests) != 0 {
		t.Fatalf("tenant request isolation failed: %d %s",
			airtelRequestsRecorder.Code, airtelRequestsRecorder.Body.String())
	}

	institutionReviewBody, _ := json.Marshal(map[string]string{"decision": "approved"})
	institutionReview := httptest.NewRequest(http.MethodPatch,
		"/v1/admin/user-requests/"+accessRequestID, bytes.NewReader(institutionReviewBody))
	institutionReview.Header.Set("Authorization", "Bearer "+equityToken)
	institutionReview.Header.Set("X-Kifaru-CSRF", equityCSRF)
	institutionReviewRecorder := httptest.NewRecorder()
	app.serveHTTP(institutionReviewRecorder, institutionReview)
	if institutionReviewRecorder.Code != http.StatusForbidden {
		t.Fatalf("institution should not review access requests, got %d",
			institutionReviewRecorder.Code)
	}

	staffRequests := httptest.NewRequest(http.MethodGet, "/v1/user-requests", nil)
	staffRequests.Header.Set("Authorization", "Bearer "+staffToken)
	staffRequestsRecorder := httptest.NewRecorder()
	app.serveHTTP(staffRequestsRecorder, staffRequests)
	var staffRequestsPayload struct {
		Requests []UserAccessRequest `json:"requests"`
	}
	if err := json.Unmarshal(staffRequestsRecorder.Body.Bytes(), &staffRequestsPayload); err != nil {
		t.Fatal(err)
	}
	if staffRequestsRecorder.Code != http.StatusOK ||
		len(staffRequestsPayload.Requests) != 1 ||
		staffRequestsPayload.Requests[0].InstitutionCode != "psp_c" {
		t.Fatalf("staff request queue failed: %d %s",
			staffRequestsRecorder.Code, staffRequestsRecorder.Body.String())
	}

	reviewAccessRequest := func(requestID, decision string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"decision": decision})
		request := httptest.NewRequest(http.MethodPatch,
			"/v1/admin/user-requests/"+requestID, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+staffToken)
		request.Header.Set("X-Kifaru-CSRF", staffCSRF)
		recorder := httptest.NewRecorder()
		app.serveHTTP(recorder, request)
		return recorder
	}

	approvedRecorder := reviewAccessRequest(accessRequestID, "approved")
	if approvedRecorder.Code != http.StatusOK {
		t.Fatalf("staff approval failed: %d %s",
			approvedRecorder.Code, approvedRecorder.Body.String())
	}
	secondApprovalRecorder := reviewAccessRequest(accessRequestID, "approved")
	if secondApprovalRecorder.Code != http.StatusConflict {
		t.Fatalf("second approval should fail, got %d", secondApprovalRecorder.Code)
	}
	var admittedUserID, admittedInstitution string
	if err := db.QueryRow(ctx, `SELECT user_id,institution_code FROM auth_users
		WHERE email='newanalyst9@equity.co.ke'`).
		Scan(&admittedUserID, &admittedInstitution); err != nil {
		t.Fatal(err)
	}
	if admittedUserID != "admitted-"+accessRequestID || admittedInstitution != "psp_c" {
		t.Fatalf("incorrect admitted user: id=%q institution=%q",
			admittedUserID, admittedInstitution)
	}
	if _, err := db.Exec(ctx, `UPDATE auth_users SET password_hash=$1 WHERE user_id=$2`,
		string(testHash), admittedUserID); err != nil {
		t.Fatal(err)
	}
	if _, _, admittedToken, status := login(
		"newanalyst9@equity.co.ke", testPassword, "psp_c",
	); status != http.StatusOK || admittedToken == "" {
		t.Fatalf("admitted user cannot sign in: status=%d token=%q", status, admittedToken)
	}

	rejectedRequestRecorder, rejectedRequestID := createAccessRequest("declineduser")
	if rejectedRequestRecorder.Code != http.StatusCreated {
		t.Fatalf("rejection test request failed: %d %s",
			rejectedRequestRecorder.Code, rejectedRequestRecorder.Body.String())
	}
	rejectedRecorder := reviewAccessRequest(rejectedRequestID, "rejected")
	if rejectedRecorder.Code != http.StatusOK {
		t.Fatalf("staff rejection failed: %d %s",
			rejectedRecorder.Code, rejectedRecorder.Body.String())
	}
	var rejectedUserCount int
	if err := db.QueryRow(ctx,
		"SELECT COUNT(*) FROM auth_users WHERE email='declineduser@equity.co.ke'").
		Scan(&rejectedUserCount); err != nil {
		t.Fatal(err)
	}
	if rejectedUserCount != 0 {
		t.Fatal("rejected access request created an account")
	}
	resubmittedRecorder, resubmittedID := createAccessRequest("declineduser")
	if resubmittedRecorder.Code != http.StatusCreated || resubmittedID != rejectedRequestID {
		t.Fatalf("rejected alias should be reusable: %d %s",
			resubmittedRecorder.Code, resubmittedRecorder.Body.String())
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
		Evidence: map[string]any{
			"account_age_days":    3,
			"distinct_senders_7d": 8,
			"is_new_device":       true,
			"is_new_beneficiary":  true,
			"velocity_1h":         6,
		},
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
	if firstStatus != statusAwaiting {
		t.Fatalf("expected first report to await corroboration, got %s", firstStatus)
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
	if firstStatus != statusAwaiting {
		t.Fatalf("different artefact types must not corroborate; got %s", firstStatus)
	}

	differentDestination := first
	differentDestination.ReportingInstitution = "bank_b"
	differentDestination.DestinationInstitution = "bank_a"
	differentDestination.TransactionRef = "TX-DIFFERENT-RECEIVER"
	if _, apiErr := app.process(ctx, differentDestination, "rest"); apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != statusAwaiting {
		t.Fatalf("the same account token at a different receiving institution must not corroborate; got %s", firstStatus)
	}

	second := first
	second.ReportingInstitution = "psp_c"
	second.TransactionRef = "TX-CORRO-2"
	secondResult, apiErr := app.process(ctx, second, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	secondID := secondResult["report_id"].(string)
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != statusCorroborated {
		t.Fatalf("expected automatic revalidation to corroborate the first report, got %s", firstStatus)
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
	if reportCount != 4 {
		t.Fatalf("idempotent retry created another report; count=%d", reportCount)
	}
	var generatedKnownBad int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM knowledge_base
		WHERE list_name='known_bad' AND added_by='Kifaru policy engine'`).
		Scan(&generatedKnownBad); err != nil {
		t.Fatal(err)
	}
	if generatedKnownBad != 0 {
		t.Fatalf("corroboration must not automatically promote a destination to known_bad, got %d", generatedKnownBad)
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
	body, _ = json.Marshal(map[string]string{"state": "acknowledged"})
	request = httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@kcb.co.ke", Role: "institution", InstitutionCode: "bank_b",
	}))
	recorder = httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("receiving institution could not acknowledge alert: %d %s",
			recorder.Code, recorder.Body.String())
	}
	body, _ = json.Marshal(map[string]string{"state": "actioned"})
	request = httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@kcb.co.ke", Role: "institution", InstitutionCode: "bank_b",
	}))
	recorder = httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("actioned alert without an institution outcome should fail, got %d", recorder.Code)
	}
	body, _ = json.Marshal(map[string]string{
		"state": "actioned", "outcome": "held", "comment": "Review hold recorded",
	})
	request = httptest.NewRequest(http.MethodPost, "/v1/alerts/"+alertID+"/state", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "analyst@kcb.co.ke", Role: "institution", InstitutionCode: "bank_b",
	}))
	recorder = httptest.NewRecorder()
	app.setAlertState(recorder, request, alertID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("receiving institution could not record its outcome: %d %s",
			recorder.Code, recorder.Body.String())
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
	var quarantinedStatus, firstLifecycle, linkedStatus, linkedAlertState string
	if err := db.QueryRow(ctx, `SELECT v.status,r.lifecycle_state
		FROM reports r JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		WHERE r.report_id=$1`, firstID).Scan(&quarantinedStatus, &firstLifecycle); err != nil {
		t.Fatal(err)
	}
	if quarantinedStatus != statusQuarantined || firstLifecycle != lifecycleQuarantined {
		t.Fatalf("dispute did not quarantine its source: status=%s lifecycle=%s",
			quarantinedStatus, firstLifecycle)
	}
	if err := db.QueryRow(ctx, `SELECT v.status,a.state
		FROM validations v JOIN alerts a ON a.report_id=v.report_id
		WHERE v.report_id=$1 AND v.is_current=1`, secondID).
		Scan(&linkedStatus, &linkedAlertState); err != nil {
		t.Fatal(err)
	}
	if linkedStatus != statusAwaiting || linkedAlertState != "retracted" {
		t.Fatalf("linked intelligence was not reversed: status=%s alert=%s",
			linkedStatus, linkedAlertState)
	}
	notificationRequest := httptest.NewRequest(http.MethodGet, "/v1/notifications?institution=bank_a", nil)
	notificationRequest = notificationRequest.WithContext(context.WithValue(
		notificationRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@ncba.co.ke", Role: "institution", InstitutionCode: "bank_a",
		}))
	notificationRecorder := httptest.NewRecorder()
	app.notifications(notificationRecorder, notificationRequest)
	if notificationRecorder.Code != http.StatusOK ||
		!strings.Contains(notificationRecorder.Body.String(), `"event_type":"alert.disputed"`) {
		t.Fatalf("reporting institution could not retrieve its notification: %d %s",
			notificationRecorder.Code, notificationRecorder.Body.String())
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

	for query, expected := range map[string]int{"institution=*": 4, "institution=bank_a": 3, "institution=ke:uba-kenya": 0} {
		recorder = httptest.NewRecorder()
		app.history(recorder, httptest.NewRequest(http.MethodGet, "/v1/history?"+query, nil))
		var payload struct {
			History []map[string]any `json:"history"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("history %s failed: %d %s", query, recorder.Code, recorder.Body.String())
		}
		if len(payload.History) != expected {
			t.Fatalf("history %s returned %d rows, want %d", query, len(payload.History), expected)
		}
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
	if firstDemoStatus != statusAwaiting {
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
	if firstDemoStatus != statusCorroborated || firstDemoValidationCount != 2 {
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

	if _, err := db.Exec(ctx, `INSERT INTO demo_events(
		event_offset,topic,partition_key,event_type,source,payload,status,created_at,processed_at
	)
	SELECT generated_offset,'sentinel.security-alert','retention-test','retention-test',
		'unit-test','{}'::jsonb,'processed',NOW(),NOW()
	FROM generate_series(1,$1) AS offsets(generated_offset)`, demoStreamRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE demo_stream_state
		SET enabled=FALSE,next_offset=$1,emitted_since_reset=$2,last_emitted_at=TO_TIMESTAMP(0)
		WHERE singleton=TRUE`, demoStreamRetention+1, demoStreamRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx,
		"DELETE FROM schema_migrations WHERE version='005_rolling_demo_stream.sql'"); err != nil {
		t.Fatal(err)
	}
	if err := app.runMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	var resumed bool
	if err := db.QueryRow(ctx, "SELECT enabled FROM demo_stream_state WHERE singleton=TRUE").
		Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed {
		t.Fatal("rolling-stream migration did not resume a stream stopped at the former cap")
	}
	if _, err := db.Exec(ctx, `UPDATE demo_stream_state SET enabled=FALSE
		WHERE singleton=TRUE`); err != nil {
		t.Fatal(err)
	}
	rollingEvent, emitted, err := app.produceDemoEvent(ctx, false)
	if err != nil || !emitted {
		t.Fatalf("rolling demo event failed: emitted=%v result=%#v err=%v",
			emitted, rollingEvent, err)
	}
	if err := db.QueryRow(ctx, "SELECT enabled FROM demo_stream_state WHERE singleton=TRUE").
		Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed {
		t.Fatal("producer did not recover the exact legacy cap state after a rolling-deploy race")
	}
	var oldestOffset, newestOffset int64
	if err := db.QueryRow(ctx, `SELECT COUNT(*),MIN(event_offset),MAX(event_offset)
		FROM demo_events`).Scan(&demoEventCount, &oldestOffset, &newestOffset); err != nil {
		t.Fatal(err)
	}
	if demoEventCount != demoStreamRetention || oldestOffset != 2 ||
		newestOffset != demoStreamRetention+1 {
		t.Fatalf("rolling retention produced count=%d oldest=%d newest=%d",
			demoEventCount, oldestOffset, newestOffset)
	}
	streamSnapshot, err := app.demoStreamSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(streamSnapshot["retained_events"]) != strconv.Itoa(demoStreamRetention) {
		t.Fatalf("stream snapshot retained count=%v", streamSnapshot["retained_events"])
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/admin/demo-stream/reset", nil)
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, AuthUser{
		Email: "staff@kifaru.co.ke", Role: "staff",
	}))
	recorder = httptest.NewRecorder()
	app.resetDemoStream(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rolling demo reset failed: %d %s", recorder.Code, recorder.Body.String())
	}

	staffUser := AuthUser{Email: "staff@kifaru.co.ke", Role: "staff"}
	advanceGuided := func() map[string]any {
		t.Helper()
		guidedRequest := httptest.NewRequest(http.MethodPost, "/v1/admin/guided-demo/advance", nil)
		guidedRequest = guidedRequest.WithContext(context.WithValue(
			guidedRequest.Context(), authContextKey{}, staffUser,
		))
		guidedRecorder := httptest.NewRecorder()
		app.advanceGuidedDemo(guidedRecorder, guidedRequest)
		if guidedRecorder.Code != http.StatusOK {
			t.Fatalf("guided scenario advance failed: %d %s",
				guidedRecorder.Code, guidedRecorder.Body.String())
		}
		var state map[string]any
		if err := json.Unmarshal(guidedRecorder.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	resetGuided := func() {
		t.Helper()
		guidedRequest := httptest.NewRequest(http.MethodPost, "/v1/admin/guided-demo/reset", nil)
		guidedRequest = guidedRequest.WithContext(context.WithValue(
			guidedRequest.Context(), authContextKey{}, staffUser,
		))
		guidedRecorder := httptest.NewRecorder()
		app.resetGuidedDemo(guidedRecorder, guidedRequest)
		if guidedRecorder.Code != http.StatusOK {
			t.Fatalf("guided scenario reset failed: %d %s",
				guidedRecorder.Code, guidedRecorder.Body.String())
		}
		if !strings.Contains(guidedRecorder.Body.String(), `"status":"ready"`) {
			t.Fatalf("guided scenario did not return to ready: %s", guidedRecorder.Body.String())
		}
	}

	guidedState := advanceGuided()
	guidedFirstID := fmt.Sprint(guidedState["first_report_id"])
	if fmt.Sprint(guidedState["step"]) != "1" || guidedFirstID == "" {
		t.Fatalf("unexpected first guided stage: %#v", guidedState)
	}
	var guidedStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, guidedFirstID).Scan(&guidedStatus); err != nil {
		t.Fatal(err)
	}
	if guidedStatus != statusAwaiting {
		t.Fatalf("first guided signal should await corroboration, got %s", guidedStatus)
	}
	guidedState = advanceGuided()
	guidedSecondID := fmt.Sprint(guidedState["second_report_id"])
	guidedAlertID := fmt.Sprint(guidedState["alert_id"])
	if fmt.Sprint(guidedState["step"]) != "2" || guidedSecondID == "" || guidedAlertID == "" {
		t.Fatalf("unexpected corroborated guided stage: %#v", guidedState)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM validations
		WHERE report_id=$1 AND is_current=1`, guidedSecondID).Scan(&guidedStatus); err != nil {
		t.Fatal(err)
	}
	if guidedStatus != statusCorroborated {
		t.Fatalf("second guided signal should be corroborated, got %s", guidedStatus)
	}
	guidedState = advanceGuided()
	if fmt.Sprint(guidedState["status"]) != "completed" || fmt.Sprint(guidedState["step"]) != "3" {
		t.Fatalf("guided scenario did not complete: %#v", guidedState)
	}
	var guidedAlertState, guidedOutcome string
	if err := db.QueryRow(ctx, "SELECT state,outcome FROM alerts WHERE alert_id=$1", guidedAlertID).
		Scan(&guidedAlertState, &guidedOutcome); err != nil {
		t.Fatal(err)
	}
	if guidedAlertState != "actioned" || guidedOutcome != "held" {
		t.Fatalf("guided receiver outcome was not recorded: state=%s outcome=%s",
			guidedAlertState, guidedOutcome)
	}
	releaseBody, _ := json.Marshal(map[string]string{
		"state": "actioned", "outcome": "released", "comment": "Cleared after synthetic review",
	})
	releaseRequest := httptest.NewRequest(http.MethodPost,
		"/v1/alerts/"+guidedAlertID+"/state", bytes.NewReader(releaseBody))
	releaseRequest = releaseRequest.WithContext(context.WithValue(
		releaseRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@equity.co.ke", Role: "institution", InstitutionCode: "psp_c",
		},
	))
	releaseRecorder := httptest.NewRecorder()
	app.setAlertState(releaseRecorder, releaseRequest, guidedAlertID)
	if releaseRecorder.Code != http.StatusOK {
		t.Fatalf("guided alert release failed: %d %s",
			releaseRecorder.Code, releaseRecorder.Body.String())
	}
	var releasedLifecycle, releasedStatus string
	if err := db.QueryRow(ctx, `SELECT r.lifecycle_state,v.status
		FROM reports r JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		WHERE r.report_id=(SELECT report_id FROM alerts WHERE alert_id=$1)`, guidedAlertID).
		Scan(&releasedLifecycle, &releasedStatus); err != nil {
		t.Fatal(err)
	}
	if releasedLifecycle != lifecycleCleared || releasedStatus != statusCleared {
		t.Fatalf("released alert did not clear its source: lifecycle=%s status=%s",
			releasedLifecycle, releasedStatus)
	}
	reopenBody, _ := json.Marshal(map[string]string{
		"state": "actioned", "outcome": "held", "comment": "Invalid attempt to reopen",
	})
	reopenRequest := httptest.NewRequest(http.MethodPost,
		"/v1/alerts/"+guidedAlertID+"/state", bytes.NewReader(reopenBody))
	reopenRequest = reopenRequest.WithContext(context.WithValue(
		reopenRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@equity.co.ke", Role: "institution", InstitutionCode: "psp_c",
		},
	))
	reopenRecorder := httptest.NewRecorder()
	app.setAlertState(reopenRecorder, reopenRequest, guidedAlertID)
	if reopenRecorder.Code != http.StatusConflict {
		t.Fatalf("released outcome should be terminal, got %d", reopenRecorder.Code)
	}
	disputeBody, _ := json.Marshal(map[string]string{
		"state": "disputed", "comment": "Receiver found conflicting evidence",
	})
	disputeRequest := httptest.NewRequest(http.MethodPost,
		"/v1/alerts/"+guidedAlertID+"/state", bytes.NewReader(disputeBody))
	disputeRequest = disputeRequest.WithContext(context.WithValue(
		disputeRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@equity.co.ke", Role: "institution", InstitutionCode: "psp_c",
		},
	))
	disputeRecorder := httptest.NewRecorder()
	app.setAlertState(disputeRecorder, disputeRequest, guidedAlertID)
	if disputeRecorder.Code != http.StatusOK {
		t.Fatalf("released alert should still allow a later dispute: %d %s",
			disputeRecorder.Code, disputeRecorder.Body.String())
	}
	var lifecycleReasonsRaw string
	if err := db.QueryRow(ctx, `SELECT reason_codes FROM validations
		WHERE report_id=(SELECT report_id FROM alerts WHERE alert_id=$1) AND is_current=1`,
		guidedAlertID).Scan(&lifecycleReasonsRaw); err != nil {
		t.Fatal(err)
	}
	var lifecycleReasons []string
	if err := json.Unmarshal([]byte(lifecycleReasonsRaw), &lifecycleReasons); err != nil {
		t.Fatal(err)
	}
	lifecycleReasonCount := 0
	for _, reason := range lifecycleReasons {
		if strings.HasPrefix(reason, "LIFECYCLE:") {
			lifecycleReasonCount++
		}
	}
	if lifecycleReasonCount != 1 || !containsString(lifecycleReasons, "LIFECYCLE:quarantined") {
		t.Fatalf("current validation retained stale lifecycle reasons: %v", lifecycleReasons)
	}
	resetGuided()
	for _, reportID := range []string{guidedFirstID, guidedSecondID} {
		var count int
		if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM reports WHERE report_id=$1", reportID).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("guided reset retained report %s", reportID)
		}
	}

	guidedState = advanceGuided()
	retractID := fmt.Sprint(guidedState["first_report_id"])
	retractBody, _ := json.Marshal(map[string]string{
		"state": lifecycleRetracted, "comment": "Reporter withdrew the synthetic signal",
	})
	retractRequest := httptest.NewRequest(http.MethodPatch,
		"/v1/reports/"+retractID+"/lifecycle", bytes.NewReader(retractBody))
	retractRequest = retractRequest.WithContext(context.WithValue(
		retractRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@ncba.co.ke", Role: "institution", InstitutionCode: "bank_a",
		},
	))
	retractRecorder := httptest.NewRecorder()
	app.reportLifecycle(retractRecorder, retractRequest, retractID)
	if retractRecorder.Code != http.StatusOK {
		t.Fatalf("reporting institution could not retract its signal: %d %s",
			retractRecorder.Code, retractRecorder.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT lifecycle_state FROM reports WHERE report_id=$1`, retractID).
		Scan(&releasedLifecycle); err != nil {
		t.Fatal(err)
	}
	if releasedLifecycle != lifecycleRetracted {
		t.Fatalf("signal lifecycle is %s after retraction", releasedLifecycle)
	}
	resetGuided()

	guidedState = advanceGuided()
	expireID := fmt.Sprint(guidedState["first_report_id"])
	expireBody, _ := json.Marshal(map[string]string{
		"state": lifecycleExpired, "comment": "Synthetic retention window elapsed",
	})
	expireRequest := httptest.NewRequest(http.MethodPatch,
		"/v1/reports/"+expireID+"/lifecycle", bytes.NewReader(expireBody))
	expireRequest = expireRequest.WithContext(context.WithValue(
		expireRequest.Context(), authContextKey{}, AuthUser{
			Email: "analyst@ncba.co.ke", Role: "institution", InstitutionCode: "bank_a",
		},
	))
	expireRecorder := httptest.NewRecorder()
	app.reportLifecycle(expireRecorder, expireRequest, expireID)
	if expireRecorder.Code != http.StatusForbidden {
		t.Fatalf("institution should not expire a signal, got %d", expireRecorder.Code)
	}
	expireRequest = httptest.NewRequest(http.MethodPatch,
		"/v1/reports/"+expireID+"/lifecycle", bytes.NewReader(expireBody))
	expireRequest = expireRequest.WithContext(context.WithValue(
		expireRequest.Context(), authContextKey{}, staffUser,
	))
	expireRecorder = httptest.NewRecorder()
	app.reportLifecycle(expireRecorder, expireRequest, expireID)
	if expireRecorder.Code != http.StatusOK {
		t.Fatalf("staff could not expire a signal: %d %s",
			expireRecorder.Code, expireRecorder.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT lifecycle_state FROM reports WHERE report_id=$1`, expireID).
		Scan(&releasedLifecycle); err != nil {
		t.Fatal(err)
	}
	if releasedLifecycle != lifecycleExpired {
		t.Fatalf("signal lifecycle is %s after expiry", releasedLifecycle)
	}
	resetGuided()

	advisoryFirst, advisorySecond := demoOffsets(t, "credential_change_advisory")
	for index, offset := range []int64{advisoryFirst, advisorySecond} {
		report, _, _ := demoReport(offset)
		result, apiErr := app.process(ctx, report, "soc_connector")
		if apiErr != nil {
			t.Fatal(apiErr.message)
		}
		status := result["validation"].(Validation).Status
		if index == 0 && status != statusAwaiting {
			t.Fatalf("the first credential-reset report should await corroboration, got %s", status)
		}
		if index == 1 && status != statusCorroborated {
			t.Fatalf("the corroborated credential-reset report should meet policy, got %s", status)
		}
	}
	var advisoryCount int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM alerts WHERE alert_type='advisory'").Scan(&advisoryCount); err != nil {
		t.Fatal(err)
	}
	if advisoryCount == 0 {
		t.Fatal("the credential-reset campaign should produce an advisory alert")
	}

	switchFirst, switchSecond := demoOffsets(t, "device_network_switch")
	firstSwitch, _, _ := demoReport(switchFirst)
	firstSwitchResult, apiErr := app.process(ctx, firstSwitch, "soc_connector")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if status := firstSwitchResult["validation"].(Validation).Status; status != statusAwaiting {
		t.Fatalf("the first device-switch report should await corroboration, got %s", status)
	}
	secondSwitch, _, _ := demoReport(switchSecond)
	secondSwitchResult, apiErr := app.process(ctx, secondSwitch, "soc_connector")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	secondValidation := secondSwitchResult["validation"].(Validation)
	if secondValidation.Status != statusCorroborated || !containsString(secondValidation.ReasonCodes, "LINK:device_switch") {
		t.Fatalf("a device switch should still corroborate and be flagged: %s %v",
			secondValidation.Status, secondValidation.ReasonCodes)
	}
	if !strings.Contains(secondValidation.Explanation, "different device") {
		t.Fatalf("the explanation should mention the device switch: %s", secondValidation.Explanation)
	}
	var revalidatedStatus, revalidatedReasons string
	if err := db.QueryRow(ctx, `SELECT status,reason_codes FROM validations
		WHERE report_id=$1 AND is_current=1`, firstSwitchResult["report_id"]).
		Scan(&revalidatedStatus, &revalidatedReasons); err != nil {
		t.Fatal(err)
	}
	if revalidatedStatus != statusCorroborated || !strings.Contains(revalidatedReasons, "LINK:device_switch") {
		t.Fatalf("the first device-switch report should be revalidated: %s %s", revalidatedStatus, revalidatedReasons)
	}
}
