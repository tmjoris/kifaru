-- The demo runs on synthetic data, so its institutions carry invented names.
UPDATE institutions SET name = 'Tier-1 Bank A', type = 'bank' WHERE code = 'bank_a';
UPDATE institutions SET name = 'Tier-2 Bank B', type = 'bank' WHERE code = 'bank_b';
UPDATE institutions SET name = 'Mobile Money PSP C', type = 'psp' WHERE code = 'psp_c';
UPDATE institutions SET name = 'SACCO D', type = 'sacco' WHERE code = 'sacco_d';

DELETE FROM institutions i
WHERE i.code LIKE 'ke:%'
  AND NOT EXISTS (
    SELECT 1 FROM reports r
    WHERE r.reporting_institution = i.code OR r.destination_institution = i.code)
  AND NOT EXISTS (
    SELECT 1 FROM alerts a
    WHERE a.receiving_institution = i.code OR a.reporting_institution = i.code)
  AND NOT EXISTS (SELECT 1 FROM artefacts f WHERE f.institution_code = i.code)
  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.institution_code = i.code);

UPDATE institutions
SET name = 'Retired directory entry ' || code, active = 0
WHERE code LIKE 'ke:%';
