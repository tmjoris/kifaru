CREATE TABLE IF NOT EXISTS attack_scenario_state (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  run_id TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'without' CHECK (mode IN ('without','with')),
  step INTEGER NOT NULL DEFAULT 0 CHECK (step BETWEEN 0 AND 5),
  status TEXT NOT NULL DEFAULT 'ready'
    CHECK (status IN ('ready','running','completed','failed')),
  first_report_id TEXT NOT NULL DEFAULT '',
  second_report_id TEXT NOT NULL DEFAULT '',
  first_alert_id TEXT NOT NULL DEFAULT '',
  second_alert_id TEXT NOT NULL DEFAULT '',
  -- The first reporter's track record inside the scenario. Scenario reports
  -- read standing from here, so they never borrow or change real standing.
  standing_upheld INTEGER NOT NULL DEFAULT 9,
  standing_adverse INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO attack_scenario_state(singleton)
VALUES (TRUE)
ON CONFLICT (singleton) DO NOTHING;
