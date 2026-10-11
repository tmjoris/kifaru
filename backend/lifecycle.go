package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func lifecycleValidationStatus(state string) string {
	switch state {
	case lifecycleQuarantined:
		return statusQuarantined
	case lifecycleRetracted:
		return statusRetracted
	case lifecycleExpired:
		return statusExpired
	case lifecycleCleared:
		return statusCleared
	default:
		return statusAwaiting
	}
}

func lifecycleExplanation(state, reason string) string {
	detail := strings.TrimSpace(reason)
	switch state {
	case lifecycleQuarantined:
		return "The receiving institution disputed this signal. It is quarantined and no longer contributes corroboration." +
			valueOrSentence(detail)
	case lifecycleRetracted:
		return "The reporting institution retracted this signal. It no longer contributes corroboration." +
			valueOrSentence(detail)
	case lifecycleExpired:
		return "This signal expired and no longer contributes corroboration." + valueOrSentence(detail)
	case lifecycleCleared:
		return "The receiving institution reviewed and released the activity. This signal no longer contributes corroboration." +
			valueOrSentence(detail)
	default:
		return "The signal is inactive."
	}
}

func valueOrSentence(value string) string {
	if value == "" {
		return ""
	}
	return " Note: " + value
}

func insertNotification(
	ctx context.Context,
	store dbRunner,
	institution, eventType, recordID string,
	payload map[string]any,
) error {
	encoded, _ := json.Marshal(payload)
	_, err := store.Exec(ctx, `INSERT INTO notifications(
		institution_code,event_type,record_id,created_at,payload
	) VALUES ($1,$2,$3,$4,$5)`, institution, eventType, recordID, utcNow(), string(encoded))
	return err
}

func (a *App) notifications(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	filter := "WHERE institution_code=$1"
	if institution == "*" {
		filter = "WHERE $1::text IS NOT NULL"
	}
	rows, err := a.rows(r.Context(), `SELECT id,institution_code,event_type,record_id,
		created_at,read_at,payload FROM notifications `+filter+`
		ORDER BY id DESC LIMIT $2`, institution, queryLimit(r, 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"institution": institution, "notifications": rows})
}

func (a *App) applyAlertTransition(
	ctx context.Context,
	tx pgx.Tx,
	alertID, state, outcome, comment string,
	user AuthUser,
) (map[string]any, []map[string]any, *apiError) {
	state = strings.ToLower(strings.TrimSpace(state))
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	comment = strings.TrimSpace(comment)
	if !containsString([]string{"acknowledged", "actioned", "disputed"}, state) {
		return nil, nil, &apiError{http.StatusUnprocessableEntity, "invalid alert state"}
	}
	if state == "disputed" && comment == "" {
		return nil, nil, &apiError{http.StatusUnprocessableEntity, "a dispute comment is required"}
	}
	if state == "actioned" && !containsString([]string{"held", "released", "recovered"}, outcome) {
		return nil, nil, &apiError{http.StatusUnprocessableEntity,
			"an actioned alert requires held, released or recovered as its outcome"}
	}

	var oldState, oldOutcome, reportingInstitution, receivingInstitution, reportID string
	err := tx.QueryRow(ctx, `SELECT state,outcome,reporting_institution,receiving_institution,report_id
		FROM alerts WHERE alert_id=$1 FOR UPDATE`, alertID).
		Scan(&oldState, &oldOutcome, &reportingInstitution, &receivingInstitution, &reportID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, &apiError{http.StatusNotFound, "unknown alert"}
	}
	if err != nil {
		return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	if user.Role == "institution" && user.InstitutionCode != receivingInstitution {
		return nil, nil, &apiError{http.StatusForbidden,
			"only the receiving institution can update this alert"}
	}
	if oldState == state && oldOutcome == outcome {
		return map[string]any{
			"alert_id": alertID, "state": state, "previous_state": oldState,
			"outcome": outcome, "previous_outcome": oldOutcome, "comment": comment,
			"receiving_institution": receivingInstitution,
			"reporting_institution": reportingInstitution,
			"idempotent":            true,
		}, nil, nil
	}
	actionedOutcomeTransition := oldState == "actioned" && state == "actioned" &&
		oldOutcome == "held" && (outcome == "released" || outcome == "recovered")
	validTransition := (oldState == "sent" && (state == "acknowledged" || state == "disputed")) ||
		(oldState == "acknowledged" && (state == "actioned" || state == "disputed")) ||
		(oldState == "actioned" && state == "disputed") ||
		actionedOutcomeTransition
	if !validTransition {
		return nil, nil, &apiError{http.StatusConflict,
			fmt.Sprintf("cannot move alert from %s to %s", oldState, state)}
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts
		SET state=$1,outcome=$2,outcome_note=$3,updated_at=NOW()
		WHERE alert_id=$4`, state, outcome, comment, alertID); err != nil {
		return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	action := state
	if outcome != "" {
		action += ":" + outcome
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,comment,actor,at)
		VALUES ($1,$2,$3,$4,$5)`, alertID, action, nullIfEmpty(comment), user.Email, utcNow()); err != nil {
		return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
	}

	payload := map[string]any{
		"alert_id": alertID, "state": state, "outcome": outcome, "comment": comment,
		"reporting_institution": reportingInstitution, "receiving_institution": receivingInstitution,
	}
	if state == "disputed" || state == "actioned" {
		eventType := "alert." + state
		if outcome != "" {
			eventType += "." + outcome
		}
		if err := insertNotification(ctx, tx, reportingInstitution, eventType, alertID, payload); err != nil {
			return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
	}

	changedAlerts := []map[string]any{}
	if state == "disputed" {
		changed, err := a.deactivateReport(
			ctx, tx, reportID, lifecycleQuarantined, comment, user.Email, alertID,
		)
		if err != nil {
			return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
		changedAlerts = append(changedAlerts, changed...)
	}
	if state == "actioned" && outcome == "released" {
		changed, err := a.deactivateReport(
			ctx, tx, reportID, lifecycleCleared, comment, user.Email, alertID,
		)
		if err != nil {
			return nil, nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
		changedAlerts = append(changedAlerts, changed...)
	}
	return map[string]any{
		"alert_id": alertID, "state": state, "previous_state": oldState,
		"outcome": outcome, "previous_outcome": oldOutcome, "comment": comment,
		"receiving_institution": receivingInstitution,
		"reporting_institution": reportingInstitution,
	}, changedAlerts, nil
}

func (a *App) reportLifecycle(w http.ResponseWriter, r *http.Request, reportID string) {
	var request struct {
		State   string `json:"state"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	request.State = strings.ToLower(strings.TrimSpace(request.State))
	request.Comment = strings.TrimSpace(request.Comment)
	if !containsString([]string{lifecycleRetracted, lifecycleExpired}, request.State) {
		writeError(w, http.StatusUnprocessableEntity, "state must be retracted or expired")
		return
	}
	if request.Comment == "" {
		writeError(w, http.StatusUnprocessableEntity, "a lifecycle change comment is required")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var reportingInstitution, currentState string
	err = tx.QueryRow(r.Context(), `SELECT reporting_institution,lifecycle_state
		FROM reports WHERE report_id=$1 FOR UPDATE`, reportID).Scan(&reportingInstitution, &currentState)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "unknown report")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user := authUserFromContext(r.Context())
	if user.Role == "institution" && user.InstitutionCode != reportingInstitution {
		writeError(w, http.StatusForbidden, "only the reporting institution can retract this signal")
		return
	}
	if request.State == lifecycleExpired && user.Role != "staff" {
		writeError(w, http.StatusForbidden, "staff access is required to expire a signal")
		return
	}
	if currentState != lifecycleActive {
		writeError(w, http.StatusConflict, fmt.Sprintf("signal is already %s", currentState))
		return
	}
	changedAlerts, err := a.deactivateReport(
		r.Context(), tx, reportID, request.State, request.Comment, user.Email, "",
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, alert := range changedAlerts {
		a.publishAlert(fmt.Sprint(alert["receiving_institution"]), alert)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report_id": reportID, "lifecycle_state": request.State,
		"linked_alerts_updated": len(changedAlerts),
	})
}

func (a *App) deactivateReport(
	ctx context.Context,
	tx pgx.Tx,
	reportID, lifecycleState, reason, actor, preserveAlertID string,
) ([]map[string]any, error) {
	linkedRows, err := tx.Query(ctx, `SELECT DISTINCT other.report_id
		FROM artefacts source
		JOIN artefacts other
		  ON other.artefact_type=source.artefact_type
		 AND other.artefact_hash=source.artefact_hash
		 AND other.match_scope=source.match_scope
		JOIN reports other_report ON other_report.report_id=other.report_id
		WHERE source.report_id=$1
		  AND other.report_id<>$1
		  AND other_report.lifecycle_state=$2`, reportID, lifecycleActive)
	if err != nil {
		return nil, err
	}
	linkedIDs := []string{}
	for linkedRows.Next() {
		var linkedID string
		if err := linkedRows.Scan(&linkedID); err != nil {
			linkedRows.Close()
			return nil, err
		}
		linkedIDs = append(linkedIDs, linkedID)
	}
	linkedRows.Close()
	if err := linkedRows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `UPDATE reports
		SET lifecycle_state=$1,
			expires_at=CASE WHEN $1=$3 THEN NOW() ELSE expires_at END
		WHERE report_id=$2`,
		lifecycleState, reportID, lifecycleExpired); err != nil {
		return nil, err
	}
	if err := a.insertLifecycleValidation(ctx, tx, reportID, lifecycleState, reason, actor); err != nil {
		return nil, err
	}

	changedAlerts := []map[string]any{}
	alertRows, err := rowsFrom(ctx, tx, `SELECT alert_id,receiving_institution,reporting_institution,state
		FROM alerts WHERE report_id=$1`, reportID)
	if err != nil {
		return nil, err
	}
	for _, alert := range alertRows {
		if fmt.Sprint(alert["alert_id"]) == preserveAlertID {
			continue
		}
		if fmt.Sprint(alert["state"]) == "disputed" {
			continue
		}
		changed, err := retractAlert(ctx, tx, fmt.Sprint(alert["alert_id"]),
			fmt.Sprint(alert["receiving_institution"]), fmt.Sprint(alert["reporting_institution"]),
			lifecycleState, reason, actor)
		if err != nil {
			return nil, err
		}
		changedAlerts = append(changedAlerts, changed)
	}

	cfg, err := getConfigFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, linkedID := range linkedIDs {
		changed, err := a.recalculateLinkedReport(ctx, tx, linkedID, cfg, actor,
			"linked signal became "+lifecycleState)
		if err != nil {
			return nil, err
		}
		changedAlerts = append(changedAlerts, changed...)
	}
	return changedAlerts, nil
}

func (a *App) insertLifecycleValidation(
	ctx context.Context,
	tx pgx.Tx,
	reportID, lifecycleState, reason, actor string,
) error {
	var previousID, previousStatus, reasonsRaw, corroboratingRaw, alertID string
	var previousScore float64
	var corroborationCount int
	err := tx.QueryRow(ctx, `SELECT validation_id,status,validation_score,reason_codes,
		corroborating_institutions,corroboration_count,COALESCE(alert_id,'')
		FROM validations WHERE report_id=$1 AND is_current=1
		ORDER BY validated_at DESC LIMIT 1`, reportID).
		Scan(&previousID, &previousStatus, &previousScore, &reasonsRaw,
			&corroboratingRaw, &corroborationCount, &alertID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var reasons []string
	_ = json.Unmarshal([]byte(reasonsRaw), &reasons)
	reasons = slices.DeleteFunc(reasons, func(reason string) bool {
		return strings.HasPrefix(reason, "LIFECYCLE:")
	})
	reasons = uniqueSorted(append(reasons, "LIFECYCLE:"+lifecycleState))
	encodedReasons, _ := json.Marshal(reasons)
	if _, err := tx.Exec(ctx, "UPDATE validations SET is_current=0 WHERE validation_id=$1", previousID); err != nil {
		return err
	}
	validationID := "val-" + randomHex(5)
	if _, err := tx.Exec(ctx, `INSERT INTO validations (
		validation_id,report_id,validated_at,agent_version,validation_score,status,
		reason_codes,corroborating_institutions,corroboration_count,explanation,
		latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,$11,
		COALESCE((SELECT MAX(configuration_version) FROM validations WHERE report_id=$2),1),$12,1)`,
		validationID, reportID, utcNow(), policyVersion, previousScore,
		lifecycleValidationStatus(lifecycleState), string(encodedReasons), corroboratingRaw,
		corroborationCount, lifecycleExplanation(lifecycleState, reason), nullIfEmpty(alertID), previousID); err != nil {
		return err
	}
	oldValue, _ := json.Marshal(map[string]any{"status": previousStatus, "lifecycle_state": lifecycleActive})
	newValue, _ := json.Marshal(map[string]any{
		"status": lifecycleValidationStatus(lifecycleState), "lifecycle_state": lifecycleState,
	})
	return auditRecord(ctx, tx, actor, "signal.lifecycle", reportID,
		string(oldValue), string(newValue), reason)
}

func (a *App) recalculateLinkedReport(
	ctx context.Context,
	tx pgx.Tx,
	reportID string,
	cfg map[string]any,
	actor, reason string,
) ([]map[string]any, error) {
	reportRows, err := rowsFrom(ctx, tx, "SELECT * FROM reports WHERE report_id=$1", reportID)
	if err != nil || len(reportRows) == 0 {
		return nil, err
	}
	report, codes, err := reportFromRow(reportRows[0])
	if err != nil {
		return nil, err
	}
	var previousID, previousStatus, previousAlertID string
	var previousScore float64
	err = tx.QueryRow(ctx, `SELECT validation_id,status,validation_score,COALESCE(alert_id,'')
		FROM validations WHERE report_id=$1 AND is_current=1`,
		reportID).Scan(&previousID, &previousStatus, &previousScore, &previousAlertID)
	if err != nil {
		return nil, err
	}
	validation, err := a.score(ctx, tx, report, reportID, codes, cfg, time.Now())
	if err != nil {
		return nil, err
	}
	validation.Explanation = a.explain(report, validation, codes)
	newAlert := assignAlertID(&validation, previousAlertID)
	if _, err := tx.Exec(ctx, "UPDATE validations SET is_current=0 WHERE validation_id=$1", previousID); err != nil {
		return nil, err
	}
	reasons, _ := json.Marshal(validation.ReasonCodes)
	corroborating, _ := json.Marshal(validation.CorroboratingInstitutions)
	if _, err := tx.Exec(ctx, `INSERT INTO validations (
		validation_id,report_id,validated_at,agent_version,validation_score,status,
		reason_codes,corroborating_institutions,corroboration_count,explanation,
		latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,1)`,
		validation.ValidationID, reportID, validation.ValidatedAt, validation.AgentVersion,
		validation.ValidationScore, validation.Status, string(reasons), string(corroborating),
		validation.CorroborationCount, validation.Explanation, validation.LatencyMS,
		validation.AlertID, int(numberOr(cfg["configuration_version"], 1)), previousID); err != nil {
		return nil, err
	}
	oldValue, _ := json.Marshal(map[string]any{"status": previousStatus, "score": previousScore})
	newValue, _ := json.Marshal(map[string]any{"status": validation.Status, "score": validation.ValidationScore})
	if err := auditRecord(ctx, tx, actor, "validation.recalculated",
		validation.ValidationID, string(oldValue), string(newValue), reason); err != nil {
		return nil, err
	}

	return reconcileAlert(ctx, tx, report, reportID, codes, validation, previousAlertID, newAlert, actor, reason)
}

// assignAlertID links an alerting validation to the report's existing alert,
// or to a new alert ID. It reports whether a new alert must be inserted.
func assignAlertID(validation *Validation, previousAlertID string) bool {
	if alertTierFor(validation.Status) == "" {
		return false
	}
	if previousAlertID != "" {
		validation.AlertID = &previousAlertID
		return false
	}
	alertID := "alt-" + randomHex(5)
	validation.AlertID = &alertID
	return true
}

// reconcileAlert brings a report's receiver alert in line with its newest
// validation. It inserts a new alert, moves an open alert to the new tier, or
// retracts an open alert that the policy no longer supports. Disputed and
// retracted alerts are closed and left unchanged.
func reconcileAlert(
	ctx context.Context,
	tx pgx.Tx,
	report ReportIn,
	reportID string,
	codes []string,
	validation Validation,
	previousAlertID string,
	newAlert bool,
	actor, reason string,
) ([]map[string]any, error) {
	if newAlert {
		alert, err := insertSignalAlert(ctx, tx, report, reportID, codes, validation)
		if err != nil {
			return nil, err
		}
		return []map[string]any{alert}, nil
	}
	if previousAlertID == "" {
		return nil, nil
	}
	alertRows, err := rowsFrom(ctx, tx, `SELECT receiving_institution,reporting_institution,state,alert_tier
		FROM alerts WHERE alert_id=$1`, previousAlertID)
	if err != nil || len(alertRows) == 0 {
		return nil, err
	}
	previous := alertRows[0]
	state := fmt.Sprint(previous["state"])
	if state == "disputed" || state == "retracted" {
		return nil, nil
	}
	receiving := fmt.Sprint(previous["receiving_institution"])
	reporting := fmt.Sprint(previous["reporting_institution"])
	previousTier := fmt.Sprint(previous["alert_tier"])
	tier := alertTierFor(validation.Status)
	if tier == "" {
		cause := "alert_policy_no_longer_met"
		if previousTier == alertTierCorroborated {
			cause = "corroboration_removed"
		}
		changed, err := retractAlert(ctx, tx, previousAlertID, receiving, reporting, cause, reason, actor)
		if err != nil {
			return nil, err
		}
		return []map[string]any{changed}, nil
	}
	if tier == previousTier {
		return nil, nil
	}
	direction := "downgraded"
	if alertTierRank(tier) > alertTierRank(previousTier) {
		direction = "escalated"
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts
		SET alert_tier=$1,validation_id=$2,validation_score=$3,explanation=$4,updated_at=NOW()
		WHERE alert_id=$5`, tier, validation.ValidationID, validation.ValidationScore,
		validation.Explanation, previousAlertID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,comment,actor,at)
		VALUES ($1,$2,$3,$4,$5)`, previousAlertID, direction+":"+tier,
		nullIfEmpty(reason), actor, utcNow()); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"alert_id": previousAlertID, "alert_tier": tier, "previous_tier": previousTier, "reason": reason,
	}
	for _, institution := range uniqueSorted([]string{receiving, reporting}) {
		if err := insertNotification(ctx, tx, institution, "alert."+direction, previousAlertID, payload); err != nil {
			return nil, err
		}
	}
	return []map[string]any{{
		"alert_id": previousAlertID, "state": state, "alert_tier": tier, "previous_tier": previousTier,
		"receiving_institution": receiving, "reporting_institution": reporting,
	}}, nil
}

func insertSignalAlert(
	ctx context.Context,
	tx pgx.Tx,
	report ReportIn,
	reportID string,
	codes []string,
	validation Validation,
) (map[string]any, error) {
	if validation.AlertID == nil {
		return nil, errors.New("alert ID is required")
	}
	riskJSON, _ := json.Marshal(codes)
	alert := map[string]any{
		"alert_id": *validation.AlertID, "validation_id": validation.ValidationID,
		"report_id": reportID, "issued_at": utcNow(),
		"receiving_institution":    report.DestinationInstitution,
		"reporting_institution":    report.ReportingInstitution,
		"destination_account_hash": report.DestinationAccountHash,
		"destination_msisdn_hash":  report.DestinationMSISDNHash,
		"amount":                   report.Amount, "currency": report.Currency,
		"risk_codes": string(riskJSON), "validation_score": validation.ValidationScore,
		"validated_by": "Kifaru policy engine", "explanation": validation.Explanation,
		"state": "sent", "alert_type": alertTypeFor(report),
		"alert_tier": alertTierFor(validation.Status),
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alerts (
		alert_id,validation_id,report_id,issued_at,receiving_institution,
		reporting_institution,destination_account_hash,destination_msisdn_hash,
		amount,currency,risk_codes,validation_score,validated_by,explanation,state,alert_type,alert_tier
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		alert["alert_id"], alert["validation_id"], alert["report_id"], alert["issued_at"],
		alert["receiving_institution"], alert["reporting_institution"],
		alert["destination_account_hash"], alert["destination_msisdn_hash"], alert["amount"],
		alert["currency"], alert["risk_codes"], alert["validation_score"], alert["validated_by"],
		alert["explanation"], alert["state"], alert["alert_type"], alert["alert_tier"]); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,actor,at)
		VALUES ($1,'sent','policy-engine',$2)`, alert["alert_id"], alert["issued_at"]); err != nil {
		return nil, err
	}
	return alert, nil
}

func retractAlert(
	ctx context.Context,
	tx pgx.Tx,
	alertID, receivingInstitution, reportingInstitution, cause, reason, actor string,
) (map[string]any, error) {
	if _, err := tx.Exec(ctx, `UPDATE alerts
		SET state='retracted',outcome='',outcome_note=$1,updated_at=NOW()
		WHERE alert_id=$2`, reason, alertID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,comment,actor,at)
		VALUES ($1,'retracted',$2,$3,$4)`, alertID, nullIfEmpty(reason), actor, utcNow()); err != nil {
		return nil, err
	}
	payload := map[string]any{"alert_id": alertID, "cause": cause, "reason": reason}
	for _, institution := range uniqueSorted([]string{receivingInstitution, reportingInstitution}) {
		if err := insertNotification(ctx, tx, institution, "alert.retracted", alertID, payload); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"alert_id": alertID, "state": "retracted",
		"receiving_institution": receivingInstitution,
		"reporting_institution": reportingInstitution,
	}, nil
}
