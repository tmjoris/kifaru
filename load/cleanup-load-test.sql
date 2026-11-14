-- Removes the rows written by load/k6-ingest.js. Run it against the database the
-- test wrote to, for example:
--
--   psql "$DATABASE_URL" -f load/cleanup-load-test.sql
--
-- It matches only reports whose reference starts with 'LOAD-' and whose narrative is
-- 'Load test report', which is exactly what k6-ingest.js writes. It shows what it
-- will delete first and runs in a transaction; change COMMIT to ROLLBACK to preview.
BEGIN;

CREATE TEMP TABLE load_reports ON COMMIT DROP AS
  SELECT report_id FROM reports
  WHERE transaction_ref LIKE 'LOAD-%' AND narrative = 'Load test report';

SELECT count(*) AS reports_to_delete FROM load_reports;
SELECT count(*) AS alerts_to_delete FROM alerts WHERE report_id IN (SELECT report_id FROM load_reports);

DELETE FROM notifications WHERE record_id IN (SELECT alert_id FROM alerts WHERE report_id IN (SELECT report_id FROM load_reports));
DELETE FROM alerts        WHERE report_id IN (SELECT report_id FROM load_reports);  -- alert_actions cascade
DELETE FROM audit_log     WHERE target IN (SELECT validation_id FROM validations WHERE report_id IN (SELECT report_id FROM load_reports));
DELETE FROM validations   WHERE report_id IN (SELECT report_id FROM load_reports);
DELETE FROM reports       WHERE report_id IN (SELECT report_id FROM load_reports);  -- artefacts cascade

SELECT count(*) AS load_reports_left FROM reports WHERE transaction_ref LIKE 'LOAD-%';
COMMIT;
