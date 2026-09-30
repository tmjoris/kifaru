CREATE TABLE IF NOT EXISTS demo_stream_state (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  cadence_seconds INTEGER NOT NULL DEFAULT 30 CHECK (cadence_seconds BETWEEN 5 AND 3600),
  next_offset BIGINT NOT NULL DEFAULT 1,
  emitted_since_reset INTEGER NOT NULL DEFAULT 0,
  last_emitted_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO demo_stream_state(singleton)
VALUES (TRUE)
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS demo_events (
  event_offset BIGINT PRIMARY KEY,
  topic TEXT NOT NULL,
  partition_key TEXT NOT NULL,
  event_type TEXT NOT NULL,
  source TEXT NOT NULL,
  payload JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  report_id TEXT,
  outcome TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  processed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS ix_demo_events_created
  ON demo_events(created_at DESC);

CREATE INDEX IF NOT EXISTS ix_demo_events_report
  ON demo_events(report_id)
  WHERE report_id IS NOT NULL;
