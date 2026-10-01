package main

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	agentVersion         = "kifaru-agent-0.5.1"
	defaultValidated     = 0.60
	defaultInsufficient  = 0.35
	weightCode           = 0.50
	weightCorroboration  = 0.18
	corroborationCap     = 3
	weightKnownBad       = 0.30
	weightKnownGood      = -0.55
	weightAboveThreshold = 0.10
	demoStreamRetention  = 500
)

// Institution is one licensed Kenyan bank or mobile money provider from
// data/kenyan_banks.json. All Kifaru records associated with it are synthetic.
type Institution struct {
	Code      string  `json:"code"`
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	LegalName string  `json:"legal_name"`
	Ref       string  `json:"ref"`
	Type      string  `json:"type"`
	Threshold float64 `json:"threshold"`
}

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
