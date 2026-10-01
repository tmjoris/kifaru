CREATE TABLE IF NOT EXISTS user_access_requests (
  request_id TEXT PRIMARY KEY,
  institution_code TEXT NOT NULL REFERENCES institutions(code),
  alias TEXT NOT NULL,
  email TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'approved', 'rejected')),
  requested_by TEXT NOT NULL REFERENCES auth_users(user_id),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  reviewed_by TEXT REFERENCES auth_users(user_id),
  reviewed_at TIMESTAMPTZ,
  review_note TEXT NOT NULL DEFAULT '',
  admitted_user_id TEXT REFERENCES auth_users(user_id),
  UNIQUE (institution_code, alias),
  UNIQUE (email)
);

CREATE INDEX IF NOT EXISTS ix_user_access_requests_institution
  ON user_access_requests(institution_code, requested_at DESC);

CREATE INDEX IF NOT EXISTS ix_user_access_requests_status
  ON user_access_requests(status, requested_at DESC);
