package main

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	policyVersion        = "kifaru-policy-0.6.0"
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

// Reporter standing lets a single institution's report alert a receiver
// without corroboration. It is earned only from receiver decisions on that
// institution's past alerts: held or recovered outcomes uphold a report, while
// releases and disputes count against it.
const (
	standingWindowDays     = 90
	standingMinResolved    = 5
	standingMaxAdverseRate = 0.20
)

const (
	statusCorroborated = "CORROBORATED_SIGNAL"
	statusAwaiting     = "AWAITING_CORROBORATION"
	statusBelow        = "BELOW_ALERT_THRESHOLD"
	// Single-source statuses alert the receiver on one institution's evidence.
	// A review needs a victim-confirmed transfer from a reporter in good
	// standing; a notice needs a known-bad destination or a reporter in good
	// standing.
	statusSingleSourceReview = "SINGLE_SOURCE_REVIEW"
	statusSingleSourceNotice = "SINGLE_SOURCE_NOTICE"
	statusQuarantined        = "QUARANTINED"
	statusRetracted          = "RETRACTED"
	statusExpired            = "EXPIRED"
	statusCleared            = "CLEARED"
)

const (
	lifecycleActive      = "active"
	lifecycleQuarantined = "quarantined"
	lifecycleRetracted   = "retracted"
	lifecycleExpired     = "expired"
	lifecycleCleared     = "cleared"
)

const (
	alertTierCorroborated = "corroborated"
	alertTierReview       = "single_source_review"
	alertTierNotice       = "single_source_notice"
)

// qualifyingStatuses are the current-validation statuses of active reports
// that can corroborate another institution's report.
var qualifyingStatuses = []string{
	statusAwaiting, statusCorroborated, statusSingleSourceReview, statusSingleSourceNotice,
}

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
	DemoName  string  `json:"demo_name"`
	DemoEmail string  `json:"demo_email"`
}

type RiskCode struct {
	Family         string   `json:"family"`
	Name           string   `json:"name"`
	SeverityBase   float64  `json:"severity_base"`
	EvidenceFields []string `json:"evidence_fields"`
	EvidenceMode   string   `json:"evidence_mode"`
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
	// ReporterStanding is the standing snapshot used for this decision. It is
	// not stored separately; the explanation records it.
	ReporterStanding ReporterStanding `json:"reporter_standing"`
}

type ReporterStanding struct {
	Upheld  int  `json:"upheld"`
	Adverse int  `json:"adverse"`
	Good    bool `json:"good"`
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
