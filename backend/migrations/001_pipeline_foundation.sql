WITH duplicate_reports AS (
  SELECT report_id
  FROM (
    SELECT report_id,
      ROW_NUMBER() OVER (
        PARTITION BY reporting_institution, transaction_ref
        ORDER BY submitted_at, report_id
      ) AS position
    FROM reports
  ) ranked
  WHERE position > 1
)
DELETE FROM alerts WHERE report_id IN (SELECT report_id FROM duplicate_reports);

WITH duplicate_reports AS (
  SELECT report_id
  FROM (
    SELECT report_id,
      ROW_NUMBER() OVER (
        PARTITION BY reporting_institution, transaction_ref
        ORDER BY submitted_at, report_id
      ) AS position
    FROM reports
  ) ranked
  WHERE position > 1
)
DELETE FROM validations WHERE report_id IN (SELECT report_id FROM duplicate_reports);

WITH duplicate_reports AS (
  SELECT report_id
  FROM (
    SELECT report_id,
      ROW_NUMBER() OVER (
        PARTITION BY reporting_institution, transaction_ref
        ORDER BY submitted_at, report_id
      ) AS position
    FROM reports
  ) ranked
  WHERE position > 1
)
DELETE FROM reports WHERE report_id IN (SELECT report_id FROM duplicate_reports);

CREATE UNIQUE INDEX IF NOT EXISTS ux_reports_institution_reference
  ON reports(reporting_institution, transaction_ref);

CREATE TABLE IF NOT EXISTS artefacts (
  report_id TEXT NOT NULL REFERENCES reports(report_id) ON DELETE CASCADE,
  institution_code TEXT NOT NULL REFERENCES institutions(code),
  artefact_type TEXT NOT NULL,
  artefact_hash TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  PRIMARY KEY (report_id, artefact_type, artefact_hash)
);
CREATE INDEX IF NOT EXISTS ix_artefacts_hash_time
  ON artefacts(artefact_hash, observed_at DESC);
CREATE INDEX IF NOT EXISTS ix_artefacts_institution
  ON artefacts(institution_code);

INSERT INTO artefacts(report_id,institution_code,artefact_type,artefact_hash,observed_at)
SELECT report_id,reporting_institution,'destination_account',destination_account_hash,submitted_at
FROM reports WHERE destination_account_hash <> ''
ON CONFLICT DO NOTHING;

INSERT INTO artefacts(report_id,institution_code,artefact_type,artefact_hash,observed_at)
SELECT report_id,reporting_institution,'destination_msisdn',destination_msisdn_hash,submitted_at
FROM reports WHERE destination_msisdn_hash <> ''
ON CONFLICT DO NOTHING;

ALTER TABLE validations ADD COLUMN IF NOT EXISTS configuration_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE validations ADD COLUMN IF NOT EXISTS supersedes_validation_id TEXT;
ALTER TABLE validations ADD COLUMN IF NOT EXISTS is_current INTEGER NOT NULL DEFAULT 1;

WITH ranked AS (
  SELECT validation_id,
    ROW_NUMBER() OVER (
      PARTITION BY report_id
      ORDER BY validated_at DESC, validation_id DESC
    ) AS position
  FROM validations
)
UPDATE validations
SET is_current = CASE WHEN ranked.position = 1 THEN 1 ELSE 0 END
FROM ranked
WHERE validations.validation_id = ranked.validation_id;

CREATE UNIQUE INDEX IF NOT EXISTS ux_validations_current_report
  ON validations(report_id) WHERE is_current = 1;

ALTER TABLE alerts ADD COLUMN IF NOT EXISTS alert_type TEXT NOT NULL DEFAULT 'hold';

CREATE TABLE IF NOT EXISTS alert_actions (
  id BIGSERIAL PRIMARY KEY,
  alert_id TEXT NOT NULL REFERENCES alerts(alert_id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  comment TEXT,
  actor TEXT NOT NULL,
  at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_alert_actions_alert ON alert_actions(alert_id, id);

CREATE TABLE IF NOT EXISTS notifications (
  id BIGSERIAL PRIMARY KEY,
  institution_code TEXT NOT NULL REFERENCES institutions(code),
  event_type TEXT NOT NULL,
  record_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  read_at TEXT,
  payload TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS ix_notifications_institution
  ON notifications(institution_code, id DESC);

ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS old_value TEXT;
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS new_value TEXT;
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS reason TEXT;

CREATE OR REPLACE FUNCTION prevent_audit_mutation()
RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit records are append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only
BEFORE UPDATE OR DELETE ON audit_log
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
