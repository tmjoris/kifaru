package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) history(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	filter := "WHERE r.reporting_institution=$1 OR r.destination_institution=$1"
	if institution == "*" {
		filter = "WHERE $1::text IS NOT NULL"
	}
	limit := queryLimit(r, 500)
	rows, err := a.rows(r.Context(), `SELECT r.report_id,r.submitted_at,r.reporting_institution,
		r.destination_institution,r.reporting_system,r.transaction_ref,r.subject_customer_hash,
		r.destination_account_hash,r.destination_msisdn_hash,r.amount,r.currency,r.channel,
		r.risk_codes,r.evidence,r.narrative,v.validated_at,v.agent_version,v.status,
		v.validation_score,v.reason_codes,v.corroborating_institutions,v.corroboration_count,
		v.explanation,v.alert_id,a.state AS alert_state,a.alert_type
		FROM reports r
		LEFT JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		LEFT JOIN alerts a ON a.alert_id=v.alert_id
		`+filter+`
		ORDER BY r.submitted_at DESC LIMIT $2`, institution, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"institution": institution, "history": rows})
}

func (a *App) reports(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	rows, err := a.rows(r.Context(), `SELECT r.*,v.status,v.validation_score,v.reason_codes,
		v.explanation,v.alert_id,a.state AS alert_state,a.alert_type
		FROM reports r
		LEFT JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		LEFT JOIN alerts a ON a.alert_id=v.alert_id
		WHERE r.reporting_institution=$1 ORDER BY r.submitted_at DESC LIMIT $2`,
		institution, queryLimit(r, 500))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"institution": institution, "reports": rows})
}

func (a *App) alerts(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after")
	query := "SELECT * FROM alerts WHERE receiving_institution=$1"
	args := []any{institution}
	if after != "" {
		query += " AND issued_at>$2"
		args = append(args, after)
	}
	query += fmt.Sprintf(" ORDER BY issued_at DESC LIMIT $%d", len(args)+1)
	args = append(args, queryLimit(r, 200))
	rows, err := a.rows(r.Context(), query, args...)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"institution": institution, "alerts": rows})
}

func (a *App) validationDetail(w http.ResponseWriter, r *http.Request, reportID string) {
	rows, err := a.rows(r.Context(), `SELECT v.*,r.reporting_institution,r.destination_institution
		FROM validations v
		JOIN reports r ON r.report_id=v.report_id
		WHERE v.report_id=$1 ORDER BY v.is_current DESC,v.validated_at DESC LIMIT 1`, reportID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(rows) == 0 {
		writeError(w, 404, "no validation for that report")
		return
	}
	user := authUserFromContext(r.Context())
	reportingInstitution := fmt.Sprint(rows[0]["reporting_institution"])
	destinationInstitution := fmt.Sprint(rows[0]["destination_institution"])
	if user.Role == "institution" && user.InstitutionCode != reportingInstitution &&
		user.InstitutionCode != destinationInstitution {
		writeError(w, http.StatusForbidden, "you cannot access this validation")
		return
	}
	writeJSON(w, 200, rows[0])
}

func (a *App) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := map[string]string{
		"reports": "SELECT COUNT(*) FROM reports", "validations": "SELECT COUNT(*) FROM validations",
		"validated_fraud": "SELECT COUNT(*) FROM validations WHERE status='VALIDATED_FRAUD'",
		"insufficient":    "SELECT COUNT(*) FROM validations WHERE status='INSUFFICIENT_EVIDENCE'",
		"not_fraud":       "SELECT COUNT(*) FROM validations WHERE status='NOT_FRAUD'",
		"alerts":          "SELECT COUNT(*) FROM alerts",
		"kb_known_bad":    "SELECT COUNT(*) FROM knowledge_base WHERE list_name='known_bad'",
		"kb_known_good":   "SELECT COUNT(*) FROM knowledge_base WHERE list_name='known_good'",
	}
	out := map[string]any{}
	for key, query := range counts {
		var count int
		if err := a.db.QueryRow(ctx, query).Scan(&count); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		out[key] = count
	}
	var avg float64
	_ = a.db.QueryRow(ctx, "SELECT COALESCE(AVG(latency_ms),0) FROM validations").Scan(&avg)
	out["avg_latency_ms"] = math.Round(avg*10) / 10
	out["alerts_by_institution"], _ = a.groupCounts(ctx, "SELECT receiving_institution,COUNT(*) FROM alerts GROUP BY 1")
	out["reports_by_channel"], _ = a.groupCounts(ctx, "SELECT submission_channel,COUNT(*) FROM reports GROUP BY 1")
	writeJSON(w, 200, out)
}

func (a *App) config(w http.ResponseWriter, r *http.Request) {
	user := authUserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.getConfig(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, cfg)
	case http.MethodPatch:
		var patch map[string]any
		if err := decodeJSON(r, &patch); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		if user.Role != "staff" {
			for key := range patch {
				if key != "institution_thresholds" {
					writeError(w, http.StatusForbidden, "institution users can only update their own threshold")
					return
				}
			}
		}
		changed := false
		for _, key := range []string{"validated_threshold", "insufficient_threshold", "enabled_sources"} {
			if value, ok := patch[key]; ok && value != nil {
				if user.Role != "staff" {
					writeError(w, http.StatusForbidden, "staff access is required for global configuration")
					return
				}
				if err := a.setConfig(r.Context(), key, value, user.Email); err != nil {
					writeError(w, 500, err.Error())
					return
				}
				changed = true
			}
		}
		if thresholds, ok := patch["institution_thresholds"].(map[string]any); ok {
			for code, value := range thresholds {
				if user.Role == "institution" && code != user.InstitutionCode {
					writeError(w, http.StatusForbidden, "you can only update your assigned institution")
					return
				}
				if threshold, ok := numberValue(value); ok {
					var oldThreshold float64
					err := a.db.QueryRow(r.Context(), "SELECT threshold FROM institutions WHERE code=$1", code).Scan(&oldThreshold)
					if err != nil {
						writeError(w, 404, fmt.Sprintf("unknown institution %q", code))
						return
					}
					if _, err := a.db.Exec(r.Context(), "UPDATE institutions SET threshold=$1 WHERE code=$2", threshold, code); err != nil {
						writeError(w, 500, err.Error())
						return
					}
					if err := auditRecord(r.Context(), a.db, user.Email, "institution.threshold", code,
						strconv.FormatFloat(oldThreshold, 'f', -1, 64),
						strconv.FormatFloat(threshold, 'f', -1, 64),
						"institution threshold update"); err != nil {
						writeError(w, 500, err.Error())
						return
					}
					changed = true
				}
			}
		}
		if changed {
			cfg, err := a.getConfig(r.Context())
			if err != nil {
				writeError(w, 500, err.Error())
				return
			}
			nextVersion := int(numberOr(cfg["configuration_version"], 1)) + 1
			if err := a.setConfig(r.Context(), "configuration_version", nextVersion, user.Email); err != nil {
				writeError(w, 500, err.Error())
				return
			}
		}
		cfg, _ := a.getConfig(r.Context())
		writeJSON(w, 200, cfg)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *App) knowledgeBase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := authUserFromContext(ctx)
	switch r.Method {
	case http.MethodGet:
		listName := r.URL.Query().Get("list_name")
		query := "SELECT * FROM knowledge_base"
		args := []any{}
		if listName != "" {
			query += " WHERE list_name=$1"
			args = append(args, listName)
		}
		rows, err := a.rows(ctx, query, args...)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, rows)
	case http.MethodPost:
		var entry struct {
			ArtefactHash string `json:"artefact_hash"`
			ListName     string `json:"list_name"`
			Label        string `json:"label"`
			AddedBy      string `json:"added_by"`
		}
		if err := decodeJSON(r, &entry); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		entry.AddedBy = user.Email
		if err := a.kbAdd(ctx, entry.ArtefactHash, entry.ListName, entry.Label, entry.AddedBy); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "artefact_hash": entry.ArtefactHash, "list_name": entry.ListName, "label": entry.Label, "added_by": entry.AddedBy})
	case http.MethodDelete:
		hash := r.URL.Query().Get("artefact_hash")
		listName := r.URL.Query().Get("list_name")
		_, err := a.db.Exec(ctx, "DELETE FROM knowledge_base WHERE artefact_hash=$1 AND list_name=$2", hash, listName)
		if err == nil {
			err = a.audit(ctx, user.Email, "kb.remove", hash, listName)
		}
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *App) revalidate(w http.ResponseWriter, r *http.Request) {
	reportID := r.URL.Query().Get("report_id")
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	rows, err := rowsFrom(r.Context(), tx, "SELECT * FROM reports WHERE report_id=$1", reportID)
	if err != nil || len(rows) == 0 {
		writeError(w, 404, "unknown report")
		return
	}
	report, codes, err := reportFromRow(rows[0])
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	cfg, _ := getConfigFrom(r.Context(), tx)
	validation, err := a.score(r.Context(), tx, report, reportID, codes, cfg, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	validation.Explanation = a.explain(report, validation, codes)
	var previousID, previousStatus string
	var previousScore float64
	err = tx.QueryRow(r.Context(), `SELECT validation_id,status,validation_score
		FROM validations WHERE report_id=$1 AND is_current=1
		ORDER BY validated_at DESC LIMIT 1`, reportID).Scan(&previousID, &previousStatus, &previousScore)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, err.Error())
		return
	}
	if previousID != "" {
		if _, err = tx.Exec(r.Context(), "UPDATE validations SET is_current=0 WHERE validation_id=$1", previousID); err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	reasons, _ := json.Marshal(validation.ReasonCodes)
	corro, _ := json.Marshal(validation.CorroboratingInstitutions)
	_, err = tx.Exec(r.Context(), `INSERT INTO validations (
		validation_id,report_id,validated_at,agent_version,validation_score,status,
		reason_codes,corroborating_institutions,corroboration_count,explanation,
		latency_ms,alert_id,configuration_version,supersedes_validation_id,is_current
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,1)`,
		validation.ValidationID, reportID, validation.ValidatedAt, validation.AgentVersion,
		validation.ValidationScore, validation.Status, string(reasons), string(corro),
		validation.CorroborationCount, validation.Explanation, validation.LatencyMS, nil,
		int(numberOr(cfg["configuration_version"], 1)), nullIfEmpty(previousID))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	oldValue, _ := json.Marshal(map[string]any{"status": previousStatus, "score": previousScore})
	newValue, _ := json.Marshal(map[string]any{"status": validation.Status, "score": validation.ValidationScore})
	user := authUserFromContext(r.Context())
	if err := auditRecord(r.Context(), tx, user.Email, "validation.revalidated",
		validation.ValidationID, string(oldValue), string(newValue), "manual revalidation"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, validation)
}

func (a *App) auditLog(w http.ResponseWriter, r *http.Request) {
	rows, err := a.rows(r.Context(), "SELECT * FROM audit_log ORDER BY id DESC LIMIT $1", queryLimit(r, 100))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

func (a *App) setAlertState(w http.ResponseWriter, r *http.Request, alertID string) {
	state := r.URL.Query().Get("state")
	comment := ""
	if state == "" {
		var body struct {
			State   string `json:"state"`
			Comment string `json:"comment"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		state = body.State
		comment = strings.TrimSpace(body.Comment)
	}
	if !containsString([]string{"acknowledged", "actioned", "disputed"}, state) {
		writeError(w, 422, "invalid alert state")
		return
	}
	if state == "disputed" && comment == "" {
		writeError(w, 422, "a dispute comment is required")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var oldState, reportingInstitution, receivingInstitution string
	err = tx.QueryRow(r.Context(), `SELECT state,reporting_institution,receiving_institution
		FROM alerts WHERE alert_id=$1 FOR UPDATE`, alertID).
		Scan(&oldState, &reportingInstitution, &receivingInstitution)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "unknown alert")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	user := authUserFromContext(r.Context())
	if user.Role == "institution" && user.InstitutionCode != receivingInstitution {
		writeError(w, http.StatusForbidden, "only the receiving institution can update this alert")
		return
	}
	validTransition := (oldState == "sent" && (state == "acknowledged" || state == "disputed")) ||
		(oldState == "acknowledged" && (state == "actioned" || state == "disputed")) ||
		oldState == state
	if !validTransition {
		writeError(w, 409, fmt.Sprintf("cannot move alert from %s to %s", oldState, state))
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE alerts SET state=$1 WHERE alert_id=$2", state, alertID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO alert_actions(alert_id,action,comment,actor,at)
		VALUES ($1,$2,$3,$4,$5)`, alertID, state, nullIfEmpty(comment), user.Email, utcNow()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if state == "disputed" {
		payload, _ := json.Marshal(map[string]string{
			"alert_id": alertID, "comment": comment,
			"reporting_institution": reportingInstitution,
			"receiving_institution": receivingInstitution,
		})
		if _, err = tx.Exec(r.Context(), `INSERT INTO notifications(
			institution_code,event_type,record_id,created_at,payload
		) VALUES ($1,'alert.disputed',$2,$3,$4)`,
			reportingInstitution, alertID, utcNow(), string(payload)); err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	if err := auditRecord(r.Context(), tx, user.Email, "alert.state", alertID,
		oldState, state, comment); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"alert_id": alertID, "state": state, "previous_state": oldState, "comment": comment,
	})
}
