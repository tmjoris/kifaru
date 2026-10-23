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

func TestVictimConfirmationRequiresCompletedTransfer(t *testing.T) {
	report := ReportIn{
		ReportingInstitution: "bank_a",
		TransactionRef:       "TX-VICTIM",
		TransactionTimestamp: time.Now().UTC().Format(time.RFC3339),
		Evidence:             map[string]any{"victim_confirmed": true},
	}
	if err := validateReport(report); err == nil || !strings.Contains(err.Error(), "victim_confirmed") {
		t.Fatalf("victim confirmation without a transfer should be rejected, got %v", err)
	}
	report.Amount = 1500
	if err := validateReport(report); err != nil {
		t.Fatalf("victim confirmation on a completed transfer was rejected: %v", err)
	}
}

func TestAlertTierRanking(t *testing.T) {
	if alertTierFor(statusAwaiting) != "" || alertTierFor(statusBelow) != "" {
		t.Fatal("non-alerting statuses must not have an alert tier")
	}
	if !(alertTierRank(alertTierFor(statusCorroborated)) > alertTierRank(alertTierFor(statusSingleSourceReview)) &&
		alertTierRank(alertTierFor(statusSingleSourceReview)) > alertTierRank(alertTierFor(statusSingleSourceNotice))) {
		t.Fatal("corroborated alerts must outrank single-source reviews, which outrank notices")
	}
}

func TestPostgresSingleSourceAlertTiers(t *testing.T) {
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

	sequence := 0
	signal := func(reporter, destinationHash string, evidence map[string]any) ReportIn {
		sequence++
		merged := map[string]any{
			"account_age_days":    3,
			"distinct_senders_7d": 8,
			"is_new_device":       true,
			"is_new_beneficiary":  true,
			"velocity_1h":         6,
		}
		for key, value := range evidence {
			merged[key] = value
		}
		return ReportIn{
			ReportingInstitution:   reporter,
			DestinationInstitution: "bank_b",
			TransactionRef:         fmt.Sprintf("TX-TIER-%03d", sequence),
			TransactionTimestamp:   time.Now().UTC().Format(time.RFC3339),
			DestinationAccountHash: destinationHash,
			Amount:                 45000,
			RiskCodes:              []string{"MUL-440", "ATO-460", "VEL-429"},
			Evidence:               merged,
		}
	}
	submit := func(report ReportIn) (string, Validation) {
		t.Helper()
		result, apiErr := app.process(ctx, report, "rest")
		if apiErr != nil {
			t.Fatal(apiErr.message)
		}
		return result["report_id"].(string), result["validation"].(Validation)
	}
	current := func(reportID string) (status, alertID, tier, state string) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT v.status,COALESCE(v.alert_id,''),
			COALESCE(a.alert_tier,''),COALESCE(a.state,'')
			FROM validations v LEFT JOIN alerts a ON a.alert_id=v.alert_id
			WHERE v.report_id=$1 AND v.is_current=1`, reportID).
			Scan(&status, &alertID, &tier, &state); err != nil {
			t.Fatal(err)
		}
		return
	}
	hashFor := func(digit string) string { return "sha256:" + strings.Repeat(digit, 64) }
	// seedOutcomes records receiver decisions on synthetic past alerts so an
	// institution's standing can be set without running a full alert flow.
	seedOutcomes := func(reporter, prefix, outcome string, count int, guided bool) {
		t.Helper()
		evidence := "{}"
		if guided {
			evidence = `{"guided_demo":true}`
		}
		for index := 0; index < count; index++ {
			reportID := "rpt-" + prefix + randomHex(4)
			if _, err := db.Exec(ctx, `INSERT INTO reports(report_id,reporting_institution,
				destination_institution,transaction_ref,evidence,lifecycle_state)
				VALUES ($1,$2,'bank_b',$1,$3,'cleared')`, reportID, reporter, evidence); err != nil {
				t.Fatal(err)
			}
			state := "actioned"
			if outcome == "disputed" {
				state, outcome = "disputed", ""
			}
			if _, err := db.Exec(ctx, `INSERT INTO alerts(alert_id,report_id,reporting_institution,
				receiving_institution,state,outcome) VALUES ($1,$2,$3,'bank_b',$4,$5)`,
				"alt-"+prefix+randomHex(4), reportID, reporter, state, outcome); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Row 4: a single source with no standing and no known-bad hit only waits,
	// even when it reports a victim-confirmed transfer.
	untrustedID, untrusted := submit(signal("bank_a", hashFor("1"), map[string]any{"victim_confirmed": true}))
	if untrusted.Status != statusAwaiting || untrusted.AlertID != nil {
		t.Fatalf("an untrusted single source must await corroboration, got %s", untrusted.Status)
	}
	if !containsString(untrusted.ReasonCodes, "EVIDENCE:victim_confirmed") ||
		containsString(untrusted.ReasonCodes, "TRUST:reporter_good_standing") {
		t.Fatalf("unexpected reason codes for an untrusted victim report: %v", untrusted.ReasonCodes)
	}
	if status, _, _, _ := current(untrustedID); status != statusAwaiting {
		t.Fatalf("stored status should await corroboration, got %s", status)
	}

	// Row 3: a known-bad destination issues a lower-priority notice.
	knownBadHash := hashFor("2")
	if err := kbAddWith(ctx, db, knownBadHash, "known_bad", "confirmed mule", "staff@kifaru.test"); err != nil {
		t.Fatal(err)
	}
	noticeID, notice := submit(signal("bank_a", knownBadHash, nil))
	if notice.Status != statusSingleSourceNotice || notice.AlertID == nil {
		t.Fatalf("a known-bad destination should issue a single-source notice, got %s", notice.Status)
	}
	if !strings.Contains(notice.Explanation, "known-bad list") || !strings.Contains(notice.Explanation, "lower-priority") {
		t.Fatalf("the notice explanation should give its basis: %s", notice.Explanation)
	}
	_, noticeAlertID, noticeTier, _ := current(noticeID)
	if noticeTier != alertTierNotice {
		t.Fatalf("expected a notice-tier alert, got %q", noticeTier)
	}

	// Row 1: corroboration escalates the existing notice in place.
	corroboratorID, _ := submit(signal("psp_c", knownBadHash, nil))
	status, escalatedAlertID, escalatedTier, escalatedState := current(noticeID)
	if status != statusCorroborated || escalatedAlertID != noticeAlertID ||
		escalatedTier != alertTierCorroborated || escalatedState != "sent" {
		t.Fatalf("corroboration should escalate the notice in place: status=%s alert=%s/%s tier=%s state=%s",
			status, escalatedAlertID, noticeAlertID, escalatedTier, escalatedState)
	}
	var alertCount, escalations int
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM alerts WHERE report_id=$1", noticeID).Scan(&alertCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM notifications
		WHERE event_type='alert.escalated' AND record_id=$1`, noticeAlertID).Scan(&escalations); err != nil {
		t.Fatal(err)
	}
	if alertCount != 1 || escalations == 0 {
		t.Fatalf("escalation should keep one alert and notify: alerts=%d escalations=%d", alertCount, escalations)
	}

	// Losing corroboration downgrades back to a notice rather than retracting.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.deactivateReport(ctx, tx, corroboratorID, lifecycleRetracted,
		"reported in error", "analyst@equity.test", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	status, downgradedAlertID, downgradedTier, downgradedState := current(noticeID)
	if status != statusSingleSourceNotice || downgradedAlertID != noticeAlertID ||
		downgradedTier != alertTierNotice || downgradedState != "sent" {
		t.Fatalf("losing corroboration should downgrade to a notice: status=%s tier=%s state=%s",
			status, downgradedTier, downgradedState)
	}

	// Scripted guided-scenario outcomes never build standing.
	seedOutcomes("sacco_d", "guided-", "held", 6, true)
	if standing, err := reporterStanding(ctx, db, "sacco_d"); err != nil || standing.Good || standing.Upheld != 0 {
		t.Fatalf("guided outcomes must not count toward standing: %+v %v", standing, err)
	}

	// Rows 2 and 3: a reporter in good standing.
	seedOutcomes("sacco_d", "upheld-", "held", 5, false)
	standing, err := reporterStanding(ctx, db, "sacco_d")
	if err != nil || !standing.Good || standing.Upheld != 5 {
		t.Fatalf("five upheld outcomes should earn standing: %+v %v", standing, err)
	}
	trustedNoticeID, trustedNotice := submit(signal("sacco_d", hashFor("3"), nil))
	if trustedNotice.Status != statusSingleSourceNotice ||
		!containsString(trustedNotice.ReasonCodes, "TRUST:reporter_good_standing") {
		t.Fatalf("a heuristic flag from a trusted reporter should be a notice: %s %v",
			trustedNotice.Status, trustedNotice.ReasonCodes)
	}
	reviewID, review := submit(signal("sacco_d", hashFor("4"), map[string]any{"victim_confirmed": true}))
	if review.Status != statusSingleSourceReview {
		t.Fatalf("a victim-confirmed report from a trusted reporter should be a review, got %s", review.Status)
	}
	if !strings.Contains(review.Explanation, "upheld 5 of its 5") {
		t.Fatalf("the review explanation should record the standing: %s", review.Explanation)
	}
	var reviewTier, reviewType string
	if err := db.QueryRow(ctx, "SELECT alert_tier,alert_type FROM alerts WHERE report_id=$1", reviewID).
		Scan(&reviewTier, &reviewType); err != nil {
		t.Fatal(err)
	}
	if reviewTier != alertTierReview || reviewType != "review" {
		t.Fatalf("expected a single-source review alert, got tier=%s type=%s", reviewTier, reviewType)
	}

	// Standing never overrides a known-good destination.
	knownGoodHash := hashFor("5")
	if err := kbAddWith(ctx, db, knownGoodHash, "known_good", "payroll account", "staff@kifaru.test"); err != nil {
		t.Fatal(err)
	}
	_, suppressed := submit(signal("sacco_d", knownGoodHash, map[string]any{"victim_confirmed": true}))
	if suppressed.AlertID != nil || alertTierFor(suppressed.Status) != "" {
		t.Fatalf("a known-good destination must not alert on a single source, got %s", suppressed.Status)
	}

	// The guided scenario always demonstrates the corroboration path.
	_, guided := submit(signal("sacco_d", hashFor("6"), map[string]any{"guided_demo": true}))
	if guided.Status != statusAwaiting {
		t.Fatalf("guided reports must not use reporter standing, got %s", guided.Status)
	}

	// Releases and disputes erode standing; revalidation then retracts the notice.
	seedOutcomes("sacco_d", "released-", "released", 1, false)
	seedOutcomes("sacco_d", "disputed-", "disputed", 1, false)
	if standing, err := reporterStanding(ctx, db, "sacco_d"); err != nil || standing.Good || standing.Adverse != 2 {
		t.Fatalf("two adverse outcomes in seven should lose standing: %+v %v", standing, err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := getConfigFrom(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := app.recalculateLinkedReport(ctx, tx, trustedNoticeID, cfg, "staff@kifaru.test", "manual revalidation")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0]["state"] != "retracted" {
		t.Fatalf("losing standing should retract the notice, got %#v", changed)
	}
	var retractionCause string
	if err := db.QueryRow(ctx, `SELECT payload::jsonb->>'cause' FROM notifications
		WHERE event_type='alert.retracted' AND record_id=$1 LIMIT 1`, changed[0]["alert_id"]).
		Scan(&retractionCause); err != nil {
		t.Fatal(err)
	}
	if retractionCause != "alert_policy_no_longer_met" {
		t.Fatalf("unexpected retraction cause %q", retractionCause)
	}
	if status, alertID, _, _ := current(trustedNoticeID); status != statusAwaiting || alertID != "" {
		t.Fatalf("the report should await corroboration without an alert, got %s %s", status, alertID)
	}
}
