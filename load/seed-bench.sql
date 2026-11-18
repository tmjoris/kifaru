-- Seeds a throwaway benchmark database with 375,000 synthetic reports and a session token
-- for every demo user (token = "bench-<user_id>"). Run it AFTER the API has started once, so
-- the schema, institutions and demo users exist. Only for disposable databases: it adds a lot
-- of data and mints sessions that never expire for 30 days.
\timing on
CREATE TEMP TABLE codes AS SELECT code, row_number() OVER (ORDER BY code) AS n FROM institutions WHERE code <> 'external';
SELECT count(*) AS banks FROM codes \gset
INSERT INTO reports(report_id,submitted_at,submission_channel,reporting_institution,reporting_system,transaction_ref,transaction_timestamp,
  subject_account_hash,subject_customer_hash,destination_account_hash,destination_institution,amount,currency,channel,bank_risk_score,bank_threshold,risk_codes,evidence,narrative)
SELECT 'rpt-'||lpad(to_hex(g),12,'0'),
  to_char((now() at time zone 'utc') - (g * interval '10 seconds'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
  'rest', r.code, 'Bench engine', 'BENCH-'||g, to_char((now() at time zone 'utc') - (g * interval '10 seconds'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
  'sha256:'||md5(g::text)||md5((g+1)::text), 'sha256:'||md5((g%5000)::text)||md5((g%7000)::text), 'sha256:'||md5((g%40000)::text)||md5((g%9000)::text),
  d.code, 5000 + (g*37)%900000, 'KES', 'instant_payment', 0.5 + (g%50)/100.0, 0.7,
  '["ATO-460","VEL-430"]', '{"is_new_device":true,"is_new_beneficiary":true,"account_age_days":3,"distinct_senders_7d":5}', 'Bench report '||g
FROM generate_series(1::bigint,500000::bigint) g
JOIN codes r ON r.n = 1 + ((g*7919) % :banks)
JOIN codes d ON d.n = 1 + ((g*104729) % :banks)
WHERE r.code <> d.code;
INSERT INTO validations(validation_id,report_id,validated_at,agent_version,validation_score,status,reason_codes,corroborating_institutions,corroboration_count,explanation,latency_ms,alert_id,configuration_version,is_current)
SELECT 'val-'||substr(report_id,5), report_id, submitted_at, 'bench', 0.4 + (random()*0.6),
  CASE WHEN m<70 THEN 'BELOW_ALERT_THRESHOLD' WHEN m<85 THEN 'AWAITING_CORROBORATION' WHEN m<91 THEN 'CORROBORATED_SIGNAL' WHEN m<96 THEN 'SINGLE_SOURCE_REVIEW' ELSE 'SINGLE_SOURCE_NOTICE' END,
  '["BANK_SCORE"]', '[]', 0, 'Bench validation', 20, CASE WHEN m>=85 THEN 'alt-'||substr(report_id,5) END, 1, 1
FROM (SELECT report_id, submitted_at, (abs(hashtext(report_id)) % 100) AS m FROM reports WHERE report_id LIKE 'rpt-%' AND reporting_system='Bench engine') x;
INSERT INTO alerts(alert_id,validation_id,report_id,issued_at,receiving_institution,reporting_institution,destination_account_hash,amount,currency,risk_codes,validation_score,validated_by,explanation,state,alert_type,outcome,outcome_note,alert_tier)
SELECT v.alert_id, v.validation_id, r.report_id, r.submitted_at, r.destination_institution, r.reporting_institution, r.destination_account_hash, r.amount, 'KES', r.risk_codes, v.validation_score, 'bench', 'Bench alert', 'sent', 'hold', '', '',
  CASE v.status WHEN 'CORROBORATED_SIGNAL' THEN 'corroborated' WHEN 'SINGLE_SOURCE_REVIEW' THEN 'single_source_review' ELSE 'single_source_notice' END
FROM validations v JOIN reports r ON r.report_id=v.report_id WHERE v.alert_id IS NOT NULL AND r.reporting_system='Bench engine';
-- one pre-minted session per demo user, token = bench-<user_id>
INSERT INTO auth_sessions(token_hash,csrf_token,user_id,expires_at)
SELECT encode(sha256(convert_to('bench-'||user_id::text,'UTF8')),'hex'), 'csrf-'||user_id::text, user_id, now() + interval '30 days' FROM auth_users
ON CONFLICT DO NOTHING;
ANALYZE;
SELECT (SELECT count(*) FROM reports) reports, (SELECT count(*) FROM validations) validations, (SELECT count(*) FROM alerts) alerts, (SELECT count(*) FROM auth_sessions) sessions;
