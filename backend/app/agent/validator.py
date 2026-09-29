"""
The KIFARU validation agent.

  Input      one normalised report
  Reasoning  deterministic weighted sum over risk codes, cross-institution
             corroboration, and knowledge-base membership
  Tools      prior-report index, knowledge base, the standard
  Output     score, status, reason codes, corroborating institutions
  Human      receiving-bank analyst holds for step-up verification

The agent NEVER blocks a transaction and NEVER decides using a language model.
The LLM writes the explanation only, after the decision is already made.
"""
import json, time, uuid, os
from ..schemas import Status, utcnow
from ..normalize import CODES
from .. import store

AGENT_VERSION = "kifaru-agent-0.3.0"

W_CODE = 0.50          # per risk-code severity multiplier
W_CORRO = 0.18         # per corroborating institution
CORRO_CAP = 3
W_KB_BAD = 0.30
W_KB_GOOD = -0.55      # suppression must be able to overcome several codes
W_ABOVE_THRESHOLD = 0.10


def score_report(conn, report: dict, codes: list[str]) -> dict:
    t0 = time.perf_counter()
    cfg = store.get_config(conn)
    kb = store.kb_lists(conn)

    score = 0.0
    reasons: list[str] = []

    # --- 1. the codes themselves
    for c in codes:
        score += CODES[c]["severity_base"] * W_CODE
        reasons.append(f"CODE:{c}")

    # --- 2. cross-institution corroboration
    dev = (report.get("evidence") or {}).get("device_profile", "")
    rows = store.corroboration_candidates(
        conn,
        report.get("destination_account_hash", ""),
        report.get("destination_msisdn_hash", ""),
        dev,
        report["reporting_institution"],
    )
    corro: set[str] = set()
    for r in rows:
        inst = r["reporting_institution"]
        if report.get("destination_account_hash") and \
           r["destination_account_hash"] == report["destination_account_hash"]:
            corro.add(inst); reasons.append("CORRO:destination")
        elif report.get("destination_msisdn_hash") and \
                r["destination_msisdn_hash"] == report["destination_msisdn_hash"]:
            corro.add(inst); reasons.append("CORRO:msisdn")
        elif dev and dev in (r["evidence"] or ""):
            corro.add(inst); reasons.append("CORRO:device_profile")
    score += min(len(corro), CORRO_CAP) * W_CORRO

    # --- 3. knowledge base
    dh = report.get("destination_account_hash", "")
    mh = report.get("destination_msisdn_hash", "")
    if dh and dh in kb["known_bad"]:
        score += W_KB_BAD; reasons.append("KB:known_bad")
    if mh and mh in kb["known_bad"]:
        score += W_KB_BAD; reasons.append("KB:known_bad_msisdn")
    if (dh and dh in kb["known_good"]) or (mh and mh in kb["known_good"]):
        score += W_KB_GOOD; reasons.append("KB:suppressed_legitimate")

    # --- 4. did it clear the reporting bank's own bar
    if report.get("bank_risk_score", 0) >= report.get("bank_threshold", 1):
        score += W_ABOVE_THRESHOLD; reasons.append("BANK:above_threshold")

    score = round(max(0.0, min(score, 0.99)), 2)
    if score >= cfg["validated_threshold"]:
        status = Status.VALIDATED_FRAUD
    elif score >= cfg["insufficient_threshold"]:
        status = Status.INSUFFICIENT_EVIDENCE
    else:
        status = Status.NOT_FRAUD

    return {
        "validation_id": "val-" + uuid.uuid4().hex[:10],
        "report_id": report["report_id"],
        "validated_at": utcnow(),
        "agent_version": AGENT_VERSION,
        "validation_score": score,
        "status": status.value,
        "reason_codes": sorted(set(reasons)),
        "corroborating_institutions": sorted(corro),
        "corroboration_count": len(corro),
        "latency_ms": int((time.perf_counter() - t0) * 1000) or 1,
    }


# --------------------------------------------------------------- explanation
_CACHE: dict[str, str] = {}


def explain(report: dict, validation: dict, codes: list[str]) -> str:
    """One Azure OpenAI call. Cached. Deterministic fallback if unavailable.

    The model receives reason codes and hashed artefacts only. No customer data
    can reach it, because no customer data exists in the report."""
    key = validation["status"] + "|" + "|".join(sorted(codes)) + f"|{validation['corroboration_count']}"
    if key in _CACHE:
        return _CACHE[key]

    text = _fallback(report, validation, codes)
    endpoint = os.getenv("AZURE_OPENAI_ENDPOINT")
    api_key = os.getenv("AZURE_OPENAI_KEY")
    deployment = os.getenv("AZURE_OPENAI_DEPLOYMENT", "gpt-4o-mini")
    if endpoint and api_key:
        try:
            import httpx
            prompt = (
                "You are explaining a fraud validation decision to a bank analyst who has no "
                "graph or data-science training. Three sentences maximum. State what was found, "
                "what corroborated it, and the single recommended action. Never invent detail.\n\n"
                f"Status: {validation['status']}\nScore: {validation['validation_score']}\n"
                f"Risk codes: {', '.join(f'{c} ({CODES[c]['name']})' for c in codes)}\n"
                f"Corroborating institutions: {validation['corroboration_count']}\n"
                f"Reason codes: {', '.join(validation['reason_codes'])}\n"
                f"Amount: {report.get('amount')} {report.get('currency')}"
            )
            r = httpx.post(
                f"{endpoint}/openai/deployments/{deployment}/chat/completions?api-version=2024-02-01",
                headers={"api-key": api_key},
                json={"messages": [{"role": "user", "content": prompt}],
                      "max_tokens": 160, "temperature": 0.2},
                timeout=6.0)
            if r.status_code == 200:
                text = r.json()["choices"][0]["message"]["content"].strip()
        except Exception:
            pass          # fallback already set — the demo never depends on the network

    _CACHE[key] = text
    return text


def _fallback(report, validation, codes) -> str:
    names = [CODES[c]["name"].lower() for c in codes[:3]]
    n = validation["corroboration_count"]
    amt = f"{report.get('currency','KES')} {report.get('amount',0):,.0f}"
    if validation["status"] == Status.VALIDATED_FRAUD.value:
        c = (f"{n} other institution{'s' if n != 1 else ''} independently reported the same artefact"
             if n else "the reporting institution's own evidence")
        return (f"A transfer of {amt} was flagged for {', '.join(names)}. "
                f"This was corroborated by {c}. "
                f"Hold the transaction for step-up verification before release.")
    if validation["status"] == Status.INSUFFICIENT_EVIDENCE.value:
        return (f"A transfer of {amt} showed {', '.join(names)}, but no other institution has "
                f"reported a matching artefact. Monitoring only — the report will be re-evaluated "
                f"automatically if a second institution corroborates it.")
    return (f"A transfer of {amt} matched {', '.join(names)}, but the destination is a known "
            f"legitimate high-volume account or the pattern did not meet the sector standard. "
            f"Marked not fraud. No alert issued; the record is retained in history.")
