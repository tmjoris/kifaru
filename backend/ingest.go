package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// refData is the configuration and the institution list. They change only when
// an administrator edits them, but every report used to read them again: three
// round trips each, on every report. Now they are read once and reused until an
// administrator changes something (the response cache's generation moves) or
// refDataMaxAge passes.
type refData struct {
	cfg        map[string]any
	thresholds map[string]float64
	active     map[string]bool
	kb         map[string]map[string]bool
	gen        uint64
	at         time.Time
}

const refDataMaxAge = 30 * time.Second

func (a *App) reference(ctx context.Context) (*refData, error) {
	gen := a.cache.gen.Load()
	a.refMu.Lock()
	defer a.refMu.Unlock()
	if a.ref != nil && a.ref.gen == gen && time.Since(a.ref.at) < refDataMaxAge {
		return a.ref, nil
	}
	// One round trip for both reads.
	batch := &pgx.Batch{}
	batch.Queue("SELECT key,value FROM config")
	batch.Queue("SELECT code,threshold,active FROM institutions")
	batch.Queue("SELECT artefact_hash,list_name FROM knowledge_base")
	results := a.db.SendBatch(ctx, batch)
	defer results.Close()
	ref := &refData{cfg: map[string]any{}, thresholds: map[string]float64{}, active: map[string]bool{}, kb: map[string]map[string]bool{"known_good": {}, "known_bad": {}}, gen: gen, at: time.Now()}
	configRows, err := results.Query()
	if err != nil {
		return nil, err
	}
	for configRows.Next() {
		var key, raw string
		if err := configRows.Scan(&key, &raw); err != nil {
			configRows.Close()
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			configRows.Close()
			return nil, err
		}
		ref.cfg[key] = value
	}
	configRows.Close()
	if err := configRows.Err(); err != nil {
		return nil, err
	}
	institutionRows, err := results.Query()
	if err != nil {
		return nil, err
	}
	defer institutionRows.Close()
	for institutionRows.Next() {
		var code string
		var threshold float64
		var active int
		if err := institutionRows.Scan(&code, &threshold, &active); err != nil {
			return nil, err
		}
		ref.thresholds[code], ref.active[code] = threshold, active == 1
	}
	if err := institutionRows.Err(); err != nil {
		return nil, err
	}
	institutionRows.Close()
	kbRows, err := results.Query()
	if err != nil {
		return nil, err
	}
	defer kbRows.Close()
	for kbRows.Next() {
		var hash, list string
		if err := kbRows.Scan(&hash, &list); err != nil {
			return nil, err
		}
		if ref.kb[list] == nil {
			ref.kb[list] = map[string]bool{}
		}
		ref.kb[list][hash] = true
	}
	if err := kbRows.Err(); err != nil {
		return nil, err
	}
	a.ref = ref
	return ref, nil
}

// processed is what handling one report produced. Publishing waits until the
// transaction that holds it has committed.
type processed struct {
	result   map[string]any
	reportID string
	alerts   []map[string]any
	replay   bool
}

// publishProcessed pushes a committed report and its alerts to open dashboards.
func (a *App) publishProcessed(done processed) {
	if done.replay {
		return
	}
	a.hub.markDirty(done.reportID)
	for _, alert := range done.alerts {
		a.publishAlert(fmt.Sprint(alert["receiving_institution"]), alert)
	}
}

// preflight rejects a report that can be refused without touching the database.
func preflight(ctx context.Context, report ReportIn) *apiError {
	if attackScenarioRun(report.Evidence) != "" && !fromAttackScenario(ctx) {
		return &apiError{http.StatusUnprocessableEntity,
			"evidence field attack_scenario is reserved for the attack scenario"}
	}
	if truthy(report.Evidence["seeded_history"]) && !fromSeedHistory(ctx) {
		return &apiError{http.StatusUnprocessableEntity,
			"evidence field seeded_history is reserved for the history seed"}
	}
	if err := validateReport(report); err != nil {
		return &apiError{http.StatusUnprocessableEntity, err.Error()}
	}
	return nil
}

// insertReportSQL writes a report, its artefacts, its first validation and the
// audit entry in one statement. Everything after the report insert is driven by
// the rows the insert actually returned, so a report that already exists (a bank
// retrying) changes nothing and returns no row.
const insertReportSQL = `WITH ins AS (
	INSERT INTO reports (
		report_id,submitted_at,submission_channel,reporting_institution,reporting_system,
		transaction_ref,transaction_timestamp,subject_account_hash,subject_customer_hash,
		destination_account_hash,destination_msisdn_hash,destination_institution,amount,currency,
		channel,bank_risk_score,bank_threshold,risk_codes,evidence,narrative
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	ON CONFLICT (reporting_institution,transaction_ref) DO NOTHING
	RETURNING report_id
), art AS (
	INSERT INTO artefacts(report_id,institution_code,artefact_type,artefact_hash,observed_at,match_scope)
	SELECT $1,$4,t.kind,t.hash,$2,t.scope FROM ins, unnest($21::text[],$22::text[],$23::text[]) AS t(kind,hash,scope)
	ON CONFLICT DO NOTHING
), val AS (
	INSERT INTO validations (
		validation_id,report_id,validated_at,agent_version,validation_score,status,
		reason_codes,corroborating_institutions,corroboration_count,explanation,
		latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
	) SELECT $24,$1,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,NULL,1 FROM ins
), aud AS (
	INSERT INTO audit_log(at,actor,action,target,detail,old_value,new_value,reason)
	SELECT $36,'policy-engine','validation.created',$24,$37,'',$37,'report validation' FROM ins
)
SELECT report_id FROM ins`

// artefactRows lists the hashes a report contributes for corroboration.
func artefactRows(report ReportIn) (kinds, hashes, scopes []string) {
	add := func(kind, hash string) {
		if hash == "" {
			return
		}
		kinds, hashes, scopes = append(kinds, kind), append(hashes, hash), append(scopes, artefactScope(kind, report.DestinationInstitution))
	}
	add("subject_account", report.SubjectAccountHash)
	add("subject_customer", report.SubjectCustomerHash)
	add("destination_account", report.DestinationAccountHash)
	add("destination_msisdn", report.DestinationMSISDNHash)
	if device := fmt.Sprint(report.Evidence["device_profile"]); device != "" && device != "<nil>" {
		add("device_profile", device)
	}
	return kinds, hashes, scopes
}

// processIn validates, scores and stores one report inside a transaction the
// caller owns, and commits nothing. A returned 4xx means the report was refused
// before anything was written, so the transaction is still usable; a 5xx means
// the transaction must be abandoned.
//
// In the common case (no alert, no corroboration) this is two round trips: the
// corroboration lookup, then one batch holding the insert and the follow-up
// re-validation lookup.
func (a *App) processIn(ctx context.Context, tx pgx.Tx, report ReportIn, channel string) (processed, *apiError) {
	start := time.Now()
	ref, err := a.reference(ctx)
	if err != nil {
		return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
	}
	cfg := ref.cfg
	if !containsString(toStringSlice(cfg["enabled_sources"]), channel) {
		return processed{}, &apiError{http.StatusForbidden, fmt.Sprintf("submission channel %q is disabled by admin", channel)}
	}
	threshold, known := ref.thresholds[report.ReportingInstitution]
	if !known {
		return processed{}, &apiError{http.StatusBadRequest, fmt.Sprintf("unknown institution %q", report.ReportingInstitution)}
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
	if !ref.active[report.DestinationInstitution] {
		return processed{}, &apiError{http.StatusUnprocessableEntity,
			fmt.Sprintf("unknown destination institution %q", report.DestinationInstitution)}
	}
	report.BankThreshold = threshold
	if report.Evidence == nil {
		report.Evidence = map[string]any{}
	}

	codes, err := a.normalize(report)
	if err != nil {
		return processed{}, &apiError{http.StatusUnprocessableEntity, err.Error()}
	}
	if err := a.validateCodeEvidence(report, codes); err != nil {
		return processed{}, &apiError{http.StatusUnprocessableEntity, err.Error()}
	}
	reportID := "rpt-" + randomHex(6)
	submittedAt := utcNow()
	validation, err := a.score(ctx, tx, report, reportID, codes, cfg, start)
	if err != nil {
		return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
	}
	validation.Explanation = a.explain(report, validation, codes)
	newAlert := assignAlertID(&validation, "")

	riskJSON, _ := json.Marshal(codes)
	evidenceJSON, _ := json.Marshal(report.Evidence)
	reasonsJSON, _ := json.Marshal(validation.ReasonCodes)
	corroJSON, _ := json.Marshal(validation.CorroboratingInstitutions)
	configVersion := int(numberOr(cfg["configuration_version"], 1))
	validationAudit, _ := json.Marshal(map[string]any{
		"status": validation.Status, "score": validation.ValidationScore,
		"reason_codes": validation.ReasonCodes, "configuration_version": configVersion,
	})
	kinds, hashes, scopes := artefactRows(report)

	batch := &pgx.Batch{}
	batch.Queue(insertReportSQL,
		reportID, submittedAt, channel, report.ReportingInstitution, report.ReportingSystem,
		report.TransactionRef, report.TransactionTimestamp, report.SubjectAccountHash,
		report.SubjectCustomerHash, report.DestinationAccountHash, report.DestinationMSISDNHash,
		report.DestinationInstitution, report.Amount, report.Currency, report.Channel,
		report.BankRiskScore, report.BankThreshold, string(riskJSON), string(evidenceJSON), report.Narrative,
		kinds, hashes, scopes,
		validation.ValidationID, validation.ValidatedAt, validation.AgentVersion,
		validation.ValidationScore, validation.Status, string(reasonsJSON), string(corroJSON),
		validation.CorroborationCount, validation.Explanation, validation.LatencyMS,
		validation.AlertID, configVersion, utcNow(), string(validationAudit))
	batch.Queue(revalidateCandidatesSQL, revalidateArgs(reportID)...)
	results := tx.SendBatch(ctx, batch)
	var inserted string
	insertErr := results.QueryRow().Scan(&inserted)
	var candidates []string
	if insertErr == nil {
		candidateRows, err := results.Query()
		if err != nil {
			_ = results.Close()
			return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
		}
		for candidateRows.Next() {
			var id string
			if err := candidateRows.Scan(&id); err != nil {
				candidateRows.Close()
				_ = results.Close()
				return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
			}
			candidates = append(candidates, id)
		}
		candidateRows.Close()
		if err := candidateRows.Err(); err != nil {
			_ = results.Close()
			return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
		}
	}
	closeErr := results.Close()
	if errors.Is(insertErr, pgx.ErrNoRows) {
		// A report with this reference already exists: answer with it, change nothing.
		var existingReportID string
		if err := tx.QueryRow(ctx, `SELECT report_id FROM reports
			WHERE reporting_institution=$1 AND transaction_ref=$2`,
			report.ReportingInstitution, report.TransactionRef).Scan(&existingReportID); err != nil {
			return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
		}
		result, resultErr := existingReportResult(ctx, tx, existingReportID)
		if resultErr != nil {
			return processed{}, &apiError{http.StatusInternalServerError, resultErr.Error()}
		}
		result["idempotent_replay"] = true
		return processed{result: result, reportID: existingReportID, replay: true}, nil
	}
	if insertErr != nil {
		return processed{}, &apiError{http.StatusInternalServerError, insertErr.Error()}
	}
	if closeErr != nil {
		return processed{}, &apiError{http.StatusInternalServerError, closeErr.Error()}
	}

	var alert map[string]any
	publishedAlerts, err := reconcileAlert(ctx, tx, report, reportID, codes, validation, "", newAlert,
		"policy-engine", "report validation")
	if err != nil {
		return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
	}
	if len(publishedAlerts) > 0 {
		alert = publishedAlerts[0]
	}
	revalidatedAlerts, err := a.revalidateReports(ctx, tx, candidates, cfg)
	if err != nil {
		return processed{}, &apiError{http.StatusInternalServerError, err.Error()}
	}
	publishedAlerts = append(publishedAlerts, revalidatedAlerts...)

	described := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		item := a.standard.Codes[code]
		described = append(described, map[string]any{"code": code, "family": item.Family, "name": item.Name})
	}
	return processed{
		result: map[string]any{
			"report_id": reportID, "risk_codes": described, "validation": validation, "alert": alert,
		},
		reportID: reportID, alerts: publishedAlerts,
	}, nil
}

// batchChunk is how many reports of a batch share one transaction. It bounds how
// long locks are held and how much a failed transaction has to redo.
const batchChunk = 100

// processBatch handles many reports with few transactions: each chunk of up to
// batchChunk reports shares one BEGIN and one COMMIT, and the reports run in
// order, so reports in the same batch still corroborate each other. A report
// refused with a 4xx is recorded and skipped. If the database fails partway, the
// chunk is abandoned and its reports are retried one at a time, so one bad
// report cannot take its neighbours down with it.
func (a *App) processBatch(ctx context.Context, reports []ReportIn, channel string) (results []map[string]any, errs []map[string]any) {
	results = make([]map[string]any, 0, len(reports))
	errs = []map[string]any{}
	for start := 0; start < len(reports); start += batchChunk {
		end := min(start+batchChunk, len(reports))
		chunkResults, chunkErrs := a.processChunk(ctx, reports[start:end], start, channel)
		results = append(results, chunkResults...)
		errs = append(errs, chunkErrs...)
	}
	return results, errs
}

func (a *App) processChunk(ctx context.Context, reports []ReportIn, offset int, channel string) ([]map[string]any, []map[string]any) {
	results := make([]map[string]any, 0, len(reports))
	errs := []map[string]any{}
	refuse := func(index int, message string) {
		errs = append(errs, map[string]any{"index": offset + index, "error": message})
	}
	var done []processed
	tx, err := a.db.Begin(ctx)
	failed := err != nil
	if !failed {
		for index, report := range reports {
			if apiErr := preflight(ctx, report); apiErr != nil {
				refuse(index, apiErr.message)
				continue
			}
			item, apiErr := a.processIn(ctx, tx, report, channel)
			switch {
			case apiErr == nil:
				done = append(done, item)
				results = append(results, item.result)
			case apiErr.status < 500:
				refuse(index, apiErr.message)
			default:
				failed = true
			}
			if failed {
				break
			}
		}
		if !failed {
			failed = tx.Commit(ctx) != nil
		}
		if failed {
			_ = tx.Rollback(ctx)
		}
	}
	if failed {
		// Start the chunk over, one report per transaction.
		results, errs, done = results[:0], errs[:0], nil
		for index, report := range reports {
			result, apiErr := a.process(ctx, report, channel)
			if apiErr != nil {
				refuse(index, apiErr.message)
				continue
			}
			results = append(results, result)
		}
		return results, errs
	}
	for _, item := range done {
		a.publishProcessed(item)
	}
	return results, errs
}
