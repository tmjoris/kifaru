package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
)

func (a *App) submitCSV(w http.ResponseWriter, r *http.Request) {
	reader := csv.NewReader(http.MaxBytesReader(w, r.Body, 2<<20))
	reader.ReuseRecord = false
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		writeError(w, 400, "CSV must include headers and at least one data row")
		return
	}
	if len(records)-1 > 1000 {
		writeError(w, http.StatusRequestEntityTooLarge, "CSV uploads are limited to 1,000 data rows")
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
		report, err := reportFromCSV(row)
		if err != nil {
			errs = append(errs, map[string]any{"index": index, "error": err.Error()})
			continue
		}
		if report.TransactionRef == "" {
			report.TransactionRef = fmt.Sprintf("uploaded-transaction-%04d", index+1)
		}
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
	counts := map[string]int{"corroborated": 0, "below_threshold": 0, "needs_review": 0}
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
			"total_rows": len(validations), "corroborated": counts["corroborated"],
			"below_threshold": counts["below_threshold"], "needs_review": counts["needs_review"],
			"top_risk_codes": topCodes,
		},
		"validations": validations, "errors": errs,
	})
}

// cleartextCSVColumns names columns that carry raw customer identifiers. The
// browser hashes them before upload, so any unhashed value here is refused.
var cleartextCSVColumns = []string{
	"customer_ref", "customer", "customer_name", "account_number",
	"destination_account", "destination_msisdn", "msisdn", "phone_number",
	"device_profile", "evidence_device_profile",
}

func reportFromCSV(row map[string]string) (ReportIn, error) {
	for _, column := range cleartextCSVColumns {
		if value := strings.TrimSpace(row[column]); value != "" && !strings.HasPrefix(value, "sha256:") {
			return ReportIn{}, fmt.Errorf(
				"column %q holds a cleartext identifier; hash identifiers before upload", column)
		}
	}
	reporting := institutionCode(first(row["reporting_institution"], row["reporting_bank"]))
	receiving := institutionCode(first(row["destination_institution"], row["receiving_bank"]))
	transactionRef := first(row["transaction_ref"], row["transaction_id"])
	customer := first(row["subject_customer_hash"], row["customer_ref"], row["customer"])
	codes := splitCodes(row["risk_codes"])
	if len(codes) == 0 {
		codes = deriveUploadCodes(row)
	}
	evidence := map[string]any{}
	for destination, sources := range map[string][]string{
		"device_profile":      {"evidence_device_profile", "device_profile"},
		"account_age_days":    {"evidence_account_age_days", "account_age_days"},
		"distinct_senders_7d": {"evidence_distinct_senders_7d", "distinct_senders_7d"},
		"flow_through_ratio":  {"evidence_flow_through_ratio", "flow_through_ratio"},
		"dwell_minutes":       {"evidence_dwell_minutes", "dwell_minutes"},
		"sim_swap_age_days":   {"evidence_sim_swap_age_days", "sim_swap_age_days"},
		"velocity_1h":         {"evidence_velocity_1h", "transfers_1h", "transfers_5m"},
	} {
		for _, source := range sources {
			if row[source] != "" {
				evidence[destination] = row[source]
				break
			}
		}
	}
	for destination, sources := range map[string][]string{
		"is_new_device":      {"evidence_is_new_device", "new_device"},
		"is_new_beneficiary": {"evidence_is_new_beneficiary", "new_beneficiary"},
		"is_emulator":        {"evidence_is_emulator", "is_emulator"},
		"is_rooted":          {"evidence_is_rooted", "is_rooted"},
		"ip_country_changed": {"evidence_ip_country_changed", "ip_country_changed"},
		"vpn_proxy_tor":      {"evidence_vpn_proxy_tor", "vpn_proxy_tor"},
		"credential_changed": {"evidence_credential_changed", "credential_changed", "password_reset_within_1h"},
	} {
		for _, source := range sources {
			if row[source] != "" {
				evidence[destination] = boolValue(row[source])
				break
			}
		}
	}
	if _, exists := evidence["is_new_device"]; !exists &&
		strings.EqualFold(row["device_status"], "new_device") {
		evidence["is_new_device"] = true
	}
	if _, exists := evidence["is_new_beneficiary"]; !exists &&
		floatValue(row["beneficiary_age_minutes"], 999999) <= 60 {
		evidence["is_new_beneficiary"] = true
	}
	return ReportIn{
		ReportingInstitution: reporting, ReportingSystem: first(row["reporting_system"], row["bank_flag_source"], "AG Screener"),
		TransactionRef: transactionRef, TransactionTimestamp: first(row["transaction_timestamp"], utcNow()),
		SubjectAccountHash:     first(row["subject_account_hash"], customer),
		SubjectCustomerHash:    customer,
		DestinationAccountHash: first(row["destination_account_hash"], row["destination_account"]),
		DestinationMSISDNHash:  first(row["destination_msisdn_hash"], row["destination_msisdn"]),
		DestinationInstitution: receiving, Amount: floatValue(row["amount"], 0),
		Currency: first(row["currency"], "KES"), Channel: first(row["channel"], row["payment_rail"]),
		BankRiskScore: floatValue(row["bank_risk_score"], 0.8),
		BankThreshold: floatValue(row["bank_threshold"], 0.5), RiskCodes: codes, Evidence: evidence,
		Narrative: first(row["narrative"], reporting+" submitted "+transactionRef+" from CSV upload."),
	}, nil
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
		statusCorroborated: "corroborated", statusBelow: "below_threshold",
		statusAwaiting: "needs_review",
	}[validation.Status]
	action := "No receiving-institution alert; keep the signal in history."
	if validation.Status == statusCorroborated {
		action = "Send a corroborated risk alert to " + displayInstitution(report.DestinationInstitution) + "."
	}
	customerRef := report.SubjectCustomerHash
	if len(customerRef) > 4 {
		customerRef = "*" + customerRef[len(customerRef)-4:]
	}
	return map[string]any{
		"status": status, "confidence": int(math.Round(validation.ValidationScore * 100)),
		"validated_by": "Kifaru policy engine", "bank_flag_source": report.ReportingSystem,
		"reporting_bank": displayInstitution(report.ReportingInstitution),
		"receiving_bank": displayInstitution(report.DestinationInstitution),
		"transaction_id": report.TransactionRef, "customer_ref": customerRef,
		"amount": fmt.Sprintf("%s %.0f", report.Currency, report.Amount), "currency": report.Currency,
		"risk_codes": riskCodes, "key_signals": validation.ReasonCodes,
		"destination_hash":           first(report.DestinationAccountHash, report.DestinationMSISDNHash),
		"corroborating_institutions": validation.CorroboratingInstitutions,
		"corroboration_count":        validation.CorroborationCount, "missing_fields": []string{},
		"recommended_action": action, "human_review_required": validation.Status == statusAwaiting,
		"short_explanation": validation.Explanation, "created_at": validation.ValidatedAt,
	}
}
