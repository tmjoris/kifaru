package main

import (
	"context"
	"fmt"
	"net/http"
)

func guidedReport(runID string, phase int) ReportIn {
	reporting := "bank_a"
	transactionSuffix := "A"
	if phase == 1 {
		reporting = "bank_b"
		transactionSuffix = "B"
	}
	destination := "psp_c"
	return ReportIn{
		ReportingInstitution:   reporting,
		ReportingSystem:        "Guided risk operations scenario",
		TransactionRef:         fmt.Sprintf("GUIDED-%s-%s", runID, transactionSuffix),
		TransactionTimestamp:   utcNow(),
		SubjectAccountHash:     demoHash(fmt.Sprintf("guided-subject:%s:%d", runID, phase)),
		SubjectCustomerHash:    demoHash(fmt.Sprintf("guided-customer:%s:%d", runID, phase)),
		DestinationAccountHash: demoHash("guided-beneficiary:" + runID),
		DestinationInstitution: destination,
		Amount:                 125000,
		Currency:               "KES",
		Channel:                "instant_payment",
		BankRiskScore:          0.87,
		RiskCodes:              []string{"ATO-460", "MUL-440"},
		Evidence: map[string]any{
			"guided_demo":         true,
			"is_new_device":       true,
			"is_new_beneficiary":  true,
			"account_age_days":    3,
			"distinct_senders_7d": 8,
			"device_profile":      demoHash(fmt.Sprintf("guided-device:%s:%d", runID, phase)),
		},
		Narrative: "Synthetic guided scenario showing a new beneficiary and mule-account pattern.",
	}
}

func (a *App) guidedDemoSnapshot(ctx context.Context) (map[string]any, error) {
	rows, err := a.rows(ctx, `SELECT run_id,step,status,first_report_id,
		second_report_id,alert_id,error,updated_at FROM guided_demo_state WHERE singleton=TRUE`)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("guided demonstration state is unavailable")
	}
	state := rows[0]
	state["reporting_institution_a"] = "bank_a"
	state["reporting_institution_a_name"] = displayInstitution("bank_a")
	state["reporting_institution_b"] = "bank_b"
	state["reporting_institution_b_name"] = displayInstitution("bank_b")
	state["receiving_institution"] = "psp_c"
	state["receiving_institution_name"] = displayInstitution("psp_c")
	return state, nil
}

func (a *App) guidedDemoStatus(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.guidedDemoSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *App) advanceGuidedDemo(w http.ResponseWriter, r *http.Request) {
	var runID, status, firstReportID, secondReportID, alertID string
	var step int
	err := a.db.QueryRow(r.Context(), `SELECT run_id,step,status,first_report_id,
		second_report_id,alert_id FROM guided_demo_state WHERE singleton=TRUE`).
		Scan(&runID, &step, &status, &firstReportID, &secondReportID, &alertID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "failed" {
		writeError(w, http.StatusConflict, "reset the guided scenario before trying again")
		return
	}
	if step >= 3 {
		writeError(w, http.StatusConflict, "the guided scenario is complete; reset it to run again")
		return
	}
	if runID == "" {
		runID = randomHex(5)
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE guided_demo_state
		SET status='running',error='',updated_at=NOW() WHERE singleton=TRUE`); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fail := func(message string) {
		_, _ = a.db.Exec(r.Context(), `UPDATE guided_demo_state
			SET status='failed',error=$1,updated_at=NOW() WHERE singleton=TRUE`, message)
		writeError(w, http.StatusInternalServerError, message)
	}

	switch step {
	case 0:
		result, apiErr := a.process(r.Context(), guidedReport(runID, 0), "soc_connector")
		if apiErr != nil {
			fail(apiErr.message)
			return
		}
		firstReportID = fmt.Sprint(result["report_id"])
		if _, err := a.db.Exec(r.Context(), `UPDATE guided_demo_state
			SET run_id=$1,step=1,status='running',first_report_id=$2,updated_at=NOW()
			WHERE singleton=TRUE`, runID, firstReportID); err != nil {
			fail(err.Error())
			return
		}
	case 1:
		result, apiErr := a.process(r.Context(), guidedReport(runID, 1), "soc_connector")
		if apiErr != nil {
			fail(apiErr.message)
			return
		}
		secondReportID = fmt.Sprint(result["report_id"])
		if alert, ok := result["alert"].(map[string]any); ok {
			alertID = fmt.Sprint(alert["alert_id"])
		}
		if alertID == "" {
			_ = a.db.QueryRow(r.Context(), `SELECT alert_id FROM alerts
				WHERE report_id IN ($1,$2) ORDER BY issued_at DESC LIMIT 1`,
				firstReportID, secondReportID).Scan(&alertID)
		}
		if alertID == "" {
			fail("the corroborated guided signal did not produce an alert")
			return
		}
		if _, err := a.db.Exec(r.Context(), `UPDATE guided_demo_state
			SET step=2,status='running',second_report_id=$1,alert_id=$2,updated_at=NOW()
			WHERE singleton=TRUE`, secondReportID, alertID); err != nil {
			fail(err.Error())
			return
		}
	case 2:
		tx, err := a.db.Begin(r.Context())
		if err != nil {
			fail(err.Error())
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()
		user := authUserFromContext(r.Context())
		alertRows, err := tx.Query(r.Context(), `SELECT alert_id FROM alerts
			WHERE report_id IN ($1,$2)
			ORDER BY CASE WHEN alert_id=$3 THEN 0 ELSE 1 END, issued_at`,
			firstReportID, secondReportID, alertID)
		if err != nil {
			fail(err.Error())
			return
		}
		alertIDs := []string{}
		for alertRows.Next() {
			var scenarioAlertID string
			if err := alertRows.Scan(&scenarioAlertID); err != nil {
				alertRows.Close()
				fail(err.Error())
				return
			}
			alertIDs = append(alertIDs, scenarioAlertID)
		}
		alertRows.Close()
		if err := alertRows.Err(); err != nil {
			fail(err.Error())
			return
		}
		if len(alertIDs) == 0 {
			fail("the guided scenario has no receiver alerts to action")
			return
		}
		for _, scenarioAlertID := range alertIDs {
			if _, _, apiErr := a.applyAlertTransition(
				r.Context(), tx, scenarioAlertID, "acknowledged", "", "", user,
			); apiErr != nil {
				fail(apiErr.message)
				return
			}
			if _, _, apiErr := a.applyAlertTransition(
				r.Context(), tx, scenarioAlertID, "actioned", "held",
				"Guided scenario: receiving institution recorded a review hold.", user,
			); apiErr != nil {
				fail(apiErr.message)
				return
			}
		}
		if err := auditRecord(r.Context(), tx, user.Email, "guided_demo.completed",
			runID, "", fmt.Sprintf(`{"outcome":"held","alerts":%d}`, len(alertIDs)),
			"deterministic demonstration scenario"); err != nil {
			fail(err.Error())
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			fail(err.Error())
			return
		}
		if _, err := a.db.Exec(r.Context(), `UPDATE guided_demo_state
			SET step=3,status='completed',updated_at=NOW() WHERE singleton=TRUE`); err != nil {
			fail(err.Error())
			return
		}
	}
	snapshot, err := a.guidedDemoSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *App) resetGuidedDemo(w http.ResponseWriter, r *http.Request) {
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var runID, firstReportID, secondReportID string
	if err := tx.QueryRow(r.Context(), `SELECT run_id,first_report_id,second_report_id
		FROM guided_demo_state WHERE singleton=TRUE FOR UPDATE`).
		Scan(&runID, &firstReportID, &secondReportID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	reportIDs := []string{}
	for _, reportID := range []string{firstReportID, secondReportID} {
		if reportID != "" {
			reportIDs = append(reportIDs, reportID)
		}
	}
	if len(reportIDs) > 0 {
		queries := []string{
			`DELETE FROM notifications WHERE record_id IN (
				SELECT alert_id FROM alerts WHERE report_id = ANY($1::text[]))`,
			`DELETE FROM alert_actions WHERE alert_id IN (
				SELECT alert_id FROM alerts WHERE report_id = ANY($1::text[]))`,
			`DELETE FROM alerts WHERE report_id = ANY($1::text[])`,
			`DELETE FROM validations WHERE report_id = ANY($1::text[])`,
			`DELETE FROM artefacts WHERE report_id = ANY($1::text[])`,
			`DELETE FROM reports WHERE report_id = ANY($1::text[])`,
		}
		for _, query := range queries {
			if _, err := tx.Exec(r.Context(), query, reportIDs); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if _, err := tx.Exec(r.Context(), `UPDATE guided_demo_state SET
		run_id='',step=0,status='ready',first_report_id='',second_report_id='',
		alert_id='',error='',updated_at=NOW() WHERE singleton=TRUE`); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := auditRecord(r.Context(), tx, authUserFromContext(r.Context()).Email,
		"guided_demo.reset", runID, "", "{}", "reset deterministic demonstration scenario"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot, err := a.guidedDemoSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
