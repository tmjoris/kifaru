CREATE TABLE IF NOT EXISTS institutions (
  code TEXT PRIMARY KEY, name TEXT, type TEXT,
  threshold REAL DEFAULT 0.5, active INTEGER DEFAULT 1
);

CREATE TABLE IF NOT EXISTS reports (
  report_id TEXT PRIMARY KEY,
  submitted_at TEXT, submission_channel TEXT,
  reporting_institution TEXT, reporting_system TEXT,
  transaction_ref TEXT, transaction_timestamp TEXT,
  subject_account_hash TEXT, subject_customer_hash TEXT,
  destination_account_hash TEXT, destination_msisdn_hash TEXT,
  destination_institution TEXT,
  amount REAL, currency TEXT, channel TEXT,
  bank_risk_score REAL, bank_threshold REAL,
  risk_codes TEXT, evidence TEXT, narrative TEXT
);
CREATE INDEX IF NOT EXISTS ix_rep_inst ON reports(reporting_institution);
CREATE INDEX IF NOT EXISTS ix_rep_dest ON reports(destination_account_hash);
CREATE INDEX IF NOT EXISTS ix_rep_msisdn ON reports(destination_msisdn_hash);

CREATE TABLE IF NOT EXISTS validations (
  validation_id TEXT PRIMARY KEY,
  report_id TEXT, validated_at TEXT, agent_version TEXT,
  validation_score REAL, status TEXT,
  reason_codes TEXT, corroborating_institutions TEXT, corroboration_count INTEGER,
  explanation TEXT, latency_ms INTEGER, alert_id TEXT,
  FOREIGN KEY(report_id) REFERENCES reports(report_id)
);
CREATE INDEX IF NOT EXISTS ix_val_status ON validations(status);

CREATE TABLE IF NOT EXISTS alerts (
  alert_id TEXT PRIMARY KEY,
  validation_id TEXT, report_id TEXT, issued_at TEXT,
  receiving_institution TEXT, reporting_institution TEXT,
  destination_account_hash TEXT, destination_msisdn_hash TEXT,
  amount REAL, currency TEXT, risk_codes TEXT,
  validation_score REAL, validated_by TEXT, explanation TEXT, state TEXT
);
CREATE INDEX IF NOT EXISTS ix_alert_recv ON alerts(receiving_institution);

CREATE TABLE IF NOT EXISTS knowledge_base (
  artefact_hash TEXT, list_name TEXT, label TEXT, added_by TEXT, added_at TEXT,
  PRIMARY KEY (artefact_hash, list_name)
);

CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS audit_log (
  id BIGSERIAL PRIMARY KEY,
  at TEXT, actor TEXT, action TEXT, target TEXT, detail TEXT
);
