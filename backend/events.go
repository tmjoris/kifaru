package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) streamAlerts(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	channel := make(chan []byte, 16)
	a.streamsMu.Lock()
	if a.streams[institution] == nil {
		a.streams[institution] = map[chan []byte]struct{}{}
	}
	a.streams[institution][channel] = struct{}{}
	a.streamsMu.Unlock()
	defer func() {
		a.streamsMu.Lock()
		delete(a.streams[institution], channel)
		if len(a.streams[institution]) == 0 {
			delete(a.streams, institution)
		}
		a.streamsMu.Unlock()
	}()
	_, _ = fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-channel:
			_, _ = w.Write(payload)
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := a.authenticatedUser(r); err != nil {
				return
			}
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (a *App) publishAlert(institution string, alert map[string]any) {
	a.publishStreamEvent([]string{institution, "*"}, "alert", fmt.Sprint(alert["alert_id"]), alert)
}

func (a *App) publishStreamEvent(keys []string, eventName, eventID string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := []byte(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", eventID, eventName, encoded))
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	for _, key := range keys {
		for channel := range a.streams[key] {
			select {
			case channel <- event:
			default:
			}
		}
	}
}

func (a *App) runDemoProducer(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if _, _, err := a.produceDemoEvent(ctx, false); err != nil {
			log.Printf("demo stream producer: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) produceDemoEvent(ctx context.Context, force bool) (map[string]any, bool, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var enabled bool
	var cadence, emitted int
	var offset int64
	var lastEmitted time.Time
	err = tx.QueryRow(ctx, `SELECT enabled,cadence_seconds,next_offset,emitted_since_reset,
		COALESCE(last_emitted_at,TO_TIMESTAMP(0))
		FROM demo_stream_state WHERE singleton=TRUE FOR UPDATE`).
		Scan(&enabled, &cadence, &offset, &emitted, &lastEmitted)
	if err != nil {
		return nil, false, err
	}
	if !enabled && emitted == demoStreamRetention && offset == int64(demoStreamRetention+1) {
		enabled = true
		if _, err := tx.Exec(ctx, `UPDATE demo_stream_state
			SET enabled=TRUE,updated_at=NOW() WHERE singleton=TRUE`); err != nil {
			return nil, false, err
		}
	}
	if !force && (!enabled || time.Since(lastEmitted) < time.Duration(cadence)*time.Second) {
		return nil, false, nil
	}
	if _, err := pruneDemoStreamRetention(ctx, tx, demoStreamRetention-1); err != nil {
		return nil, false, err
	}

	report, payload, metadata := demoReport(offset)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO demo_events(
		event_offset,topic,partition_key,event_type,source,payload,status,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,'pending',$7)`,
		offset, metadata["topic"], metadata["partition_key"], metadata["event_type"],
		metadata["source"], string(encoded), now); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE demo_stream_state
		SET next_offset=$1,emitted_since_reset=emitted_since_reset+1,
			last_emitted_at=$2,updated_at=$2
		WHERE singleton=TRUE`, offset+1, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}

	event := map[string]any{
		"event_offset": offset, "topic": metadata["topic"], "event_type": metadata["event_type"],
		"source": metadata["source"], "partition_key": metadata["partition_key"],
		"status": "pending", "created_at": now.Format(time.RFC3339),
		"reporting_institution":   report.ReportingInstitution,
		"destination_institution": report.DestinationInstitution,
		"alert_name":              payload["AlertName"],
	}
	result, apiErr := a.process(ctx, report, "soc_connector")
	if apiErr != nil {
		event["status"] = "failed"
		event["error"] = apiErr.message
		_, err = a.db.Exec(ctx, `UPDATE demo_events
			SET status='failed',error=$1,processed_at=NOW() WHERE event_offset=$2`,
			apiErr.message, offset)
		if err != nil {
			return nil, true, err
		}
	} else {
		reportID := fmt.Sprint(result["report_id"])
		outcome := ""
		if validation, ok := result["validation"].(Validation); ok {
			outcome = validation.Status
		} else if validation, ok := result["validation"].(map[string]any); ok {
			outcome = fmt.Sprint(validation["status"])
		}
		event["status"] = "processed"
		event["report_id"] = reportID
		event["outcome"] = outcome
		if _, err := a.db.Exec(ctx, `UPDATE demo_events
			SET status='processed',report_id=$1,outcome=$2,processed_at=NOW()
			WHERE event_offset=$3`, reportID, outcome, offset); err != nil {
			return nil, true, err
		}
	}
	a.publishStreamEvent([]string{"*"}, "demo-event", strconv.FormatInt(offset, 10), event)
	return event, true, nil
}

func pruneDemoStreamRetention(ctx context.Context, tx pgx.Tx, keep int) (int64, error) {
	expiredReports := `SELECT report_id FROM demo_events
		WHERE status <> 'pending'
		ORDER BY event_offset DESC OFFSET $1`
	queries := []string{
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM notifications WHERE record_id IN (
				SELECT alert_id FROM alerts WHERE report_id IN (
					SELECT report_id FROM expired WHERE report_id IS NOT NULL))`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM alert_actions WHERE alert_id IN (
				SELECT alert_id FROM alerts WHERE report_id IN (
					SELECT report_id FROM expired WHERE report_id IS NOT NULL))`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM alerts WHERE report_id IN (
				SELECT report_id FROM expired WHERE report_id IS NOT NULL)`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM knowledge_base kb USING reports r
			WHERE r.report_id IN (
				SELECT report_id FROM expired WHERE report_id IS NOT NULL)
				AND kb.label LIKE '%' || r.report_id`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM validations WHERE report_id IN (
				SELECT report_id FROM expired WHERE report_id IS NOT NULL)`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM artefacts WHERE report_id IN (
				SELECT report_id FROM expired WHERE report_id IS NOT NULL)`,
		`WITH expired AS (` + expiredReports + `)
			DELETE FROM reports WHERE report_id IN (
				SELECT report_id FROM expired WHERE report_id IS NOT NULL)`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, keep); err != nil {
			return 0, err
		}
	}
	result, err := tx.Exec(ctx, `WITH expired AS (
		SELECT event_offset FROM demo_events
		WHERE status <> 'pending'
		ORDER BY event_offset DESC OFFSET $1
	)
	DELETE FROM demo_events d USING expired
	WHERE d.event_offset=expired.event_offset`, keep)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func demoReport(offset int64) (ReportIn, map[string]any, map[string]string) {
	type scenario struct {
		eventType      string
		alertName      string
		severity       string
		riskCodes      []string
		amount         float64
		channel        string
		tactics        []string
		techniques     []string
		evidence       map[string]any
		phaseEvidence  [2]map[string]any
		switchesDevice bool
	}
	scenarios := []scenario{
		{
			eventType: "sim_swap_account_takeover", alertName: "SIM change followed by new-device transfer",
			severity:  "High",
			riskCodes: []string{"ATO-460"}, amount: 48500, channel: "mobile_banking",
			tactics: []string{"CredentialAccess", "InitialAccess"}, techniques: []string{"T1078"},
			evidence: map[string]any{"sim_swap_age_days": 0, "is_new_device": true, "is_new_beneficiary": true},
		},
		{
			eventType: "mule_rapid_flow_through", alertName: "New account receiving and rapidly forwarding funds",
			severity:  "High",
			riskCodes: []string{"MUL-440"}, amount: 73500, channel: "instant_payment",
			tactics: []string{"Collection", "Exfiltration"}, techniques: []string{"T1020"},
			evidence: map[string]any{"flow_through_ratio": 0.96, "dwell_minutes": 4, "account_age_days": 3, "distinct_senders_7d": 9},
		},
		{
			eventType: "beneficiary_change_high_value", alertName: "New beneficiary followed by high-value transfer",
			severity:  "Medium",
			riskCodes: []string{"BEN-450", "ATO-461"}, amount: 126000, channel: "internet_banking",
			tactics: []string{"CredentialAccess"}, techniques: []string{"T1078"},
			evidence: map[string]any{"is_new_device": true, "is_new_beneficiary": true, "beneficiary_age_minutes": 12},
		},
		{
			eventType: "credential_change_advisory", alertName: "Credential reset from an unfamiliar device on a foreign network",
			severity:  "Medium",
			riskCodes: []string{"ATO-460", "ATO-461"}, amount: 0, channel: "mobile_app",
			tactics: []string{"Persistence", "CredentialAccess"}, techniques: []string{"T1098"},
			evidence: map[string]any{"is_new_device": true, "credential_changed": true, "transaction_attempted": false, "ip_country_changed": true},
		},
		{
			eventType: "legitimate_payment_anomaly", alertName: "Unusual beneficiary payment requiring corroboration",
			severity:  "Low",
			riskCodes: []string{"BEN-450"}, amount: 9400, channel: "mobile_banking",
			tactics: []string{"Discovery"}, techniques: []string{"T1087"},
			evidence: map[string]any{"account_age_days": 1450, "distinct_senders_7d": 1, "known_customer_pattern": true},
		},
		{
			eventType: "device_network_switch", alertName: "New device and new network before a transfer to a fresh beneficiary",
			severity:  "High",
			riskCodes: []string{"ATO-460", "VEL-430"}, amount: 61500, channel: "mobile_banking",
			tactics: []string{"DefenseEvasion", "InitialAccess"}, techniques: []string{"T1078", "T1090"},
			evidence:       map[string]any{"is_new_device": true, "is_new_beneficiary": true, "beneficiary_age_minutes": 6},
			phaseEvidence:  [2]map[string]any{{"ip_country_changed": true}, {"vpn_proxy_tor": true}},
			switchesDevice: true,
		},
	}

	campaign := (offset - 1) / 2
	phase := (offset - 1) % 2
	selected := scenarios[campaign%int64(len(scenarios))]
	reporters, destination := demoInstitutions(campaign)
	reporting := reporters[phase]
	destinationHash := demoHash(fmt.Sprintf("%s:%d", selected.eventType, campaign))
	subjectHash := demoHash(fmt.Sprintf("subject:%d", offset))
	deviceSeed := fmt.Sprintf("device:%d", campaign)
	if selected.switchesDevice {
		deviceSeed = fmt.Sprintf("device:%d:%d", campaign, phase)
	}
	deviceProfile := "dp:" + strings.TrimPrefix(demoHash(deviceSeed), "sha256:")[:20]
	evidence := map[string]any{
		"synthetic_stream": true,
		"dataset_basis":    "Microsoft Sentinel public schemas and PaySim-informed transaction patterns",
		"device_profile":   deviceProfile,
	}
	for key, value := range selected.evidence {
		evidence[key] = value
	}
	for key, value := range selected.phaseEvidence[phase] {
		evidence[key] = value
	}
	systemAlertID := fmt.Sprintf("sentinel-demo-%06d", offset)
	payload := map[string]any{
		"TimeGenerated":        utcNow(),
		"SystemAlertId":        systemAlertID,
		"AlertName":            selected.alertName,
		"AlertSeverity":        selected.severity,
		"ProviderName":         "Microsoft Sentinel",
		"ProductName":          "Kifaru synthetic SOC connector",
		"CompromisedEntity":    subjectHash,
		"Description":          "Synthetic banking SOC event generated for the Kifaru live demonstration.",
		"Tactics":              selected.tactics,
		"Techniques":           selected.techniques,
		"Entities":             []map[string]any{{"Type": "account", "Name": subjectHash}, {"Type": "host", "HostName": deviceProfile}},
		"ExtendedProperties":   map[string]any{"synthetic": true, "transaction_amount": selected.amount, "currency": "KES"},
		"TransactionReference": fmt.Sprintf("DEMO-SENTINEL-%06d", offset),
	}
	evidence["sentinel_security_alert"] = payload

	report := ReportIn{
		ReportingInstitution: reporting, ReportingSystem: "Microsoft Sentinel",
		TransactionRef:       fmt.Sprintf("DEMO-SENTINEL-%06d", offset),
		TransactionTimestamp: utcNow(), SubjectAccountHash: subjectHash,
		SubjectCustomerHash: subjectHash, DestinationAccountHash: destinationHash,
		DestinationInstitution: destination, Amount: selected.amount, Currency: "KES",
		Channel: selected.channel, BankRiskScore: 0.86, BankThreshold: 0.50,
		RiskCodes: selected.riskCodes, Evidence: evidence,
		Narrative: "Synthetic Microsoft Sentinel event: " + selected.alertName,
	}
	metadata := map[string]string{
		"topic": "sentinel.security-alert", "partition_key": destinationHash,
		"event_type": selected.eventType, "source": "microsoft-sentinel-demo",
	}
	return report, payload, metadata
}

// demoInstitutions rotates every bank and mobile money provider through
// reporting, corroborating and receiving roles across successive campaigns.
func demoInstitutions(campaign int64) ([2]string, string) {
	n := int64(len(kenyanBanks))
	pick := func(shift int64) string {
		return kenyanBanks[((campaign*7+shift)%n+n)%n].Code
	}
	return [2]string{pick(0), pick(n / 3)}, pick(2 * n / 3)
}

func demoHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (a *App) demoStreamSnapshot(ctx context.Context) (map[string]any, error) {
	stateRows, err := rowsFrom(ctx, a.db, `SELECT enabled,cadence_seconds,next_offset,
		emitted_since_reset,last_emitted_at,updated_at,
		(SELECT COUNT(*) FROM demo_events) AS retained_events
		FROM demo_stream_state WHERE singleton=TRUE`)
	if err != nil {
		return nil, err
	}
	if len(stateRows) == 0 {
		return nil, errors.New("demo stream state is unavailable")
	}
	events, err := rowsFrom(ctx, a.db, `SELECT event_offset,topic,partition_key,event_type,
		source,status,report_id,outcome,error,created_at,processed_at,
		payload->>'AlertName' AS alert_name,
		payload->>'AlertSeverity' AS alert_severity
		FROM demo_events ORDER BY event_offset DESC LIMIT 30`)
	if err != nil {
		return nil, err
	}
	state := stateRows[0]
	state["max_events"] = demoStreamRetention
	state["events"] = events
	state["topic"] = "sentinel.security-alert"
	state["dataset_basis"] = "Microsoft Sentinel public schemas and PaySim-informed synthetic transactions"
	return state, nil
}

func (a *App) demoStreamStatus(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.demoStreamSnapshot(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, snapshot)
}

func (a *App) setDemoStreamState(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled        *bool `json:"enabled"`
		CadenceSeconds int   `json:"cadence_seconds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if request.Enabled == nil && request.CadenceSeconds == 0 {
		writeError(w, 422, "enabled or cadence_seconds is required")
		return
	}
	if request.CadenceSeconds != 0 && (request.CadenceSeconds < 5 || request.CadenceSeconds > 3600) {
		writeError(w, 422, "cadence_seconds must be between 5 and 3600")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var oldEnabled bool
	var oldCadence int
	if err := tx.QueryRow(r.Context(), `SELECT enabled,cadence_seconds
		FROM demo_stream_state WHERE singleton=TRUE FOR UPDATE`).Scan(&oldEnabled, &oldCadence); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	enabled := oldEnabled
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	cadence := oldCadence
	if request.CadenceSeconds != 0 {
		cadence = request.CadenceSeconds
	}
	if _, err := tx.Exec(r.Context(), `UPDATE demo_stream_state
		SET enabled=$1,cadence_seconds=$2,updated_at=NOW() WHERE singleton=TRUE`,
		enabled, cadence); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	oldValue, _ := json.Marshal(map[string]any{"enabled": oldEnabled, "cadence_seconds": oldCadence})
	newValue, _ := json.Marshal(map[string]any{"enabled": enabled, "cadence_seconds": cadence})
	if err := auditRecord(r.Context(), tx, authUserFromContext(r.Context()).Email, "demo_stream.state",
		"sentinel.security-alert", string(oldValue), string(newValue), "demo stream control"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	snapshot, err := a.demoStreamSnapshot(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, snapshot)
}

func (a *App) emitDemoStreamEvent(w http.ResponseWriter, r *http.Request) {
	event, emitted, err := a.produceDemoEvent(r.Context(), true)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !emitted {
		writeError(w, 409, "demo event was not emitted")
		return
	}
	writeJSON(w, 200, event)
}

func (a *App) resetDemoStream(w http.ResponseWriter, r *http.Request) {
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var wasEnabled bool
	if err := tx.QueryRow(r.Context(), `SELECT enabled FROM demo_stream_state
		WHERE singleton=TRUE FOR UPDATE`).Scan(&wasEnabled); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE demo_stream_state
		SET enabled=FALSE,updated_at=NOW() WHERE singleton=TRUE`); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var pendingCount int
	if err := tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM demo_events
		WHERE status='pending'`).Scan(&pendingCount); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if pendingCount > 0 {
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeError(w, 409, "stream paused; wait for the pending event to finish, then reset again")
		return
	}
	var eventCount int
	if err := tx.QueryRow(r.Context(), "SELECT COUNT(*) FROM demo_events").Scan(&eventCount); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	queries := []string{
		`DELETE FROM notifications WHERE record_id IN (
			SELECT alert_id FROM alerts WHERE report_id IN (
				SELECT report_id FROM demo_events WHERE report_id IS NOT NULL))`,
		`DELETE FROM alert_actions WHERE alert_id IN (
			SELECT alert_id FROM alerts WHERE report_id IN (
				SELECT report_id FROM demo_events WHERE report_id IS NOT NULL))`,
		`DELETE FROM alerts WHERE report_id IN (
			SELECT report_id FROM demo_events WHERE report_id IS NOT NULL)`,
		`DELETE FROM knowledge_base kb USING reports r,demo_events d
			WHERE d.report_id=r.report_id AND kb.label LIKE '%' || r.report_id`,
		`DELETE FROM validations WHERE report_id IN (
			SELECT report_id FROM demo_events WHERE report_id IS NOT NULL)`,
		`DELETE FROM artefacts WHERE report_id IN (
			SELECT report_id FROM demo_events WHERE report_id IS NOT NULL)`,
		`DELETE FROM reports WHERE report_id IN (
			SELECT report_id FROM demo_events WHERE report_id IS NOT NULL)`,
		`DELETE FROM demo_events`,
		`UPDATE demo_stream_state SET enabled=FALSE,next_offset=1,emitted_since_reset=0,
			last_emitted_at=NULL,updated_at=NOW() WHERE singleton=TRUE`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(r.Context(), query); err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	if err := auditRecord(r.Context(), tx, authUserFromContext(r.Context()).Email, "demo_stream.reset",
		"sentinel.security-alert", strconv.Itoa(eventCount), "0", "remove synthetic demo data"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	payload := map[string]any{"status": "reset", "removed_events": eventCount}
	a.publishStreamEvent([]string{"*"}, "demo-event", "reset", payload)
	snapshot, err := a.demoStreamSnapshot(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, snapshot)
}
