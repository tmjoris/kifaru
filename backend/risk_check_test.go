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
)

func TestRiskTierForStatus(t *testing.T) {
	for status, tier := range map[string]string{
		statusCorroborated: alertTierCorroborated, statusSingleSourceReview: alertTierReview,
		statusSingleSourceNotice: alertTierNotice, statusAwaiting: "reported",
	} {
		if got := riskTierForStatus(status); got != tier {
			t.Fatalf("%s: expected %s, got %s", status, tier, got)
		}
	}
	signal := RiskSignal{}
	signal.raise(alertTierCorroborated)
	signal.raise("reported")
	if !signal.Flagged || signal.Tier != alertTierCorroborated {
		t.Fatalf("a weaker tier must not replace a stronger one: %+v", signal)
	}
}

func TestPostgresRiskCheck(t *testing.T) {
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

	hashFor := func(digit string) string { return "sha256:" + strings.Repeat(digit, 64) }
	device, destination := hashFor("d"), hashFor("e")
	result, apiErr := app.process(ctx, ReportIn{
		ReportingInstitution:   "bank_a",
		DestinationInstitution: "bank_b",
		TransactionRef:         "TX-RISK-CHECK-001",
		TransactionTimestamp:   time.Now().UTC().Format(time.RFC3339),
		DestinationAccountHash: destination,
		Amount:                 85000,
		RiskCodes:              []string{"ATO-460", "VEL-430"},
		Evidence: map[string]any{
			"is_new_device": true, "is_new_beneficiary": true, "device_profile": device,
		},
	}, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	reportID := result["report_id"].(string)

	type answer struct {
		Device      RiskSignal `json:"device"`
		Destination RiskSignal `json:"destination"`
	}
	check := func(caller, institution string, body map[string]any) (int, answer, string) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, "/v1/risk-check?institution="+institution, bytes.NewReader(encoded))
		request = request.WithContext(context.WithValue(request.Context(), authContextKey{},
			AuthUser{Email: "analyst@" + caller + ".test", Role: "institution", InstitutionCode: caller}))
		recorder := httptest.NewRecorder()
		app.riskCheck(recorder, request)
		var out answer
		_ = json.Unmarshal(recorder.Body.Bytes(), &out)
		return recorder.Code, out, recorder.Body.String()
	}
	both := map[string]any{
		"device_profile": device, "destination_account_hash": destination, "destination_institution": "bank_b",
	}

	// Another institution sees both artefacts flagged, and nothing that names
	// the reporter or the report.
	code, out, body := check("psp_c", "psp_c", both)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	if !out.Device.Flagged || !out.Destination.Flagged || out.Device.Institutions != 1 ||
		out.Destination.Institutions != 1 || out.Destination.Tier != "reported" {
		t.Fatalf("expected both artefacts flagged by one institution: %s", body)
	}
	if strings.Contains(body, "bank_a") || strings.Contains(body, reportID) {
		t.Fatalf("the risk check must not reveal the reporter or report: %s", body)
	}

	// The reporter's own reports never flag its own checks.
	if _, own, _ := check("bank_a", "bank_a", both); own.Device.Flagged || own.Destination.Flagged {
		t.Fatal("an institution's own reports must not flag its risk check")
	}

	// Destination matches stay scoped to the receiving institution.
	if _, other, _ := check("psp_c", "psp_c", map[string]any{
		"destination_account_hash": destination, "destination_institution": "psp_c",
	}); other.Destination.Flagged || other.Device.Checked {
		t.Fatal("the same account hash at another institution must not match")
	}

	// Institution users can only check as their own institution, and only
	// protected identifiers are accepted.
	if code, _, _ := check("psp_c", "bank_b", both); code != http.StatusForbidden {
		t.Fatalf("expected 403 for another institution, got %d", code)
	}
	if code, _, _ := check("psp_c", "psp_c", map[string]any{"device_profile": "android-1234"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a clear device identifier, got %d", code)
	}
	if code, _, _ := check("psp_c", "psp_c", map[string]any{}); code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 with nothing to check, got %d", code)
	}

	// Known-good destinations are never flagged; known-bad ones are flagged
	// even without an active report.
	if err := kbAddWith(ctx, db, destination, "known_good", "payroll account", "staff@kifaru.test"); err != nil {
		t.Fatal(err)
	}
	if _, good, _ := check("psp_c", "psp_c", both); good.Destination.Flagged || !good.Device.Flagged {
		t.Fatal("a known-good destination must not be flagged")
	}
	knownBad := hashFor("f")
	if err := kbAddWith(ctx, db, knownBad, "known_bad", "confirmed mule", "staff@kifaru.test"); err != nil {
		t.Fatal(err)
	}
	if _, bad, _ := check("psp_c", "psp_c", map[string]any{
		"destination_account_hash": knownBad, "destination_institution": "bank_b",
	}); !bad.Destination.Flagged || bad.Destination.Tier != "known_risk" {
		t.Fatalf("a known-bad destination should be flagged as known_risk: %+v", bad.Destination)
	}

	// Inactive reports stop flagging.
	if _, err := db.Exec(ctx, "UPDATE reports SET lifecycle_state=$1 WHERE report_id=$2",
		lifecycleRetracted, reportID); err != nil {
		t.Fatal(err)
	}
	if _, retracted, _ := check("psp_c", "psp_c", both); retracted.Device.Flagged {
		t.Fatal("a retracted report must not flag a device")
	}

	var audited int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE action='risk.check' AND target='psp_c'").
		Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited == 0 {
		t.Fatal("risk checks must be audited")
	}
}
