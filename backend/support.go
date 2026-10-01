package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) getConfig(ctx context.Context) (map[string]any, error) {
	return getConfigFrom(ctx, a.db)
}

func getConfigFrom(ctx context.Context, store dbRunner) (map[string]any, error) {
	rows, err := store.Query(ctx, "SELECT key,value FROM config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (a *App) setConfig(ctx context.Context, key string, value any, actor string) error {
	var oldValue string
	_ = a.db.QueryRow(ctx, "SELECT value FROM config WHERE key=$1", key).Scan(&oldValue)
	encoded, _ := json.Marshal(value)
	if _, err := a.db.Exec(ctx, `INSERT INTO config(key,value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value`, key, string(encoded)); err != nil {
		return err
	}
	return auditRecord(ctx, a.db, actor, "config.set", key, oldValue, string(encoded), "configuration update")
}

func kbListsFrom(ctx context.Context, store dbRunner) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{"known_good": {}, "known_bad": {}}
	rows, err := store.Query(ctx, "SELECT artefact_hash,list_name FROM knowledge_base")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash, list string
		if err := rows.Scan(&hash, &list); err != nil {
			return nil, err
		}
		if out[list] == nil {
			out[list] = map[string]bool{}
		}
		out[list][hash] = true
	}
	return out, rows.Err()
}

func (a *App) kbAdd(ctx context.Context, hash, list, label, actor string) error {
	return kbAddWith(ctx, a.db, hash, list, label, actor)
}

func kbAddWith(ctx context.Context, store dbRunner, hash, list, label, actor string) error {
	if _, err := store.Exec(ctx, `INSERT INTO knowledge_base VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (artefact_hash,list_name) DO NOTHING`, hash, list, label, actor, utcNow()); err != nil {
		return err
	}
	newValue, _ := json.Marshal(map[string]string{"list": list, "label": label})
	return auditRecord(ctx, store, actor, "kb.add", hash, "", string(newValue), "knowledge base update")
}

func (a *App) audit(ctx context.Context, actor, action, target, detail string) error {
	return auditRecord(ctx, a.db, actor, action, target, "", detail, "")
}

func auditRecord(
	ctx context.Context,
	store dbRunner,
	actor, action, target, oldValue, newValue, reason string,
) error {
	detail := newValue
	_, err := store.Exec(ctx, `INSERT INTO audit_log(
		at,actor,action,target,detail,old_value,new_value,reason
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		utcNow(), actor, action, target, detail, oldValue, newValue, reason)
	return err
}

func (a *App) queryRows(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	rows, err := a.rows(r.Context(), query, args...)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

func (a *App) rows(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	return rowsFrom(ctx, a.db, query, args...)
}

func rowsFrom(ctx context.Context, store dbRunner, query string, args ...any) ([]map[string]any, error) {
	rows, err := store.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToMap)
}

func (a *App) groupCounts(ctx context.Context, query string) (map[string]int, error) {
	rows, err := a.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		out[key] = count
	}
	return out, rows.Err()
}

func reportFromRow(row map[string]any) (ReportIn, []string, error) {
	var codes []string
	var evidence map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(row["risk_codes"])), &codes); err != nil {
		return ReportIn{}, nil, err
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(row["evidence"])), &evidence); err != nil {
		return ReportIn{}, nil, err
	}
	return ReportIn{
		ReportingInstitution:   fmt.Sprint(row["reporting_institution"]),
		ReportingSystem:        fmt.Sprint(row["reporting_system"]),
		TransactionRef:         fmt.Sprint(row["transaction_ref"]),
		TransactionTimestamp:   fmt.Sprint(row["transaction_timestamp"]),
		SubjectAccountHash:     fmt.Sprint(row["subject_account_hash"]),
		SubjectCustomerHash:    fmt.Sprint(row["subject_customer_hash"]),
		DestinationAccountHash: fmt.Sprint(row["destination_account_hash"]),
		DestinationMSISDNHash:  fmt.Sprint(row["destination_msisdn_hash"]),
		DestinationInstitution: fmt.Sprint(row["destination_institution"]),
		Amount:                 numberOr(row["amount"], 0), Currency: fmt.Sprint(row["currency"]),
		Channel: fmt.Sprint(row["channel"]), BankRiskScore: numberOr(row["bank_risk_score"], 0),
		BankThreshold: numberOr(row["bank_threshold"], 0.5), Evidence: evidence,
		Narrative: fmt.Sprint(row["narrative"]),
	}, codes, nil
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 10<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"detail": message})
}

func utcNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	return min(value, 5000)
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func numberOr(value any, fallback float64) float64 {
	if number, ok := numberValue(value); ok {
		return number
	}
	return fallback
}

func toStringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		if direct, ok := value.([]string); ok {
			return direct
		}
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func uniqueSorted(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func nullSentinel(value string) string {
	if value == "" {
		return "\x00"
	}
	return value
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func floatValue(value string, fallback float64) float64 {
	cleaned := strings.NewReplacer(",", "", "KES", "", "USD", "").Replace(value)
	number, err := strconv.ParseFloat(strings.TrimSpace(cleaned), 64)
	if err != nil {
		return fallback
	}
	return number
}

func boolValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func truthy(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return value != nil && boolValue(fmt.Sprint(value))
}

func splitCodes(value string) []string {
	value = strings.ReplaceAll(value, ",", "|")
	out := []string{}
	for _, code := range strings.Split(value, "|") {
		if code = strings.TrimSpace(code); code != "" && !containsString(out, code) {
			out = append(out, code)
		}
	}
	return out
}

func deriveUploadCodes(row map[string]string) []string {
	codes := []string{}
	add := func(code string) {
		if !containsString(codes, code) {
			codes = append(codes, code)
		}
	}
	if floatValue(row["amount"], 0) >= 250000 {
		add("BEN-450")
	}
	if floatValue(row["transfers_5m"], 0) >= 4 || floatValue(row["transfers_1h"], 0) >= 8 {
		add("VEL-429")
	}
	if boolValue(row["ip_country_changed"]) || boolValue(row["vpn_proxy_tor"]) {
		add("IP-404")
	}
	if strings.EqualFold(row["device_status"], "new_device") || boolValue(row["new_device"]) {
		add("IP-401")
	}
	if floatValue(row["beneficiary_age_minutes"], 999999) <= 60 {
		add("VEL-430")
		add("BEN-450")
	}
	if boolValue(row["password_reset_within_1h"]) {
		add("ATO-461")
	}
	if boolValue(row["vendor_bank_change"]) || boolValue(row["bec_signal"]) {
		add("BEN-450")
	}
	if len(codes) == 0 {
		add("VEL-431")
	}
	return codes
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func institutionCode(value string) string {
	wanted := strings.TrimSpace(value)
	for _, institution := range kenyanBanks {
		for _, candidate := range []string{
			institution.Code, institution.ID, institution.Name, institution.LegalName, institution.Ref,
		} {
			if candidate != "" && strings.EqualFold(candidate, wanted) {
				return institution.Code
			}
		}
	}
	return wanted
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func displayInstitution(value string) string {
	for _, institution := range kenyanBanks {
		if institution.Code == value {
			return institution.Name
		}
	}
	if value == "" {
		return "External network"
	}
	return value
}
