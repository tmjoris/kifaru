-- Read-path indexes for the dashboards.
--
-- /v1/history sorts by submitted_at and filters on the reporting or the
-- destination institution. Without these indexes the OR of two single-column
-- indexes forced a sort of every matching row, and the staff view sorted the
-- whole reports table on disk on every request (about 1.9 s at 375,000 reports).
-- With them, each side is an index range scan that stops at the limit.
--
-- The two single-column institution indexes become redundant (the composites
-- start with the same column), so they are dropped to make every insert cheaper.
CREATE INDEX IF NOT EXISTS ix_reports_submitted
    ON reports (submitted_at DESC);
CREATE INDEX IF NOT EXISTS ix_reports_reporter_submitted
    ON reports (reporting_institution, submitted_at DESC);
CREATE INDEX IF NOT EXISTS ix_reports_destination_submitted
    ON reports (destination_institution, submitted_at DESC);
DROP INDEX IF EXISTS ix_rep_inst;
DROP INDEX IF EXISTS ix_rep_dest_inst;
