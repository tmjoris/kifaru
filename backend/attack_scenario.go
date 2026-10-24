package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The attack scenario replays one account-takeover campaign twice: once with
// each institution on its own, and once with the sending banks publishing to
// Kifaru. The banks' own systems are simulated by the frontend; with Kifaru,
// every signal, alert and receiver decision goes through the real pipeline.
const (
	attackFirstSender  = "bank_a"
	attackSecondSender = "bank_b"
	attackReceiver     = "psp_c"
	attackLastStep     = 5

	attackFirstAmount  = 85000
	attackSecondAmount = 150000
)

type attackScenarioKey struct{}

// fromAttackScenario reports whether a report is being submitted by the
// scenario runner, the only caller allowed to set the attack_scenario field.
func fromAttackScenario(ctx context.Context) bool {
	marked, _ := ctx.Value(attackScenarioKey{}).(bool)
	return marked
}

func attackScenarioRun(evidence map[string]any) string {
	run, _ := evidence["attack_scenario"].(string)
	return strings.TrimSpace(run)
}

// attackScenarioStanding is the first bank's track record inside a scenario
// run. It comes from the scenario set-up, never from real receiver outcomes,
// and the second bank has none.
func attackScenarioStanding(ctx context.Context, store dbRunner, runID, institution string) (ReporterStanding, error) {
	var standing ReporterStanding
	if institution != attackFirstSender {
		return standing, nil
	}
	err := store.QueryRow(ctx, `SELECT standing_upheld,standing_adverse
		FROM attack_scenario_state WHERE singleton=TRUE AND run_id=$1`, runID).
		Scan(&standing.Upheld, &standing.Adverse)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReporterStanding{}, nil
	}
	if err != nil {
		return ReporterStanding{}, err
	}
	return standing.evaluated(), nil
}

// attackReport builds a sending bank's report on a transfer into the mule
// account. The attacker reuses one phone at the first bank and switches to a
// new phone at the second, but must cash out through the same account.
func attackReport(runID string, second bool) ReportIn {
	evidence := map[string]any{
		"attack_scenario":     runID,
		"is_new_device":       true,
		"is_new_beneficiary":  true,
		"account_age_days":    3,
		"distinct_senders_7d": 2,
	}
	report := ReportIn{
		ReportingInstitution:   attackFirstSender,
		ReportingSystem:        "Simulated online banking fraud engine",
		TransactionRef:         fmt.Sprintf("ATTACK-%s-1", runID),
		TransactionTimestamp:   utcNow(),
		SubjectAccountHash:     demoHash("attack-victim-account:1:" + runID),
		SubjectCustomerHash:    demoHash("attack-victim:1:" + runID),
		DestinationAccountHash: demoHash("attack-mule:" + runID),
		DestinationInstitution: attackReceiver,
		Amount:                 attackFirstAmount,
		Currency:               "KES",
		Channel:                "instant_payment",
		BankRiskScore:          0.91,
		RiskCodes:              []string{"ATO-460", "MUL-440"},
		Evidence:               evidence,
		Narrative: "Synthetic attack scenario: the customer confirmed they did not make this transfer " +
			"after a second takeover attempt to the same beneficiary was blocked.",
	}
	evidence["victim_confirmed"] = true
	evidence["device_profile"] = demoHash("attack-device:1:" + runID)
	if second {
		delete(evidence, "victim_confirmed")
		evidence["device_profile"] = demoHash("attack-device:2:" + runID)
		report.ReportingInstitution = attackSecondSender
		report.TransactionRef = fmt.Sprintf("ATTACK-%s-2", runID)
		report.SubjectAccountHash = demoHash("attack-victim-account:2:" + runID)
		report.SubjectCustomerHash = demoHash("attack-victim:2:" + runID)
		report.Amount = attackSecondAmount
		report.BankRiskScore = 0.62
		report.Narrative = "Synthetic attack scenario: a new device paid a new beneficiary. " +
			"The score was below the bank's blocking threshold, so the transfer completed."
	}
	return report
}

func (a *App) attackScenarioSnapshot(ctx context.Context) (map[string]any, error) {
	rows, err := a.rows(ctx, `SELECT run_id,mode,step,status,first_report_id,second_report_id,
		first_alert_id,second_alert_id,standing_upheld,standing_adverse,error,updated_at
		FROM attack_scenario_state WHERE singleton=TRUE`)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("attack scenario state is unavailable")
	}
	state := rows[0]
	signals := []map[string]any{}
	for _, reportID := range []string{fmt.Sprint(state["first_report_id"]), fmt.Sprint(state["second_report_id"])} {
		if reportID == "" {
			continue
		}
		signal, err := a.attackSignal(ctx, reportID)
		if err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	state["signals"] = signals
	state["first_sender"] = attackFirstSender
	state["first_sender_name"] = displayInstitution(attackFirstSender)
	state["second_sender"] = attackSecondSender
	state["second_sender_name"] = displayInstitution(attackSecondSender)
	state["receiver"] = attackReceiver
	state["receiver_name"] = displayInstitution(attackReceiver)
	state["first_amount"] = attackFirstAmount
	state["second_amount"] = attackSecondAmount
	return state, nil
}

// attackSignal returns what Kifaru holds for one scenario report: its current
// validation, the alert routed to the receiver, and the receiver's actions.
func (a *App) attackSignal(ctx context.Context, reportID string) (map[string]any, error) {
	var signal struct {
		reporting, submitted, status, explanation, reasons, alertID, state, outcome, tier, issued string
		score                                                                                     float64
		corroboration                                                                             int
	}
	err := a.db.QueryRow(ctx, `SELECT r.reporting_institution,COALESCE(r.submitted_at,''),
		v.status,COALESCE(v.explanation,''),COALESCE(v.reason_codes,'[]'),
		COALESCE(a.alert_id,''),COALESCE(a.state,''),COALESCE(a.outcome,''),
		COALESCE(a.alert_tier,''),COALESCE(a.issued_at,''),
		COALESCE(v.validation_score,0),COALESCE(v.corroboration_count,0)
		FROM reports r
		JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		LEFT JOIN alerts a ON a.report_id=r.report_id
		WHERE r.report_id=$1`, reportID).Scan(
		&signal.reporting, &signal.submitted, &signal.status, &signal.explanation, &signal.reasons,
		&signal.alertID, &signal.state, &signal.outcome, &signal.tier, &signal.issued,
		&signal.score, &signal.corroboration)
	if err != nil {
		return nil, err
	}
	reasons := []string{}
	_ = json.Unmarshal([]byte(signal.reasons), &reasons)
	actions := []map[string]any{}
	if signal.alertID != "" {
		actions, err = a.rows(ctx, `SELECT action,COALESCE(comment,'') AS comment,at
			FROM alert_actions WHERE alert_id=$1 ORDER BY id`, signal.alertID)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"report_id": reportID, "reporting_institution": signal.reporting,
		"reporting_institution_name": displayInstitution(signal.reporting),
		"submitted_at":               signal.submitted, "status": signal.status,
		"score": signal.score, "explanation": signal.explanation, "reason_codes": reasons,
		"corroboration_count": signal.corroboration, "alert_id": signal.alertID,
		"alert_state": signal.state, "alert_outcome": signal.outcome,
		"alert_tier": signal.tier, "alert_issued_at": signal.issued, "actions": actions,
	}, nil
}

func (a *App) attackScenarioStatus(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.attackScenarioSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *App) advanceAttackScenario(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var runID, mode, status, firstReportID, secondReportID, firstAlertID, secondAlertID string
	var step int
	err := a.db.QueryRow(ctx, `SELECT run_id,mode,step,status,first_report_id,second_report_id,
		first_alert_id,second_alert_id FROM attack_scenario_state WHERE singleton=TRUE`).
		Scan(&runID, &mode, &step, &status, &firstReportID, &secondReportID, &firstAlertID, &secondAlertID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "failed" {
		writeError(w, http.StatusConflict, "reset the attack scenario before trying again")
		return
	}
	if step >= attackLastStep {
		writeError(w, http.StatusConflict, "the attack scenario is complete; reset it to run again")
		return
	}
	if runID == "" {
		runID = randomHex(5)
	}
	fail := func(message string) {
		_, _ = a.db.Exec(ctx, `UPDATE attack_scenario_state
			SET status='failed',error=$1,updated_at=NOW() WHERE singleton=TRUE`, message)
		writeError(w, http.StatusInternalServerError, message)
	}
	user := authUserFromContext(ctx)
	// Without Kifaru each bank acts alone, so no step reaches the exchange.
	if mode == "with" {
		scenarioCtx := context.WithValue(ctx, attackScenarioKey{}, true)
		switch step {
		case 1:
			// The first bank blocks a second takeover, the first victim confirms
			// the earlier transfer, and the bank publishes the beneficiary.
			result, apiErr := a.process(scenarioCtx, attackReport(runID, false), "soc_connector")
			if apiErr != nil {
				fail(apiErr.message)
				return
			}
			firstReportID = fmt.Sprint(result["report_id"])
			if alert, ok := result["alert"].(map[string]any); ok {
				firstAlertID = fmt.Sprint(alert["alert_id"])
			}
			if firstAlertID == "" {
				fail("the first bank's signal did not alert the receiving bank")
				return
			}
		case 2:
			if apiErr := a.attackReceiverActs(ctx, user, firstAlertID, "held",
				"Attack scenario: the receiving bank placed a post-no-debit hold on the beneficiary account."); apiErr != nil {
				fail(apiErr.message)
				return
			}
		case 3:
			result, apiErr := a.process(scenarioCtx, attackReport(runID, true), "soc_connector")
			if apiErr != nil {
				fail(apiErr.message)
				return
			}
			secondReportID = fmt.Sprint(result["report_id"])
			if alert, ok := result["alert"].(map[string]any); ok {
				secondAlertID = fmt.Sprint(alert["alert_id"])
			}
			if secondAlertID == "" {
				fail("the second bank's signal was not corroborated")
				return
			}
		case 4:
			if apiErr := a.attackReceiverActs(ctx, user, secondAlertID, "recovered",
				"Attack scenario: the credit landed on a held account and was returned to the sending bank."); apiErr != nil {
				fail(apiErr.message)
				return
			}
			if apiErr := a.attackReceiverActs(ctx, user, firstAlertID, "recovered",
				"Attack scenario: the cash-out was declined and the frozen funds were returned."); apiErr != nil {
				fail(apiErr.message)
				return
			}
		}
	}
	nextStatus := "running"
	if step+1 >= attackLastStep {
		nextStatus = "completed"
	}
	if _, err := a.db.Exec(ctx, `UPDATE attack_scenario_state SET run_id=$1,step=$2,status=$3,
		first_report_id=$4,second_report_id=$5,first_alert_id=$6,second_alert_id=$7,
		error='',updated_at=NOW() WHERE singleton=TRUE`, runID, step+1, nextStatus,
		firstReportID, secondReportID, firstAlertID, secondAlertID); err != nil {
		fail(err.Error())
		return
	}
	snapshot, err := a.attackScenarioSnapshot(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.publishStreamEvent([]string{"*"}, "demo-event", "attack-"+runID, map[string]any{"attack_scenario": step + 1})
	writeJSON(w, http.StatusOK, snapshot)
}

// attackReceiverActs records the receiving bank's decision, moving the alert
// through acknowledgement first when it has not been reviewed yet.
func (a *App) attackReceiverActs(ctx context.Context, user AuthUser, alertID, outcome, comment string) *apiError {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return &apiError{http.StatusInternalServerError, err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state, currentOutcome, receiver string
	if err := tx.QueryRow(ctx, `SELECT state,COALESCE(outcome,''),receiving_institution
		FROM alerts WHERE alert_id=$1`, alertID).Scan(&state, &currentOutcome, &receiver); err != nil {
		return &apiError{http.StatusInternalServerError, err.Error()}
	}
	transitions := [][2]string{}
	if state == "sent" {
		transitions = append(transitions, [2]string{"acknowledged", ""})
	}
	if outcome == "recovered" && currentOutcome != "held" {
		transitions = append(transitions, [2]string{"actioned", "held"})
	}
	transitions = append(transitions, [2]string{"actioned", outcome})
	for _, transition := range transitions {
		if _, _, apiErr := a.applyAlertTransition(ctx, tx, alertID, transition[0], transition[1], comment, user); apiErr != nil {
			return apiErr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return &apiError{http.StatusInternalServerError, err.Error()}
	}
	a.publishAlert(receiver, map[string]any{"alert_id": alertID, "state": "actioned", "outcome": outcome})
	return nil
}

func (a *App) resetAttackScenario(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var request struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if request.Mode != "without" && request.Mode != "with" {
		writeError(w, http.StatusUnprocessableEntity, "mode must be without or with")
		return
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runID string
	if err := tx.QueryRow(ctx, `SELECT run_id FROM attack_scenario_state
		WHERE singleton=TRUE FOR UPDATE`).Scan(&runID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runID != "" {
		scenarioReports := `SELECT report_id FROM reports WHERE transaction_ref LIKE $1`
		queries := []string{
			`DELETE FROM notifications WHERE record_id IN (
				SELECT alert_id FROM alerts WHERE report_id IN (` + scenarioReports + `))`,
			`DELETE FROM alert_actions WHERE alert_id IN (
				SELECT alert_id FROM alerts WHERE report_id IN (` + scenarioReports + `))`,
			`DELETE FROM alerts WHERE report_id IN (` + scenarioReports + `)`,
			`DELETE FROM validations WHERE report_id IN (` + scenarioReports + `)`,
			`DELETE FROM artefacts WHERE report_id IN (` + scenarioReports + `)`,
			`DELETE FROM reports WHERE report_id IN (` + scenarioReports + `)`,
		}
		for _, query := range queries {
			if _, err := tx.Exec(ctx, query, "ATTACK-"+runID+"-%"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE attack_scenario_state SET run_id='',mode=$1,step=0,
		status='ready',first_report_id='',second_report_id='',first_alert_id='',
		second_alert_id='',error='',updated_at=NOW() WHERE singleton=TRUE`, request.Mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := auditRecord(ctx, tx, authUserFromContext(ctx).Email, "attack_scenario.reset",
		runID, "", fmt.Sprintf(`{"mode":%q}`, request.Mode), "reset attack scenario"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot, err := a.attackScenarioSnapshot(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.publishStreamEvent([]string{"*"}, "demo-event", "attack-reset", map[string]any{"attack_scenario": 0})
	writeJSON(w, http.StatusOK, snapshot)
}
