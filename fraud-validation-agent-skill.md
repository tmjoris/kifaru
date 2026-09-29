---
name: fraud-validation-agent
description: Validate bank fraud/SOC transaction reports using structured transaction, identity, device, network, velocity, beneficiary, and history signals. Use when an AI agent receives suspected fraud data from a bank fraud detection system, SOC connector, API feed, webhook, SIEM, transaction monitor, or case system and must decide whether Kifaru should mark it as validated fraud, not fraud, or needs review.
---

# Fraud Validation Agent

Use this skill to evaluate suspected fraud reports submitted by a bank fraud detection system and produce a clear fraud-validation decision.

## Goal

Given a suspected fraud transaction and its surrounding SOC/fraud signals, determine whether the transaction is:

- `validated_fraud`
- `not_fraud`
- `needs_review`

Return a confidence score, risk codes, reasoned evidence, and recommended action.

## Operating model

1. A bank fraud system flags a transaction as suspicious.
2. Kifaru receives the report through a SOC connector, API, webhook, stream, or batch feed.
3. The validation agent normalizes the fields into a common fraud schema.
4. The agent evaluates the report against centralized fraud patterns.
5. The agent returns a status:
   - `validated_fraud`: alert the receiving bank.
   - `not_fraud`: store in history, do not alert.
   - `needs_review`: queue for human analyst review.

## Important rules

- Do not expose raw PII in the output. Use masked references such as `*8622` or `*A5f6`.
- Do not decide fraud from one weak signal alone unless the signal is severe.
- Prefer evidence combinations: amount + new beneficiary + device/IP anomaly is stronger than any one field alone.
- Treat bank-provided fraud flags as inputs, not final truth.
- If data is missing, say what is missing and reduce confidence.
- Always separate:
  - **Reporting bank**: the bank that submitted the fraud report.
  - **Receiving bank**: the bank/account receiving the funds or alert.
  - **Validated by**: `AG Screener`, `Kifaru agent`, or another validator name.

## Input fields to expect

The agent may receive any of these fields. Use what is available.

| Category | Example fields | Use |
|---|---|---|
| Transaction | transaction_id, amount, currency, timestamp, merchant, channel, payment_rail | Detect unusual amount, timing, merchant, or payment type |
| Banks | reporting_bank, receiving_bank, source_account_hash, destination_account_hash | Understand who reported and who should receive alerts |
| Customer/account | masked_customer_ref, account_age, customer_segment, normal_spend_range | Compare against baseline behavior |
| Device | device_id_hash, device_fingerprint, os, app_version, rooted_or_jailbroken, emulator_signal | Detect new/reused/high-risk devices |
| Network | ip_hash, ip_country, asn, vpn_proxy_tor, ip_reputation, geo_velocity | Detect changing IPs, impossible travel, and anonymized networks |
| Authentication | login_time, failed_logins, mfa_result, password_reset, sim_swap_signal, session_age | Detect account takeover risk |
| Velocity | transfers_5m, transfers_1h, beneficiary_changes_24h, failed_login_count | Detect bursts and high-volume behavior |
| Beneficiary | beneficiary_age, beneficiary_bank, beneficiary_risk_score, prior_fraud_link | Detect mule/payee risk |
| History | prior_alerts, prior_chargebacks, prior_disputes, analyst_outcomes | Improve decision with past behavior |
| Enrichment | sanctions_hit, mule_network_distance, blacklist_hit, watchlist_match | Add external or internal intelligence |

## Risk codes

Assign one or more risk codes. Use these codes in summary output and explain them in expanded detail.

| Code | Meaning | Typical signals |
|---|---|---|
| `IP-401` | Changing IP/location | New country, VPN/proxy/Tor, impossible travel, bad IP reputation |
| `VEL-429` | Velocity/volume spike | Many transfers, repeated cash-outs, many failed logins, rapid beneficiary changes |
| `DEV-403` | Device/auth anomaly | New device, SIM swap, password reset, emulator, rooted device, failed MFA |
| `BEN-409` | Beneficiary mismatch | New payee, changed beneficiary details, risky receiving bank/account |
| `AML-451` | Mule/AML pattern | Structuring, mule proximity, rapid cash-out, linked-account network |
| `DOC-422` | Invoice/vendor anomaly | BEC signal, invoice mismatch, vendor-bank change, spoofed domain |
| `CRY-418` | Crypto/forex/remittance risk | Crypto gateway, forex dealer, offshore transfer, high-risk remittance corridor |
| `GEN-400` | General fraud signal | Strong suspicion that does not fit another code |

## Decision guidance

### Mark `validated_fraud`

Use when several strong signals align, such as:

- New beneficiary + high amount + new device.
- SIM swap/password reset + rapid transfer.
- Changing IP/location + failed MFA + high-value payment.
- Beneficiary linked to prior fraud.
- Mule account pattern + rapid cash-out.
- Invoice/vendor change with BEC indicators.

### Mark `not_fraud`

Use when the transaction is consistent with known legitimate behavior, such as:

- Known beneficiary and expected amount.
- Trusted device and normal IP/location.
- Valid travel notice or expected cross-border behavior.
- Historical pattern supports the transaction.
- Only one weak signal is present and no corroborating evidence exists.

### Mark `needs_review`

Use when signals are mixed or important fields are missing, such as:

- High amount but trusted device and known beneficiary.
- New beneficiary but normal customer behavior.
- Risky IP but successful MFA and normal amount.
- Conflicting signals between bank system and Kifaru evaluation.

## Scoring guidance

Use a 0–100 confidence score.

| Score range | Interpretation |
|---|---|
| 0–39 | Likely not fraud |
| 40–69 | Unclear, needs review |
| 70–84 | Suspicious, likely review/hold |
| 85–100 | High-confidence fraud |

Adjust confidence upward for multiple independent signals. Adjust downward for missing data, stale data, or explainable behavior.

## Output format

Return this JSON shape:

```json
{
  "status": "validated_fraud",
  "confidence": 92,
  "validated_by": "Kifaru agent",
  "reporting_bank": "NCBA",
  "receiving_bank": "KCB",
  "transaction_id": "NCBA-90831",
  "customer_ref": "*8622",
  "amount": "KES 626,600",
  "risk_codes": [
    {
      "code": "DEV-403",
      "label": "Device/auth anomaly",
      "evidence": "New device observed after password reset"
    },
    {
      "code": "BEN-409",
      "label": "Beneficiary mismatch",
      "evidence": "New beneficiary added minutes before transfer"
    }
  ],
  "key_signals": [
    "New device after password reset",
    "High-value transfer above normal baseline",
    "New beneficiary with elevated risk"
  ],
  "missing_fields": [],
  "recommended_action": "Send fraud alert to receiving bank and hold the transaction.",
  "human_review_required": false,
  "short_explanation": "Multiple independent account-takeover and beneficiary-risk signals support a high-confidence fraud decision."
}
```

## Output requirements

- Use `validated_by: "Kifaru agent"` when the AI agent makes the final validation.
- Use `validated_by: "AG Screener"` only when reporting the original bank-system source, not the final Kifaru decision.
- Keep `customer_ref` masked.
- Keep explanations concise and evidence-based.
- Include `missing_fields` even if empty.
- If status is `validated_fraud`, include the receiving-bank alert action.
- If status is `not_fraud`, say that no receiving-bank alert should be sent.
- If status is `needs_review`, state exactly what a human analyst must verify.

## Example input

```json
{
  "reporting_bank": "NCBA",
  "receiving_bank": "KCB",
  "transaction_id": "TX-90831",
  "customer_ref": "*A5f6",
  "amount": "KES 1,240,000",
  "currency": "KES",
  "beneficiary_age_minutes": 8,
  "device_status": "new_device",
  "password_reset_within_1h": true,
  "ip_country_changed": true,
  "transfers_5m": 6,
  "mfa_result": "passed",
  "prior_fraud_link": true,
  "bank_flag_source": "AG Screener"
}
```

## Example output

```json
{
  "status": "validated_fraud",
  "confidence": 94,
  "validated_by": "Kifaru agent",
  "reporting_bank": "NCBA",
  "receiving_bank": "KCB",
  "transaction_id": "TX-90831",
  "customer_ref": "*A5f6",
  "amount": "KES 1,240,000",
  "risk_codes": [
    {
      "code": "DEV-403",
      "label": "Device/auth anomaly",
      "evidence": "New device and recent password reset"
    },
    {
      "code": "IP-401",
      "label": "Changing IP/location",
      "evidence": "IP country changed before transaction"
    },
    {
      "code": "VEL-429",
      "label": "Velocity/volume spike",
      "evidence": "Six transfers in five minutes"
    }
  ],
  "key_signals": [
    "New beneficiary created minutes before payment",
    "Prior fraud link found",
    "Multiple independent account-takeover indicators"
  ],
  "missing_fields": [],
  "recommended_action": "Send fraud alert to KCB, hold settlement, and open a fraud case.",
  "human_review_required": false,
  "short_explanation": "The transaction has device, IP, velocity, and beneficiary-risk signals that strongly support fraud."
}
```
