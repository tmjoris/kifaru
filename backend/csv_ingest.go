package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (a *App) submitCSV(w http.ResponseWriter, r *http.Request) {
	reader := csv.NewReader(r.Body)
	reader.ReuseRecord = false
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		writeError(w, 400, "CSV must include headers and at least one data row")
		return
	}
	headers := records[0]
	validations := []map[string]any{}
	errs := []map[string]any{}
	for index, record := range records[1:] {
		row := map[string]string{}
		for i, header := range headers {
			if i < len(record) {
				row[header] = record[i]
			}
		}
		report := reportFromCSV(row)
		if !authorizeReport(w, r, &report) {
			return
		}
		result, apiErr := a.process(r.Context(), report, "batch")
		if apiErr != nil {
			errs = append(errs, map[string]any{"index": index, "error": apiErr.message})
			continue
		}
		validations = append(validations, dashboardValidation(result, report))
	}
	counts := map[string]int{"validated_fraud": 0, "not_fraud": 0, "needs_review": 0}
	riskCounts := map[string]int{}
	for _, validation := range validations {
		counts[validation["status"].(string)]++
		for _, value := range validation["risk_codes"].([]map[string]any) {
			riskCounts[value["code"].(string)]++
		}
	}
	topCodes := []map[string]any{}
	for code, count := range riskCounts {
		topCodes = append(topCodes, map[string]any{"code": code, "count": count})
	}
	sort.Slice(topCodes, func(i, j int) bool { return topCodes[i]["count"].(int) > topCodes[j]["count"].(int) })
	writeJSON(w, 200, map[string]any{
		"summary": map[string]any{
			"total_rows": len(validations), "validated_fraud": counts["validated_fraud"],
			"not_fraud": counts["not_fraud"], "needs_review": counts["needs_review"],
			"top_risk_codes": topCodes,
		},
		"validations": validations, "errors": errs,
	})
}

func reportFromCSV(row map[string]string) ReportIn {
	reporting := institutionCode(first(row["reporting_institution"], row["reporting_bank"]))
	receiving := institutionCode(first(row["destination_institution"], row["receiving_bank"]))
	transactionRef := first(row["transaction_ref"], row["transaction_id"], "uploaded-transaction")
	customer := first(row["customer_ref"], row["customer"], row["subject_customer_hash"], "unknown")
	codes := splitCodes(row["risk_codes"])
	if len(codes) == 0 {
		codes = deriveUploadCodes(row)
	}
	evidence := map[string]any{}
	for source, destination := range map[string]string{
		"evidence_device_profile": "device_profile", "evidence_account_age_days": "account_age_days",
		"evidence_distinct_senders_7d": "distinct_senders_7d",
		"evidence_flow_through_ratio":  "flow_through_ratio", "evidence_dwell_minutes": "dwell_minutes",
		"evidence_sim_swap_age_days": "sim_swap_age_days",
	} {
		if row[source] != "" {
			evidence[destination] = row[source]
		}
	}
	if _, ok := row["reporting_institution"]; !ok {
		evidence["is_new_device"] = strconv.FormatBool(strings.EqualFold(row["device_status"], "new_device") || boolValue(row["new_device"]))
		evidence["is_new_beneficiary"] = strconv.FormatBool(floatValue(row["beneficiary_age_minutes"], 999999) <= 60)
	}
	return ReportIn{
		ReportingInstitution: reporting, ReportingSystem: first(row["reporting_system"], row["bank_flag_source"], "AG Screener"),
		TransactionRef: transactionRef, TransactionTimestamp: first(row["transaction_timestamp"], utcNow()),
		SubjectAccountHash:     firstHash(row["subject_account_hash"], customer),
		SubjectCustomerHash:    firstHash(row["subject_customer_hash"], customer),
		DestinationAccountHash: firstHash(row["destination_account_hash"], first(row["destination_account"], receiving+":"+transactionRef)),
		DestinationMSISDNHash:  hashIfNeeded(row["destination_msisdn_hash"]),
		DestinationInstitution: receiving, Amount: floatValue(row["amount"], 0),
		Currency: first(row["currency"], "KES"), Channel: first(row["channel"], row["payment_rail"]),
		BankRiskScore: floatValue(row["bank_risk_score"], 0.8),
		BankThreshold: floatValue(row["bank_threshold"], 0.5), RiskCodes: codes, Evidence: evidence,
		Narrative: first(row["narrative"], reporting+" submitted "+transactionRef+" from CSV upload."),
	}
}

func dashboardValidation(result map[string]any, report ReportIn) map[string]any {
	validation := result["validation"].(Validation)
	described := result["risk_codes"].([]map[string]any)
	riskCodes := make([]map[string]any, 0, len(described))
	for _, item := range described {
		riskCodes = append(riskCodes, map[string]any{
			"code": item["code"], "label": item["name"], "evidence": item["family"],
		})
	}
	status := map[string]string{
		"VALIDATED_FRAUD": "validated_fraud", "NOT_FRAUD": "not_fraud",
		"INSUFFICIENT_EVIDENCE": "needs_review",
	}[validation.Status]
	action := "No receiving-bank alert; keep result in history."
	if validation.Status == "VALIDATED_FRAUD" {
		action = "Send fraud alert to " + displayInstitution(report.DestinationInstitution) + "."
	}
	customerRef := report.SubjectCustomerHash
	if len(customerRef) > 4 {
		customerRef = "*" + customerRef[len(customerRef)-4:]
	}
	return map[string]any{
		"status": status, "confidence": int(math.Round(validation.ValidationScore * 100)),
		"validated_by": "Kifaru agent", "bank_flag_source": report.ReportingSystem,
		"reporting_bank": displayInstitution(report.ReportingInstitution),
		"receiving_bank": displayInstitution(report.DestinationInstitution),
		"transaction_id": report.TransactionRef, "customer_ref": customerRef,
		"amount": fmt.Sprintf("%s %.0f", report.Currency, report.Amount), "currency": report.Currency,
		"risk_codes": riskCodes, "key_signals": validation.ReasonCodes,
		"destination_hash":           first(report.DestinationAccountHash, report.DestinationMSISDNHash),
		"corroborating_institutions": validation.CorroboratingInstitutions,
		"corroboration_count":        validation.CorroborationCount, "missing_fields": []string{},
		"recommended_action": action, "human_review_required": validation.Status == "INSUFFICIENT_EVIDENCE",
		"short_explanation": validation.Explanation, "created_at": validation.ValidatedAt,
	}
}
