package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresFastPathAndSlowPath(t *testing.T) {
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
	mule := "sha256:" + strings.Repeat("d", 64)
	report := func(reporter, ref string) ReportIn {
		return ReportIn{
			ReportingInstitution: reporter, DestinationInstitution: "bank_b", TransactionRef: ref,
			TransactionTimestamp: time.Now().UTC().Format(time.RFC3339), DestinationAccountHash: mule,
			Amount: 45000, RiskCodes: []string{"MUL-440", "ATO-460", "VEL-429"},
			Evidence: map[string]any{"account_age_days": 3, "distinct_senders_7d": 8, "is_new_device": true, "is_new_beneficiary": true, "velocity_1h": 6},
		}
	}
	count := func(query string, args ...any) (n int) {
		_ = db.QueryRow(ctx, query, args...).Scan(&n)
		return
	}

	// A lone report: no alert, nothing to re-score, so no transaction is needed.
	first := report("bank_a", "FAST-1")
	done, apiErr := app.processIn(ctx, nil, first, "rest")
	if apiErr != nil {
		t.Fatalf("a lone report must take the fast path, got %v", apiErr.message)
	}
	firstID := done.reportID
	if count(`SELECT count(*) FROM reports WHERE report_id=$1`, firstID) != 1 ||
		count(`SELECT count(*) FROM validations WHERE report_id=$1 AND is_current=1`, firstID) != 1 ||
		count(`SELECT count(*) FROM artefacts WHERE report_id=$1`, firstID) < 1 ||
		count(`SELECT count(*) FROM audit_log WHERE action='validation.created' AND target IN (SELECT validation_id FROM validations WHERE report_id=$1)`, firstID) != 1 {
		t.Fatal("the fast path must store the report, its validation, its artefacts and its audit entry")
	}

	// Sending it again is an idempotent replay, even on the fast path.
	replay, apiErr := app.processIn(ctx, nil, first, "rest")
	if apiErr != nil || replay.result["idempotent_replay"] != true || replay.reportID != firstID {
		t.Fatalf("a repeated reference must replay the stored report: %v %v", apiErr, replay.result)
	}
	if count(`SELECT count(*) FROM reports WHERE transaction_ref='FAST-1'`) != 1 {
		t.Fatal("a replay must not store a second copy")
	}

	// A second institution reporting the same destination corroborates the first:
	// it needs an alert and a re-score of the first report, so it needs a transaction.
	second := report("psp_c", "SLOW-1")
	if _, apiErr := app.processIn(ctx, nil, second, "rest"); apiErr != errSlowPath {
		t.Fatalf("a corroborating report must be sent to the transactional path, got %v", apiErr)
	}
	if count(`SELECT count(*) FROM reports WHERE transaction_ref='SLOW-1'`) != 0 {
		t.Fatal("the fast attempt must not leave anything behind when it hands over")
	}
	result, apiErr := app.process(ctx, second, "rest")
	if apiErr != nil {
		t.Fatal(apiErr.message)
	}
	if fmt.Sprint(result["validation"].(Validation).Status) != statusCorroborated {
		t.Errorf("the second report should be corroborated, got %v", result["validation"].(Validation).Status)
	}
	var firstStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM validations WHERE report_id=$1 AND is_current=1`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != statusCorroborated {
		t.Errorf("the first report must be re-scored as corroborated, got %s", firstStatus)
	}
	if count(`SELECT count(*) FROM alerts`) < 1 {
		t.Error("corroboration must issue an alert")
	}
}
