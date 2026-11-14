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

func TestPostgresPruneRemovesOnlyOldReportsAndTheirChildren(t *testing.T) {
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
	submit := func(ref string, evidence map[string]any) string {
		merged := map[string]any{"account_age_days": 3, "distinct_senders_7d": 8, "is_new_device": true, "is_new_beneficiary": true, "velocity_1h": 6}
		for k, v := range evidence {
			merged[k] = v
		}
		result, apiErr := app.process(ctx, ReportIn{
			ReportingInstitution: "bank_a", DestinationInstitution: "bank_b", TransactionRef: ref,
			TransactionTimestamp: time.Now().UTC().Format(time.RFC3339), DestinationAccountHash: "sha256:" + strings.Repeat("c", 64),
			Amount: 45000, RiskCodes: []string{"MUL-440", "ATO-460", "VEL-429"}, Evidence: merged,
		}, "rest")
		if apiErr != nil {
			t.Fatal(apiErr.message)
		}
		return fmt.Sprint(result["report_id"])
	}
	old, recent := submit("OLD", nil), submit("RECENT", nil)
	for _, id := range []string{old} {
		if _, err := db.Exec(ctx, `UPDATE reports SET expires_at=NOW()-INTERVAL '90 days' WHERE report_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := app.pruneOnce(ctx, 30)
	if err != nil || removed != 1 {
		t.Fatalf("removed %d (err %v), want only the one report expired 90 days ago", removed, err)
	}
	count := func(query string, args ...any) (n int) {
		_ = db.QueryRow(ctx, query, args...).Scan(&n)
		return
	}
	if count(`SELECT count(*) FROM reports WHERE report_id=$1`, old) != 0 || count(`SELECT count(*) FROM validations WHERE report_id=$1`, old) != 0 ||
		count(`SELECT count(*) FROM artefacts WHERE report_id=$1`, old) != 0 {
		t.Error("the old report and its validation and artefacts must all be gone")
	}
	if count(`SELECT count(*) FROM reports WHERE report_id=$1`, recent) != 1 || count(`SELECT count(*) FROM validations WHERE report_id=$1`, recent) != 1 {
		t.Error("a report inside its retention period must be kept whole")
	}
	if removed, _ := app.pruneOnce(ctx, 30); removed != 0 {
		t.Errorf("a second pass removed %d, want nothing left to prune", removed)
	}
}
