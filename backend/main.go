package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	agentVersion         = "kifaru-agent-0.3.0"
	defaultValidated     = 0.60
	defaultInsufficient  = 0.35
	weightCode           = 0.50
	weightCorroboration  = 0.18
	corroborationCap     = 3
	weightKnownBad       = 0.30
	weightKnownGood      = -0.55
	weightAboveThreshold = 0.10
)

//go:embed schema.sql
var schema string

//go:embed migrations/*.sql
var migrationFiles embed.FS

type RiskCode struct {
	Family       string  `json:"family"`
	Name         string  `json:"name"`
	SeverityBase float64 `json:"severity_base"`
}

type Standard struct {
	Version         string                 `json:"version"`
	Codes           map[string]RiskCode    `json:"codes"`
	BankRuleMapping map[string]string      `json:"bank_rule_mapping"`
	Extra           map[string]interface{} `json:"-"`
}

type ReportIn struct {
	ReportingInstitution   string         `json:"reporting_institution"`
	ReportingSystem        string         `json:"reporting_system"`
	TransactionRef         string         `json:"transaction_ref"`
	TransactionTimestamp   string         `json:"transaction_timestamp"`
	SubjectAccountHash     string         `json:"subject_account_hash"`
	SubjectCustomerHash    string         `json:"subject_customer_hash"`
	DestinationAccountHash string         `json:"destination_account_hash"`
	DestinationMSISDNHash  string         `json:"destination_msisdn_hash"`
	DestinationInstitution string         `json:"destination_institution"`
	Amount                 float64        `json:"amount"`
	Currency               string         `json:"currency"`
	Channel                string         `json:"channel"`
	BankRiskScore          float64        `json:"bank_risk_score"`
	BankThreshold          float64        `json:"bank_threshold"`
	BankRuleIDs            []string       `json:"bank_rule_ids"`
	RiskCodes              []string       `json:"risk_codes"`
	Evidence               map[string]any `json:"evidence"`
	Narrative              string         `json:"narrative"`
}

type Validation struct {
	ValidationID              string   `json:"validation_id"`
	ReportID                  string   `json:"report_id"`
	ValidatedAt               string   `json:"validated_at"`
	AgentVersion              string   `json:"agent_version"`
	ValidationScore           float64  `json:"validation_score"`
	Status                    string   `json:"status"`
	ReasonCodes               []string `json:"reason_codes"`
	CorroboratingInstitutions []string `json:"corroborating_institutions"`
	CorroborationCount        int      `json:"corroboration_count"`
	Explanation               string   `json:"explanation"`
	LatencyMS                 int      `json:"latency_ms"`
	AlertID                   *string  `json:"alert_id"`
}

type App struct {
	db          *pgxpool.Pool
	standardRaw map[string]any
	standard    Standard
	streamsMu   sync.Mutex
	streams     map[string]map[chan []byte]struct{}
}

type dbRunner interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required and must point to Neon PostgreSQL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}

	raw, standard, err := loadStandard()
	if err != nil {
		log.Fatal(err)
	}
	app := &App{db: db, standardRaw: raw, standard: standard, streams: map[string]map[chan []byte]struct{}{}}
	if err := app.initDB(ctx); err != nil {
		log.Fatal(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           app.withCORS(http.HandlerFunc(app.serveHTTP)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("KIFARU Go API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func loadStandard() (map[string]any, Standard, error) {
	path := filepath.Join("data", "kifaru_risk_codes.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, Standard{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw map[string]any
	var standard Standard
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, Standard{}, err
	}
	if err := json.Unmarshal(body, &standard); err != nil {
		return nil, Standard{}, err
	}
	return raw, standard, nil
}

func (a *App) initDB(ctx context.Context) error {
	for _, statement := range strings.Split(schema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := a.db.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if err := a.runMigrations(ctx); err != nil {
		return err
	}
	institutions := [][]any{
		{"external", "External financial network", "external", 0.50},
		{"bank_a", "NCBA Bank Kenya PLC", "bank", 0.45},
		{"bank_b", "KCB Bank Kenya Limited", "bank", 0.50},
		{"psp_c", "Equity Bank Kenya Limited", "bank", 0.40},
		{"sacco_d", "I&M Bank Limited", "bank", 0.60},
		{"ke:absa-bank-kenya", "Absa Bank Kenya PLC", "bank", 0.50},
		{"ke:access-bank-kenya", "Access Bank (Kenya) PLC", "bank", 0.50},
		{"ke:bank-of-africa-kenya", "Bank of Africa Kenya Limited", "bank", 0.50},
		{"ke:bank-of-baroda-kenya", "Bank of Baroda (Kenya) Limited", "bank", 0.50},
		{"ke:bank-of-india-kenya", "Bank of India (Kenya)", "bank", 0.50},
		{"ke:citibank-n-a-kenya", "Citibank N.A. Kenya", "bank", 0.50},
		{"ke:commercial-international-bank-kenya-cib", "Commercial International Bank Kenya Limited", "bank", 0.50},
		{"ke:consolidated-bank-of-kenya", "Consolidated Bank of Kenya Limited", "bank", 0.50},
		{"ke:co-operative-bank-of-kenya", "Co-operative Bank of Kenya Limited", "bank", 0.50},
		{"ke:credit-bank", "Credit Bank PLC", "bank", 0.50},
		{"ke:development-bank-of-kenya", "Development Bank of Kenya Limited", "bank", 0.50},
		{"ke:diamond-trust-bank-dtb", "Diamond Trust Bank Kenya Limited", "bank", 0.50},
		{"ke:dib-bank-kenya", "DIB Bank Kenya Limited", "bank", 0.50},
		{"ke:ecobank-kenya", "Ecobank Kenya Limited", "bank", 0.50},
		{"ke:family-bank", "Family Bank Limited", "bank", 0.50},
		{"ke:first-community-bank", "First Community Bank Limited", "bank", 0.50},
		{"ke:guaranty-trust-bank-kenya-gtbank", "Guaranty Trust Bank (Kenya) Limited", "bank", 0.50},
		{"ke:guardian-bank", "Guardian Bank Limited", "bank", 0.50},
		{"ke:gulf-african-bank", "Gulf African Bank Limited", "bank", 0.50},
		{"ke:habib-bank-ag-zurich", "Habib Bank AG Zurich", "bank", 0.50},
		{"ke:hfc-limited-housing-finance", "Housing Finance Company of Kenya Limited", "bank", 0.50},
		{"ke:kingdom-bank", "Kingdom Bank Limited", "bank", 0.50},
		{"ke:middle-east-bank-kenya", "Middle East Bank (Kenya) Limited", "bank", 0.50},
		{"ke:m-oriental-bank", "M Oriental Bank Limited", "bank", 0.50},
		{"ke:national-bank-of-kenya", "National Bank of Kenya Limited", "bank", 0.50},
		{"ke:paramount-bank", "Paramount Bank Limited", "bank", 0.50},
		{"ke:prime-bank", "Prime Bank Limited", "bank", 0.50},
		{"ke:sbm-bank-kenya", "SBM Bank Kenya Limited", "bank", 0.50},
		{"ke:sidian-bank", "Sidian Bank Limited", "bank", 0.50},
		{"ke:stanbic-bank-kenya", "Stanbic Bank Kenya Limited", "bank", 0.50},
		{"ke:standard-chartered-bank-kenya", "Standard Chartered Bank Kenya Limited", "bank", 0.50},
		{"ke:uba-kenya", "United Bank for Africa Kenya Limited", "bank", 0.50},
		{"ke:victoria-commercial-bank", "Victoria Commercial Bank PLC", "bank", 0.50},
		{"ke:abc-bank-african-banking-corporation", "African Banking Corporation Limited", "bank", 0.50},
	}
	for _, values := range institutions {
		if _, err := a.db.Exec(ctx, `
			INSERT INTO institutions(code,name,type,threshold) VALUES ($1,$2,$3,$4)
			ON CONFLICT (code) DO NOTHING`, values...); err != nil {
			return err
		}
	}
	constraints := []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reports_reporting_institution_fkey') THEN
				ALTER TABLE reports ADD CONSTRAINT reports_reporting_institution_fkey
				FOREIGN KEY (reporting_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reports_destination_institution_fkey') THEN
				ALTER TABLE reports ADD CONSTRAINT reports_destination_institution_fkey
				FOREIGN KEY (destination_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='alerts_receiving_institution_fkey') THEN
				ALTER TABLE alerts ADD CONSTRAINT alerts_receiving_institution_fkey
				FOREIGN KEY (receiving_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='alerts_reporting_institution_fkey') THEN
				ALTER TABLE alerts ADD CONSTRAINT alerts_reporting_institution_fkey
				FOREIGN KEY (reporting_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
	}
	for _, statement := range constraints {
		if _, err := a.db.Exec(ctx, statement); err != nil {
			return err
		}
	}
	defaults := map[string]any{
		"validated_threshold":    defaultValidated,
		"insufficient_threshold": defaultInsufficient,
		"enabled_sources":        []string{"soc_connector", "rest", "webhook", "batch"},
		"agent_version":          agentVersion,
		"configuration_version":  1,
	}
	for key, value := range defaults {
		encoded, _ := json.Marshal(value)
		if _, err := a.db.Exec(ctx, `
			INSERT INTO config(key,value) VALUES ($1,$2)
			ON CONFLICT (key) DO NOTHING`, key, string(encoded)); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runMigrations(ctx context.Context) error {
	if _, err := a.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := a.db.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", entry.Name(),
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := a.db.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx,
				"INSERT INTO schema_migrations(version,applied_at) VALUES ($1,$2)",
				entry.Name(), utcNow())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/":
		writeJSON(w, http.StatusOK, map[string]any{"service": "KIFARU", "health": "/health"})
	case r.Method == http.MethodGet && path == "/health":
		a.health(w, r)
	case r.Method == http.MethodGet && path == "/v1/standard":
		writeJSON(w, http.StatusOK, a.standardRaw)
	case r.Method == http.MethodGet && path == "/v1/institutions":
		a.queryRows(w, r, "SELECT * FROM institutions ORDER BY name")
	case r.Method == http.MethodGet && path == "/v1/stats":
		a.stats(w, r)
	case r.Method == http.MethodGet && path == "/v1/history":
		a.history(w, r)
	case r.Method == http.MethodGet && path == "/v1/reports":
		a.reports(w, r)
	case r.Method == http.MethodGet && path == "/v1/alerts":
		a.alerts(w, r)
	case r.Method == http.MethodGet && path == "/v1/stream":
		a.streamAlerts(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/validations/"):
		a.validationDetail(w, r, strings.TrimPrefix(path, "/v1/validations/"))
	case r.Method == http.MethodPost && path == "/v1/reports":
		a.submitReport(w, r, "rest")
	case r.Method == http.MethodPost && path == "/v1/reports/batch":
		a.submitBatch(w, r)
	case r.Method == http.MethodPost && (path == "/v1/reports/csv" || path == "/validate-csv"):
		a.submitCSV(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/hooks/"):
		channel := "webhook"
		source := strings.TrimPrefix(path, "/v1/hooks/")
		if source == "sentinel" || source == "soc" {
			channel = "soc_connector"
		}
		a.submitReport(w, r, channel)
	case path == "/v1/admin/config":
		a.config(w, r)
	case path == "/v1/admin/kb":
		a.knowledgeBase(w, r)
	case r.Method == http.MethodPost && path == "/v1/admin/revalidate":
		a.revalidate(w, r)
	case r.Method == http.MethodGet && path == "/v1/admin/audit":
		a.auditLog(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/alerts/") && strings.HasSuffix(path, "/state"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/alerts/"), "/state")
		a.setAlertState(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if err := a.db.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "agent": agentVersion, "standard": a.standard.Version, "db": "postgresql",
	})
}

func (a *App) submitReport(w http.ResponseWriter, r *http.Request, channel string) {
	var report ReportIn
	if err := decodeJSON(r, &report); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
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
		device, time.Now().UTC().Add(-30*24*time.Hour).Format("2006-01-02T15:04:05Z"))
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
	score += float64(min(len(corro), corroborationCap)) * weightCorroboration

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
		names = append(names, strings.ToLower(a.standard.Codes[code].Name))
	}
	amount := fmt.Sprintf("%s %.0f", report.Currency, report.Amount)
	switch validation.Status {
	case "VALIDATED_FRAUD":
		corroboration := "the reporting institution's own evidence"
		if validation.CorroborationCount > 0 {
			corroboration = fmt.Sprintf("%d other institutions independently reported the same artefact", validation.CorroborationCount)
		}
		return fmt.Sprintf("A transfer of %s was flagged for %s. This was corroborated by %s. Hold the transaction for step-up verification before release.",
			amount, strings.Join(names, ", "), corroboration)
	case "INSUFFICIENT_EVIDENCE":
		return fmt.Sprintf("A transfer of %s showed %s, but no other institution has reported a matching artefact. Monitoring only until another institution corroborates it.",
			amount, strings.Join(names, ", "))
	default:
		return fmt.Sprintf("A transfer of %s matched %s, but the pattern did not meet the sector standard. Marked not fraud; no alert issued.",
			amount, strings.Join(names, ", "))
	}
}

func (a *App) history(w http.ResponseWriter, r *http.Request) {
	institution := r.URL.Query().Get("institution")
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
		WHERE r.reporting_institution=$1 OR r.destination_institution=$1
		ORDER BY r.submitted_at DESC LIMIT $2`, institution, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"institution": institution, "history": rows})
}

func (a *App) reports(w http.ResponseWriter, r *http.Request) {
	institution := r.URL.Query().Get("institution")
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
	institution := r.URL.Query().Get("institution")
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

func (a *App) streamAlerts(w http.ResponseWriter, r *http.Request) {
	institution := strings.TrimSpace(r.URL.Query().Get("institution"))
	if institution == "" {
		writeError(w, 422, "institution is required")
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
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (a *App) publishAlert(institution string, alert map[string]any) {
	payload, err := json.Marshal(alert)
	if err != nil {
		return
	}
	event := []byte(fmt.Sprintf("id: %s\nevent: alert\ndata: %s\n\n", alert["alert_id"], payload))
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	for _, key := range []string{institution, "*"} {
		for channel := range a.streams[key] {
			select {
			case channel <- event:
			default:
			}
		}
	}
}

func (a *App) validationDetail(w http.ResponseWriter, r *http.Request, reportID string) {
	rows, err := a.rows(r.Context(), `SELECT * FROM validations
		WHERE report_id=$1 ORDER BY is_current DESC, validated_at DESC LIMIT 1`, reportID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(rows) == 0 {
		writeError(w, 404, "no validation for that report")
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
		changed := false
		for _, key := range []string{"validated_threshold", "insufficient_threshold", "enabled_sources"} {
			if value, ok := patch[key]; ok && value != nil {
				if err := a.setConfig(r.Context(), key, value, "admin"); err != nil {
					writeError(w, 500, err.Error())
					return
				}
				changed = true
			}
		}
		if thresholds, ok := patch["institution_thresholds"].(map[string]any); ok {
			for code, value := range thresholds {
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
					if err := auditRecord(r.Context(), a.db, "admin", "institution.threshold", code,
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
			if err := a.setConfig(r.Context(), "configuration_version", nextVersion, "admin"); err != nil {
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
		if entry.AddedBy == "" {
			entry.AddedBy = "admin"
		}
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
			err = a.audit(ctx, "admin", "kb.remove", hash, listName)
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
	if err := auditRecord(r.Context(), tx, "admin", "validation.revalidated",
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
		VALUES ($1,$2,$3,'analyst',$4)`, alertID, state, nullIfEmpty(comment), utcNow()); err != nil {
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
	if err := auditRecord(r.Context(), tx, "analyst", "alert.state", alertID,
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

func hashIfNeeded(value string) string {
	if value == "" || strings.HasPrefix(value, "sha256:") {
		return value
	}
	sum := sha256.Sum256([]byte("kifaru-upload-salt:" + value))
	return "sha256:" + hex.EncodeToString(sum[:])[:20]
}

func firstHash(value, fallback string) string {
	if value != "" {
		return hashIfNeeded(value)
	}
	return hashIfNeeded(fallback)
}

func institutionCode(value string) string {
	if code, ok := map[string]string{
		"NCBA": "bank_a", "KCB": "bank_b", "Equity": "psp_c", "I&M": "sacco_d",
	}[value]; ok {
		return code
	}
	return value
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func displayInstitution(value string) string {
	if display, ok := map[string]string{
		"bank_a": "NCBA", "bank_b": "KCB", "psp_c": "Equity", "sacco_d": "I&M",
	}[value]; ok {
		return display
	}
	if value == "" {
		return "External network"
	}
	return value
}
