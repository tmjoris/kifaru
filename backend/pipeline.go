package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) submitReport(w http.ResponseWriter, r *http.Request, channel string) {
	var report ReportIn
	if err := decodeJSON(r, &report); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !authorizeReport(w, r, &report) {
		return
	}
	result, apiErr := a.process(r.Context(), report, channel)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.message)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) submitBatch(w http.ResponseWriter, r *http.Request) {
	var reports []ReportIn
	if err := decodeJSON(r, &reports); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	for index := range reports {
		if !authorizeReport(w, r, &reports[index]) {
			return
		}
	}
	results := make([]map[string]any, 0, len(reports))
	errs := []map[string]any{}
	for index, report := range reports {
		result, apiErr := a.process(r.Context(), report, "batch")
		if apiErr != nil {
			errs = append(errs, map[string]any{"index": index, "error": apiErr.message})
			continue
		}
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": len(results), "rejected": len(errs), "errors": errs, "results": results,
	})
}

type apiError struct {
	status  int
	message string
}

func (a *App) process(ctx context.Context, report ReportIn, channel string) (map[string]any, *apiError) {
	start := time.Now()
	if err := validateReport(report); err != nil {
		return nil, &apiError{http.StatusUnprocessableEntity, err.Error()}
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cfg, err := getConfigFrom(ctx, tx)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	if !containsString(toStringSlice(cfg["enabled_sources"]), channel) {
		return nil, &apiError{http.StatusForbidden, fmt.Sprintf("submission channel %q is disabled by admin", channel)}
	}
	var institutionThreshold float64
	err = tx.QueryRow(ctx, "SELECT threshold FROM institutions WHERE code=$1", report.ReportingInstitution).Scan(&institutionThreshold)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &apiError{http.StatusBadRequest, fmt.Sprintf("unknown institution %q", report.ReportingInstitution)}
	}
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	if report.ReportingSystem == "" {
		report.ReportingSystem = "AG Screener"
	}
	if report.Currency == "" {
		report.Currency = "KES"
	}
	if report.DestinationInstitution == "" {
		report.DestinationInstitution = "external"
	}
	if report.BankThreshold == 0 {
		report.BankThreshold = institutionThreshold
	}
	if report.Evidence == nil {
		report.Evidence = map[string]any{}
	}
	var existingReportID string
	err = tx.QueryRow(ctx, `SELECT report_id FROM reports
		WHERE reporting_institution=$1 AND transaction_ref=$2`,
		report.ReportingInstitution, report.TransactionRef).Scan(&existingReportID)
	if err == nil {
		result, resultErr := existingReportResult(ctx, tx, existingReportID)
		if resultErr != nil {
			return nil, &apiError{http.StatusInternalServerError, resultErr.Error()}
		}
		result["idempotent_replay"] = true
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}

	codes, err := a.normalize(report)
	if err != nil {
		return nil, &apiError{http.StatusUnprocessableEntity, err.Error()}
	}
	reportID := "rpt-" + randomHex(6)
	submittedAt := utcNow()
	validation, err := a.score(ctx, tx, report, reportID, codes, cfg, start)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	validation.Explanation = a.explain(report, validation, codes)
	alertType := alertTypeFor(report)
	if validation.Status == "VALIDATED_FRAUD" {
		alertID := "alt-" + randomHex(5)
		validation.AlertID = &alertID
	}

	riskJSON, _ := json.Marshal(codes)
	evidenceJSON, _ := json.Marshal(report.Evidence)
	reportTag, err := tx.Exec(ctx, `INSERT INTO reports (
		report_id,submitted_at,submission_channel,reporting_institution,reporting_system,
		transaction_ref,transaction_timestamp,subject_account_hash,subject_customer_hash,
		destination_account_hash,destination_msisdn_hash,destination_institution,amount,currency,
		channel,bank_risk_score,bank_threshold,risk_codes,evidence,narrative
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	ON CONFLICT (reporting_institution,transaction_ref) DO NOTHING`,
		reportID, submittedAt, channel, report.ReportingInstitution, report.ReportingSystem,
		report.TransactionRef, report.TransactionTimestamp, report.SubjectAccountHash,
		report.SubjectCustomerHash, report.DestinationAccountHash, report.DestinationMSISDNHash,
		report.DestinationInstitution, report.Amount, report.Currency, report.Channel,
		report.BankRiskScore, report.BankThreshold, string(riskJSON), string(evidenceJSON), report.Narrative)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	if reportTag.RowsAffected() == 0 {
		if err := tx.QueryRow(ctx, `SELECT report_id FROM reports
			WHERE reporting_institution=$1 AND transaction_ref=$2`,
			report.ReportingInstitution, report.TransactionRef).Scan(&existingReportID); err != nil {
			return nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
		result, resultErr := existingReportResult(ctx, tx, existingReportID)
		if resultErr != nil {
			return nil, &apiError{http.StatusInternalServerError, resultErr.Error()}
		}
		result["idempotent_replay"] = true
		return result, nil
	}
	if err := insertArtefacts(ctx, tx, reportID, submittedAt, report); err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}

	reasonsJSON, _ := json.Marshal(validation.ReasonCodes)
	corroJSON, _ := json.Marshal(validation.CorroboratingInstitutions)
	configVersion := int(numberOr(cfg["configuration_version"], 1))
	_, err = tx.Exec(ctx, `INSERT INTO validations (
		validation_id,report_id,validated_at,agent_version,validation_score,status,
		reason_codes,corroborating_institutions,corroboration_count,explanation,
		latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,1)`,
		validation.ValidationID, reportID, validation.ValidatedAt, validation.AgentVersion,
		validation.ValidationScore, validation.Status, string(reasonsJSON), string(corroJSON),
		validation.CorroborationCount, validation.Explanation, validation.LatencyMS,
		validation.AlertID, configVersion, nil)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	validationAudit, _ := json.Marshal(map[string]any{
		"status": validation.Status, "score": validation.ValidationScore,
		"reason_codes": validation.ReasonCodes, "configuration_version": configVersion,
	})
	if err := auditRecord(ctx, tx, "agent", "validation.created", validation.ValidationID,
		"", string(validationAudit), "report validation"); err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}

	var alert map[string]any
	publishedAlerts := []map[string]any{}
	if validation.AlertID != nil {
		alert = map[string]any{
			"alert_id": *validation.AlertID, "validation_id": validation.ValidationID, "report_id": reportID,
			"issued_at": utcNow(), "receiving_institution": report.DestinationInstitution,
			"reporting_institution":    report.ReportingInstitution,
			"destination_account_hash": report.DestinationAccountHash,
			"destination_msisdn_hash":  report.DestinationMSISDNHash,
			"amount":                   report.Amount, "currency": report.Currency, "risk_codes": string(riskJSON),
			"validation_score": validation.ValidationScore, "validated_by": "KIFARU validation agent",
			"explanation": validation.Explanation, "state": "sent", "alert_type": alertType,
		}
		_, err = tx.Exec(ctx, `INSERT INTO alerts (
			alert_id,validation_id,report_id,issued_at,receiving_institution,
			reporting_institution,destination_account_hash,destination_msisdn_hash,
			amount,currency,risk_codes,validation_score,validated_by,explanation,state,alert_type
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			alert["alert_id"], alert["validation_id"], alert["report_id"], alert["issued_at"],
			alert["receiving_institution"], alert["reporting_institution"],
			alert["destination_account_hash"], alert["destination_msisdn_hash"], alert["amount"],
			alert["currency"], alert["risk_codes"], alert["validation_score"], alert["validated_by"],
			alert["explanation"], alert["state"], alert["alert_type"])
		if err != nil {
			return nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
		_, err = tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,actor,at)
			VALUES ($1,'sent','agent',$2)`, alert["alert_id"], alert["issued_at"])
		if err != nil {
			return nil, &apiError{http.StatusInternalServerError, err.Error()}
		}
		if report.DestinationAccountHash != "" {
			if err := kbAddWith(ctx, tx, report.DestinationAccountHash, "known_bad",
				"validated via "+reportID, "agent"); err != nil {
				return nil, &apiError{http.StatusInternalServerError, err.Error()}
			}
		}
		publishedAlerts = append(publishedAlerts, alert)
	}
	revalidatedAlerts, err := a.revalidateCorroborated(ctx, tx, reportID, cfg)
	if err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	publishedAlerts = append(publishedAlerts, revalidatedAlerts...)
	if err := tx.Commit(ctx); err != nil {
		return nil, &apiError{http.StatusInternalServerError, err.Error()}
	}
	for _, issuedAlert := range publishedAlerts {
		a.publishAlert(fmt.Sprint(issuedAlert["receiving_institution"]), issuedAlert)
	}

	described := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		item := a.standard.Codes[code]
		described = append(described, map[string]any{"code": code, "family": item.Family, "name": item.Name})
	}
	return map[string]any{
		"report_id": reportID, "risk_codes": described, "validation": validation, "alert": alert,
	}, nil
}

func existingReportResult(ctx context.Context, store dbRunner, reportID string) (map[string]any, error) {
	validations, err := rowsFrom(ctx, store, `SELECT * FROM validations
		WHERE report_id=$1 AND is_current=1 ORDER BY validated_at DESC LIMIT 1`, reportID)
	if err != nil {
		return nil, err
	}
	alerts, err := rowsFrom(ctx, store, "SELECT * FROM alerts WHERE report_id=$1 LIMIT 1", reportID)
	if err != nil {
		return nil, err
	}
	var validation any
	if len(validations) > 0 {
		validation = validations[0]
	}
	var alert any
	if len(alerts) > 0 {
		alert = alerts[0]
	}
	return map[string]any{"report_id": reportID, "validation": validation, "alert": alert}, nil
}

func insertArtefacts(ctx context.Context, store dbRunner, reportID, observedAt string, report ReportIn) error {
	artefacts := []struct {
		kind string
		hash string
	}{
		{"subject_account", report.SubjectAccountHash},
		{"subject_customer", report.SubjectCustomerHash},
		{"destination_account", report.DestinationAccountHash},
		{"destination_msisdn", report.DestinationMSISDNHash},
	}
	if device := fmt.Sprint(report.Evidence["device_profile"]); device != "" && device != "<nil>" {
		artefacts = append(artefacts, struct {
			kind string
			hash string
		}{"device_profile", device})
	}
	for _, artefact := range artefacts {
		if artefact.hash == "" {
			continue
		}
		if _, err := store.Exec(ctx, `INSERT INTO artefacts(
			report_id,institution_code,artefact_type,artefact_hash,observed_at
		) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			reportID, report.ReportingInstitution, artefact.kind, artefact.hash, observedAt); err != nil {
			return err
		}
	}
	return nil
}

func alertTypeFor(report ReportIn) string {
	if report.Amount <= 0 {
		return "advisory"
	}
	return "hold"
}

func (a *App) revalidateCorroborated(
	ctx context.Context,
	tx pgx.Tx,
	newReportID string,
	cfg map[string]any,
) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT v.report_id
		FROM validations v
		JOIN artefacts prior ON prior.report_id=v.report_id
		JOIN artefacts current ON current.report_id=$1
			AND current.artefact_type=prior.artefact_type
			AND current.artefact_hash=prior.artefact_hash
		WHERE v.is_current=1
		  AND v.status IN ('INSUFFICIENT_EVIDENCE','NOT_FRAUD')
		  AND prior.report_id<>$1
		  AND prior.institution_code<>current.institution_code
		  AND prior.observed_at >= $2`,
		newReportID, time.Now().UTC().Add(-30*24*time.Hour).Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return nil, err
	}
	candidateIDs := []string{}
	for rows.Next() {
		var reportID string
		if err := rows.Scan(&reportID); err != nil {
			rows.Close()
			return nil, err
		}
		candidateIDs = append(candidateIDs, reportID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	alerts := []map[string]any{}
	for _, reportID := range candidateIDs {
		reportRows, err := rowsFrom(ctx, tx, "SELECT * FROM reports WHERE report_id=$1", reportID)
		if err != nil || len(reportRows) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		report, codes, err := reportFromRow(reportRows[0])
		if err != nil {
			return nil, err
		}
		var previousID, previousStatus string
		var previousScore float64
		if err := tx.QueryRow(ctx, `SELECT validation_id,status,validation_score
			FROM validations WHERE report_id=$1 AND is_current=1`,
			reportID).Scan(&previousID, &previousStatus, &previousScore); err != nil {
			return nil, err
		}
		validation, err := a.score(ctx, tx, report, reportID, codes, cfg, time.Now())
		if err != nil {
			return nil, err
		}
		validation.Explanation = a.explain(report, validation, codes)
		if validation.Status == "VALIDATED_FRAUD" {
			alertID := "alt-" + randomHex(5)
			validation.AlertID = &alertID
		}
		if _, err := tx.Exec(ctx, "UPDATE validations SET is_current=0 WHERE validation_id=$1", previousID); err != nil {
			return nil, err
		}
		reasons, _ := json.Marshal(validation.ReasonCodes)
		corro, _ := json.Marshal(validation.CorroboratingInstitutions)
		if _, err := tx.Exec(ctx, `INSERT INTO validations (
			validation_id,report_id,validated_at,agent_version,validation_score,status,
			reason_codes,corroborating_institutions,corroboration_count,explanation,
			latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,1)`,
			validation.ValidationID, reportID, validation.ValidatedAt, validation.AgentVersion,
			validation.ValidationScore, validation.Status, string(reasons), string(corro),
			validation.CorroborationCount, validation.Explanation, validation.LatencyMS,
			validation.AlertID, int(numberOr(cfg["configuration_version"], 1)), previousID); err != nil {
			return nil, err
		}
		oldValue, _ := json.Marshal(map[string]any{"status": previousStatus, "score": previousScore})
		newValue, _ := json.Marshal(map[string]any{
			"status": validation.Status, "score": validation.ValidationScore,
			"corroborating_institutions": validation.CorroboratingInstitutions,
		})
		if err := auditRecord(ctx, tx, "agent", "validation.revalidated",
			validation.ValidationID, string(oldValue), string(newValue),
			"new report supplied matching artefact"); err != nil {
			return nil, err
		}
		if validation.AlertID == nil {
			continue
		}
		riskJSON, _ := json.Marshal(codes)
		alert := map[string]any{
			"alert_id": *validation.AlertID, "validation_id": validation.ValidationID,
			"report_id": reportID, "issued_at": utcNow(),
			"receiving_institution":    report.DestinationInstitution,
			"reporting_institution":    report.ReportingInstitution,
			"destination_account_hash": report.DestinationAccountHash,
			"destination_msisdn_hash":  report.DestinationMSISDNHash,
			"amount":                   report.Amount, "currency": report.Currency, "risk_codes": string(riskJSON),
			"validation_score": validation.ValidationScore, "validated_by": "KIFARU validation agent",
			"explanation": validation.Explanation, "state": "sent", "alert_type": alertTypeFor(report),
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alerts (
			alert_id,validation_id,report_id,issued_at,receiving_institution,
			reporting_institution,destination_account_hash,destination_msisdn_hash,
			amount,currency,risk_codes,validation_score,validated_by,explanation,state,alert_type
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			alert["alert_id"], alert["validation_id"], alert["report_id"], alert["issued_at"],
			alert["receiving_institution"], alert["reporting_institution"],
			alert["destination_account_hash"], alert["destination_msisdn_hash"], alert["amount"],
			alert["currency"], alert["risk_codes"], alert["validation_score"], alert["validated_by"],
			alert["explanation"], alert["state"], alert["alert_type"]); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alert_actions(alert_id,action,actor,at)
			VALUES ($1,'sent','agent',$2)`, alert["alert_id"], alert["issued_at"]); err != nil {
			return nil, err
		}
		if report.DestinationAccountHash != "" {
			if err := kbAddWith(ctx, tx, report.DestinationAccountHash, "known_bad",
				"validated via automatic revalidation "+reportID, "agent"); err != nil {
				return nil, err
			}
		}
		alerts = append(alerts, alert)
	}
	return alerts, nil
}

func validateReport(report ReportIn) error {
	if report.ReportingInstitution == "" || report.TransactionRef == "" || report.TransactionTimestamp == "" {
		return errors.New("reporting_institution, transaction_ref and transaction_timestamp are required")
	}
	for _, value := range []string{
		report.SubjectAccountHash, report.SubjectCustomerHash,
		report.DestinationAccountHash, report.DestinationMSISDNHash,
	} {
		if value != "" && !strings.HasPrefix(value, "sha256:") {
			return errors.New("identifiers must be submitted as 'sha256:...' — KIFARU does not accept cleartext customer data")
		}
	}
	return nil
}

func (a *App) normalize(report ReportIn) ([]string, error) {
	codes := []string{}
	add := func(code string) {
		if _, ok := a.standard.Codes[code]; ok && !containsString(codes, code) {
			codes = append(codes, code)
		}
	}
	for _, code := range report.RiskCodes {
		add(code)
	}
	for _, rule := range report.BankRuleIDs {
		add(a.standard.BankRuleMapping[rule])
	}
	ev := report.Evidence
	if value, ok := numberValue(ev["flow_through_ratio"]); ok && value > 0.90 {
		add("MUL-441")
	}
	if value, ok := numberValue(ev["dwell_minutes"]); ok && value < 10 {
		add("MUL-442")
	}
	age, hasAge := numberValue(ev["account_age_days"])
	senders, hasSenders := numberValue(ev["distinct_senders_7d"])
	if hasAge && hasSenders && age < 14 && senders >= 5 {
		add("MUL-440")
	}
	if value, ok := numberValue(ev["sim_swap_age_days"]); ok && value <= 3 {
		add("IP-402")
	}
	if strings.EqualFold(fmt.Sprint(ev["is_new_device"]), "true") &&
		strings.EqualFold(fmt.Sprint(ev["is_new_beneficiary"]), "true") {
		add("ATO-460")
	}
	if truthy(ev["is_emulator"]) || truthy(ev["is_rooted"]) {
		add("IP-403")
	}
	if truthy(ev["ip_country_changed"]) || truthy(ev["vpn_proxy_tor"]) {
		add("IP-404")
	}
	if len(codes) == 0 {
		return nil, errors.New("report produced no risk codes — nothing to validate")
	}
	return codes, nil
}

func (a *App) score(
	ctx context.Context,
	store dbRunner,
	report ReportIn,
	reportID string,
	codes []string,
	cfg map[string]any,
	start time.Time,
) (Validation, error) {
	score := 0.0
	reasons := []string{}
	for _, code := range codes {
		score += a.standard.Codes[code].SeverityBase * weightCode
		reasons = append(reasons, "CODE:"+code)
	}

	device := fmt.Sprint(report.Evidence["device_profile"])
	if device == "<nil>" {
		device = ""
	}
	since := time.Now().UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
	rows, err := store.Query(ctx, `SELECT DISTINCT a.institution_code,a.artefact_type
		FROM artefacts a
		WHERE a.institution_code != $1
		  AND (
		    (a.artefact_type='destination_account' AND a.artefact_hash=$2) OR
		    (a.artefact_type='destination_msisdn' AND a.artefact_hash=$3) OR
		    (a.artefact_type='device_profile' AND a.artefact_hash=$4)
		  )
		  AND a.observed_at >= $5
		ORDER BY a.institution_code`,
		report.ReportingInstitution, report.DestinationAccountHash, report.DestinationMSISDNHash,
		device, since)
	if err != nil {
		return Validation{}, err
	}
	defer rows.Close()
	corro := map[string]bool{}
	for rows.Next() {
		var institution, artefactType string
		if err := rows.Scan(&institution, &artefactType); err != nil {
			return Validation{}, err
		}
		switch artefactType {
		case "destination_account":
			corro[institution] = true
			reasons = append(reasons, "CORRO:destination")
		case "destination_msisdn":
			corro[institution] = true
			reasons = append(reasons, "CORRO:msisdn")
		case "device_profile":
			corro[institution] = true
			reasons = append(reasons, "CORRO:device_profile")
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Validation{}, err
	}
	score += float64(min(len(corro), corroborationCap)) * weightCorroboration

	// A fraudster who changes phones between institutions still needs the same
	// cash-out destination, so record a destination match from another device.
	if device != "" && len(corro) > 0 {
		var switched bool
		if err := store.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM artefacts m
			JOIN artefacts d ON d.report_id=m.report_id AND d.artefact_type='device_profile'
			WHERE m.institution_code != $1
			  AND (
			    (m.artefact_type='destination_account' AND m.artefact_hash=$2) OR
			    (m.artefact_type='destination_msisdn' AND m.artefact_hash=$3)
			  )
			  AND m.observed_at >= $4
			  AND d.artefact_hash != $5)`,
			report.ReportingInstitution, report.DestinationAccountHash, report.DestinationMSISDNHash,
			since, device).Scan(&switched); err != nil {
			return Validation{}, err
		}
		if switched {
			reasons = append(reasons, "LINK:device_switch")
		}
	}

	kb, err := kbListsFrom(ctx, store)
	if err != nil {
		return Validation{}, err
	}
	if kb["known_bad"][report.DestinationAccountHash] {
		score += weightKnownBad
		reasons = append(reasons, "KB:known_bad")
	}
	if kb["known_bad"][report.DestinationMSISDNHash] {
		score += weightKnownBad
		reasons = append(reasons, "KB:known_bad_msisdn")
	}
	if kb["known_good"][report.DestinationAccountHash] || kb["known_good"][report.DestinationMSISDNHash] {
		score += weightKnownGood
		reasons = append(reasons, "KB:suppressed_legitimate")
	}
	if report.BankRiskScore >= report.BankThreshold {
		score += weightAboveThreshold
		reasons = append(reasons, "BANK:above_threshold")
	}
	score = math.Round(math.Max(0, math.Min(score, 0.99))*100) / 100

	validated := numberOr(cfg["validated_threshold"], defaultValidated)
	insufficient := numberOr(cfg["insufficient_threshold"], defaultInsufficient)
	status := "NOT_FRAUD"
	if score >= validated {
		status = "VALIDATED_FRAUD"
	} else if score >= insufficient {
		status = "INSUFFICIENT_EVIDENCE"
	}
	corroInstitutions := make([]string, 0, len(corro))
	for institution := range corro {
		corroInstitutions = append(corroInstitutions, institution)
	}
	sort.Strings(corroInstitutions)
	reasons = uniqueSorted(reasons)
	latency := max(1, int(time.Since(start).Milliseconds()))
	return Validation{
		ValidationID: "val-" + randomHex(5), ReportID: reportID, ValidatedAt: utcNow(),
		AgentVersion: agentVersion, ValidationScore: score, Status: status,
		ReasonCodes: reasons, CorroboratingInstitutions: corroInstitutions,
		CorroborationCount: len(corroInstitutions), LatencyMS: latency,
	}, nil
}

func (a *App) explain(report ReportIn, validation Validation, codes []string) string {
	names := make([]string, 0, min(3, len(codes)))
	for _, code := range codes[:min(3, len(codes))] {
		name := a.standard.Codes[code].Name
		if name != "" {
			name = strings.ToLower(name[:1]) + name[1:]
		}
		names = append(names, name)
	}
	subject := fmt.Sprintf("A transfer of %s %.0f", report.Currency, report.Amount)
	if report.Amount <= 0 {
		subject = "An account event with no transfer"
	}
	signals := strings.Join(names, ", ")
	switchNote := ""
	if containsString(validation.ReasonCodes, "LINK:device_switch") {
		switchNote = " Another institution reported the same destination from a different device, which fits a fraudster switching devices."
	}
	others := fmt.Sprintf("%d other institution", validation.CorroborationCount)
	if validation.CorroborationCount != 1 {
		others += "s"
	}
	switch validation.Status {
	case "VALIDATED_FRAUD":
		corroboration := "the reporting institution's own evidence"
		if validation.CorroborationCount > 0 {
			corroboration = others + " independently reporting the same artefact"
		}
		action := "Hold the transaction for step-up verification before release."
		if report.Amount <= 0 {
			action = "No money has moved yet. Verify the customer and watch the destination before the next transfer."
		}
		return fmt.Sprintf("%s was flagged for %s. This was corroborated by %s.%s %s",
			subject, signals, corroboration, switchNote, action)
	case "INSUFFICIENT_EVIDENCE":
		if validation.CorroborationCount > 0 {
			return fmt.Sprintf("%s showed %s. %s reported a matching artefact, but the score stayed below the alert threshold.%s Monitoring only.",
				subject, signals, others, switchNote)
		}
		return fmt.Sprintf("%s showed %s, but no other institution has reported a matching artefact. Monitoring only until another institution corroborates it.",
			subject, signals)
	default:
		return fmt.Sprintf("%s matched %s, but the pattern did not meet the sector standard. Marked not fraud; no alert issued.",
			subject, signals)
	}
}
