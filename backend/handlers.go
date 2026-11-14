package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	historyCacheTTL = 10 * time.Second
	statsCacheTTL   = 5 * time.Second
)

func (a *App) history(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	limit := queryLimit(r, 500)
	// Everyone allowed to ask for this institution gets the same answer, so the
	// query runs at most once per TTL however many tabs are open.
	a.cache.serve(w, r, fmt.Sprintf("history|%s|%d", institution, limit), historyCacheTTL,
		func(ctx context.Context) ([]byte, error) { return a.historyJSON(ctx, institution, limit) })
}

// historySelect is the column list every history row has, whether it is sent as
// part of a snapshot or pushed on its own. subjectHash says who may see the
// customer hash.
func historySelect(subjectHash string) string {
	return `SELECT r.report_id,r.submitted_at,r.reporting_institution,
		r.destination_institution,r.reporting_system,r.transaction_ref,
		` + subjectHash + ` AS subject_customer_hash,
		r.destination_account_hash,r.destination_msisdn_hash,r.amount,r.currency,r.channel,
		r.risk_codes,r.evidence,r.narrative,r.lifecycle_state,r.expires_at,
		v.validated_at,v.agent_version,v.status,
		v.validation_score,v.reason_codes,v.corroborating_institutions,v.corroboration_count,
		v.explanation,v.alert_id,a.state AS alert_state,a.alert_type,a.alert_tier,
		a.outcome AS alert_outcome,a.outcome_note
		`
}

// historyJSON returns the newest reports an institution reported or received,
// or every report for the staff view ("*"), as the finished response body.
//
// Two things keep it cheap. It picks the newest ids from the
// (institution, submitted_at) indexes first and only then joins, so the work is
// proportional to the limit and not to the size of the table. And the database
// writes the JSON itself: this server has a tenth of a CPU, the database does
// not, and turning 2,000 rows into Go maps and back into JSON was most of the
// CPU the dashboard cost.
func (a *App) historyJSON(ctx context.Context, institution string, limit int) ([]byte, error) {
	picked := `(SELECT * FROM reports ORDER BY submitted_at DESC LIMIT $2)`
	if institution != "*" {
		picked = `(SELECT * FROM (
			(SELECT * FROM reports WHERE reporting_institution=$1
				ORDER BY submitted_at DESC LIMIT $2)
			UNION ALL
			(SELECT * FROM reports WHERE destination_institution=$1
				AND reporting_institution IS DISTINCT FROM $1
				ORDER BY submitted_at DESC LIMIT $2)
		) both_sides ORDER BY submitted_at DESC LIMIT $2)`
	}
	// Read the cursor first: anything that changes after this point reaches the
	// tab through the stream, replayed from here.
	cursor := a.hub.cursor()
	var rows []byte
	err := a.db.QueryRow(ctx, `SELECT COALESCE(json_agg(row_to_json(h) ORDER BY h.submitted_at DESC),'[]')::text
		FROM (`+historySelect("CASE WHEN $1='*' OR r.reporting_institution=$1 THEN r.subject_customer_hash ELSE '' END")+`
		FROM `+picked+` r
		LEFT JOIN validations v ON v.report_id=r.report_id AND v.is_current=1
		LEFT JOIN alerts a ON a.alert_id=v.alert_id) h`, institution, limit).Scan(&rows)
	if err != nil {
		return nil, err
	}
	name, _ := json.Marshal(institution)
	body := make([]byte, 0, len(rows)+len(name)+40)
	body = append(body, `{"institution":`...)
	body = append(body, name...)
	body = append(body, `,"cursor":`...)
	body = strconv.AppendQuote(body, cursor)
	body = append(body, `,"history":`...)
	body = append(body, rows...)
	return append(body, '}'), nil
}

func (a *App) reports(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	rows, err := a.rows(r.Context(), `SELECT r.*,v.status,v.validation_score,v.reason_codes,
		v.explanation,v.alert_id,a.state AS alert_state,a.alert_type,a.alert_tier
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
	a.cache.serve(w, r, "stats", statsCacheTTL, func(ctx context.Context) ([]byte, error) {
		out, err := a.computeStats(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	})
}

// computeStats answers the staff dashboard's counters with four queries, run
// side by side, instead of fourteen run one after another. Each table is read
// once and every counter is a FILTER on that single pass.
func (a *App) computeStats(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	run := func(job func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := job(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	run(func() error {
		var validations, corroborated, awaiting, below, review, notice int
		var avg float64
		err := a.db.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE is_current=1 AND status='CORROBORATED_SIGNAL'),
			count(*) FILTER (WHERE is_current=1 AND status='AWAITING_CORROBORATION'),
			count(*) FILTER (WHERE is_current=1 AND status='BELOW_ALERT_THRESHOLD'),
			count(*) FILTER (WHERE is_current=1 AND status='SINGLE_SOURCE_REVIEW'),
			count(*) FILTER (WHERE is_current=1 AND status='SINGLE_SOURCE_NOTICE'),
			COALESCE(AVG(latency_ms) FILTER (WHERE is_current=1 AND (status = ANY($1) OR status=$2)),0)
			FROM validations`, qualifyingStatuses, statusBelow).Scan(
			&validations, &corroborated, &awaiting, &below, &review, &notice, &avg)
		if err != nil {
			return err
		}
		mu.Lock()
		out["validations"], out["corroborated"], out["awaiting"] = validations, corroborated, awaiting
		out["below_policy"], out["single_source_review"], out["single_source_notice"] = below, review, notice
		out["avg_latency_ms"] = math.Round(avg*10) / 10
		mu.Unlock()
		return nil
	})
	run(func() error {
		rows, err := a.db.Query(ctx, `SELECT COALESCE(submission_channel,''),count(*),
			count(*) FILTER (WHERE lifecycle_state='quarantined'),
			count(*) FILTER (WHERE lifecycle_state='retracted')
			FROM reports GROUP BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		total, quarantined, retracted := 0, 0, 0
		byChannel := map[string]int{}
		for rows.Next() {
			var channel string
			var count, quarantinedHere, retractedHere int
			if err := rows.Scan(&channel, &count, &quarantinedHere, &retractedHere); err != nil {
				return err
			}
			byChannel[channel] = count
			total += count
			quarantined += quarantinedHere
			retracted += retractedHere
		}
		if err := rows.Err(); err != nil {
			return err
		}
		mu.Lock()
		out["reports"], out["quarantined"], out["retracted"], out["reports_by_channel"] = total, quarantined, retracted, byChannel
		mu.Unlock()
		return nil
	})
	run(func() error {
		var alerts, actioned, disputed int
		if err := a.db.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE state='actioned'),
			count(*) FILTER (WHERE state='disputed') FROM alerts`).Scan(&alerts, &actioned, &disputed); err != nil {
			return err
		}
		mu.Lock()
		out["alerts"], out["alerts_actioned"], out["alerts_disputed"] = alerts, actioned, disputed
		mu.Unlock()
		return nil
	})
	run(func() error {
		byInstitution, err := a.groupCounts(ctx, "SELECT receiving_institution,COUNT(*) FROM alerts GROUP BY 1")
		if err != nil {
			return err
		}
		mu.Lock()
		out["alerts_by_institution"] = byInstitution
		mu.Unlock()
		return nil
	})
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
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
					if threshold < 0.50 || threshold > 0.99 {
						writeError(w, http.StatusUnprocessableEntity,
							"institution thresholds must be between 0.50 and 0.99")
						return
					}
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
		if user.Role == "institution" {
			query = `SELECT
				CASE WHEN LENGTH(artefact_hash)>18
					THEN LEFT(artefact_hash,11) || '…' || RIGHT(artefact_hash,4)
					ELSE artefact_hash END AS artefact_hash,
				list_name,label,added_by,added_at FROM knowledge_base`
		}
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
	if reportID == "" {
		writeError(w, http.StatusUnprocessableEntity, "report_id is required")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var exists bool
	if err := tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM reports WHERE report_id=$1)",
		reportID).Scan(&exists); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !exists {
		writeError(w, 404, "unknown report")
		return
	}
	user := authUserFromContext(r.Context())
	cfg, err := getConfigFrom(r.Context(), tx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	changedAlerts, err := a.recalculateLinkedReport(
		r.Context(), tx, reportID, cfg, user.Email, "manual revalidation",
	)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, alert := range changedAlerts {
		a.publishAlert(fmt.Sprint(alert["receiving_institution"]), alert)
	}
	rows, err := a.rows(r.Context(), `SELECT * FROM validations
		WHERE report_id=$1 AND is_current=1 LIMIT 1`, reportID)
	if err != nil || len(rows) == 0 {
		writeError(w, 500, "revalidation result is unavailable")
		return
	}
	writeJSON(w, 200, rows[0])
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
	outcome := r.URL.Query().Get("outcome")
	comment := ""
	if state == "" {
		var body struct {
			State   string `json:"state"`
			Outcome string `json:"outcome"`
			Comment string `json:"comment"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		state = body.State
		outcome = body.Outcome
		comment = strings.TrimSpace(body.Comment)
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	user := authUserFromContext(r.Context())
	result, changedAlerts, apiErr := a.applyAlertTransition(
		r.Context(), tx, alertID, state, outcome, comment, user,
	)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.message)
		return
	}
	if result["idempotent"] != true {
		oldValue, _ := json.Marshal(map[string]any{
			"state": result["previous_state"], "outcome": result["previous_outcome"],
		})
		newValue, _ := json.Marshal(map[string]any{"state": state, "outcome": outcome})
		if err := auditRecord(r.Context(), tx, user.Email, "alert.state", alertID,
			string(oldValue), string(newValue), comment); err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, alert := range changedAlerts {
		a.publishAlert(fmt.Sprint(alert["receiving_institution"]), alert)
	}
	writeJSON(w, 200, result)
}
