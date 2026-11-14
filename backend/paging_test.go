package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresHistoryPagesBackwardsWithoutGapsOrRepeats(t *testing.T) {
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
	// Eleven reports: bank_a reports 6 (to bank_b), bank_b reports 5 (to bank_a), at distinct times.
	var ids []string
	for i := 0; i < 11; i++ {
		reporter, destination := "bank_a", "bank_b"
		if i%2 == 1 {
			reporter, destination = "bank_b", "bank_a"
		}
		result, apiErr := app.process(ctx, ReportIn{
			ReportingInstitution: reporter, DestinationInstitution: destination, TransactionRef: fmt.Sprintf("PAGE-%02d", i),
			TransactionTimestamp: time.Now().UTC().Format(time.RFC3339), DestinationAccountHash: "sha256:" + strings.Repeat(fmt.Sprintf("%x", i), 64)[:64],
			Amount: 1000, RiskCodes: []string{"MUL-440", "ATO-460", "VEL-429"},
			Evidence: map[string]any{"account_age_days": 3, "distinct_senders_7d": 8, "is_new_device": true, "is_new_beneficiary": true, "velocity_1h": 6},
		}, "rest")
		if apiErr != nil {
			t.Fatal(apiErr.message)
		}
		id := fmt.Sprint(result["report_id"])
		ids = append(ids, id)
		stamp := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format("2006-01-02T15:04:05Z")
		if _, err := db.Exec(ctx, `UPDATE reports SET submitted_at=$1 WHERE report_id=$2`, stamp, id); err != nil {
			t.Fatal(err)
		}
	}
	page := func(institution string, limit int, before string) (idsOut []string, last string) {
		t.Helper()
		body, err := app.historyJSON(ctx, institution, limit, before)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			History []struct {
				ReportID    string `json:"report_id"`
				SubmittedAt string `json:"submitted_at"`
			} `json:"history"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		for _, row := range payload.History {
			idsOut, last = append(idsOut, row.ReportID), row.SubmittedAt
		}
		return
	}
	walk := func(institution string, want int) {
		t.Helper()
		seen, before := map[string]bool{}, ""
		var order []string
		for pages := 0; pages < 20; pages++ {
			got, last := page(institution, 3, before)
			if len(got) == 0 {
				break
			}
			for _, id := range got {
				if seen[id] {
					t.Fatalf("%s: report %s appeared on two pages", institution, id)
				}
				seen[id] = true
				order = append(order, id)
			}
			before = last
		}
		if len(seen) != want {
			t.Fatalf("%s: paging returned %d reports, want %d", institution, len(seen), want)
		}
		for i := 1; i < len(order); i++ { // newest first, so each id was created later than the next
			if indexOf(ids, order[i-1]) < indexOf(ids, order[i]) {
				t.Fatalf("%s: pages are not newest-first at position %d", institution, i)
			}
		}
	}
	walk("*", 11)
	walk("bank_a", 11) // it reported 6 and received 5
	walk("bank_b", 11)
	if got, _ := page("*", 3, ""); len(got) != 3 || got[0] != ids[10] {
		t.Errorf("the first page must start at the newest report, got %v", got)
	}
	if got, _ := page("*", 3, "2025-01-01T00:00:00Z"); len(got) != 0 {
		t.Errorf("nothing is older than the oldest report, got %d", len(got))
	}
}

func indexOf(list []string, value string) int {
	for i, item := range list {
		if item == value {
			return i
		}
	}
	return -1
}
