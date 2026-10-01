# Protected Signal Policy Reference

This document describes Kifaru's implemented deterministic policy. It is not an
AI-agent prompt and it does not authorize an automated fraud determination.

## Goal

Given a structured risk report from an institution:

1. Validate the report and required evidence.
2. Normalize supported risk codes.
3. Match protected artefacts against eligible active reports.
4. Calculate a reproducible policy score.
5. Route a review alert only when the score and independent-institution match
   requirements are both met.
6. Preserve the receiving institution's authority to decide and record the
   outcome.

## Outcomes

- `CORROBORATED_SIGNAL`: policy threshold met with at least one eligible report
  from another institution.
- `AWAITING_CORROBORATION`: active evidence remains available, but the report
  has no qualified match or has not crossed the alert threshold.
- `BELOW_ALERT_THRESHOLD`: retained without a receiver alert.
- `QUARANTINED`, `RETRACTED`, `EXPIRED`, `CLEARED`: inactive lifecycle states
  that cannot support another report.

## Required safeguards

- Reject unknown risk codes and bank rules.
- Enforce the evidence fields declared for each risk code.
- Accept identifiers only as `sha256:` plus a 64-character lowercase digest.
- Treat reporting-system scores and flags as inputs, not truth.
- Load the institution threshold from server-side configuration.
- Scope destination account and MSISDN matches to the receiving institution.
- Count only active, unexpired reports from another institution.
- Never treat a protected-artefact link as proof of human identity.
- Never automatically add a corroborated destination to a known-risk list.
- Store explanations, reasons, policy version, and superseded validations.
- Recalculate linked intelligence after dispute, retraction, release, or expiry.

## Receiver decisions

Kifaru issues a `review` alert. The receiving institution may acknowledge it and
record one of these outcomes:

- `held`
- `released`
- `recovered`

Kifaru records the response; it does not execute it.

## Example response

```json
{
  "status": "corroborated",
  "confidence": 68,
  "validated_by": "Kifaru policy engine",
  "reporting_bank": "NCBA",
  "receiving_bank": "Equity Bank",
  "transaction_id": "GUIDED-EXAMPLE-A",
  "customer_ref": "*A5f6",
  "amount": "KES 125,000",
  "risk_codes": [
    {
      "code": "ATO-460",
      "label": "New device followed by beneficiary change"
    },
    {
      "code": "MUL-440",
      "label": "New account, high fan-in from unrelated senders"
    }
  ],
  "key_signals": [
    "Protected destination matched an active report from another institution",
    "New device and new beneficiary evidence were supplied",
    "Receiving institution retains the decision"
  ],
  "recommended_action": "Review the corroborated signal and record the institution outcome.",
  "short_explanation": "The shared policy threshold was met after an eligible independent institution match."
}
```
