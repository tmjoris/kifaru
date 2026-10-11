ALTER TABLE alerts
  ADD COLUMN IF NOT EXISTS alert_tier TEXT NOT NULL DEFAULT 'corroborated';

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname='alerts_alert_tier_check'
  ) THEN
    ALTER TABLE alerts ADD CONSTRAINT alerts_alert_tier_check
      CHECK (alert_tier IN ('corroborated','single_source_review','single_source_notice'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS ix_alerts_reporting_outcome
  ON alerts(reporting_institution, updated_at DESC);
