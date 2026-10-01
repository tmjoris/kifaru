ALTER TABLE reports
  ADD COLUMN IF NOT EXISTS lifecycle_state TEXT NOT NULL DEFAULT 'active',
  ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

UPDATE reports
SET expires_at = CASE
  WHEN submitted_at ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
    THEN submitted_at::TIMESTAMPTZ + INTERVAL '30 days'
  ELSE NOW() + INTERVAL '30 days'
END
WHERE expires_at IS NULL;

ALTER TABLE reports
  ALTER COLUMN expires_at SET DEFAULT (NOW() + INTERVAL '30 days'),
  ALTER COLUMN expires_at SET NOT NULL;

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname='reports_lifecycle_state_check'
  ) THEN
    ALTER TABLE reports ADD CONSTRAINT reports_lifecycle_state_check
      CHECK (lifecycle_state IN ('active','quarantined','retracted','expired','cleared'));
  END IF;
END $$;

ALTER TABLE artefacts
  ADD COLUMN IF NOT EXISTS match_scope TEXT NOT NULL DEFAULT '';

UPDATE artefacts a
SET match_scope = CASE
  WHEN a.artefact_type IN ('destination_account', 'destination_msisdn')
    THEN r.destination_institution
  ELSE ''
END
FROM reports r
WHERE r.report_id = a.report_id;

CREATE INDEX IF NOT EXISTS ix_artefacts_match_scope
  ON artefacts(artefact_type, artefact_hash, match_scope, observed_at DESC);

ALTER TABLE alerts
  ADD COLUMN IF NOT EXISTS outcome TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS outcome_note TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE alerts SET alert_type='review' WHERE alert_type='hold';

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname='alerts_outcome_check'
  ) THEN
    ALTER TABLE alerts ADD CONSTRAINT alerts_outcome_check
      CHECK (outcome IN ('','held','released','recovered'));
  END IF;
END $$;

UPDATE validations SET status='CORROBORATED_SIGNAL' WHERE status='VALIDATED_FRAUD';
UPDATE validations SET status='AWAITING_CORROBORATION' WHERE status='INSUFFICIENT_EVIDENCE';
UPDATE validations SET status='BELOW_ALERT_THRESHOLD' WHERE status='NOT_FRAUD';
UPDATE validations
SET agent_version=REPLACE(agent_version,'kifaru-agent-','kifaru-policy-')
WHERE agent_version LIKE 'kifaru-agent-%';

DELETE FROM knowledge_base
WHERE added_by='agent'
  AND (label LIKE 'validated via %' OR label LIKE 'validated via automatic revalidation %');

CREATE TABLE IF NOT EXISTS guided_demo_state (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  run_id TEXT NOT NULL DEFAULT '',
  step INTEGER NOT NULL DEFAULT 0 CHECK (step BETWEEN 0 AND 3),
  status TEXT NOT NULL DEFAULT 'ready',
  first_report_id TEXT NOT NULL DEFAULT '',
  second_report_id TEXT NOT NULL DEFAULT '',
  alert_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname='guided_demo_state_status_check'
  ) THEN
    ALTER TABLE guided_demo_state ADD CONSTRAINT guided_demo_state_status_check
      CHECK (status IN ('ready','running','completed','failed'));
  END IF;
END $$;

INSERT INTO guided_demo_state(singleton)
VALUES (TRUE)
ON CONFLICT (singleton) DO NOTHING;
