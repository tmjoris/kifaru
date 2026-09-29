#!/usr/bin/env python3
"""
Derives the objects the NEW architecture consumes, from the existing event data.

The event dataset sits one layer BELOW this architecture. Banks don't send
Kifaru raw events - they send REPORTS from their own fraud system (AG Screener).
This script builds that layer:

  kifaru_risk_codes.json          the central fraud standard (taxonomy)
  kifaru_reports.csv              what banks submit to the Ingestion API
  kifaru_validation_history.csv   seed history, incl. NOT_FRAUD outcomes
  kifaru_knowledge_base.json      what the validation agent corroborates against

Stdlib only. Deterministic.
"""
import csv, json, random, hashlib, uuid
from datetime import datetime, timedelta
from collections import defaultdict

random.seed(7)

# ---------------------------------------------------------------- taxonomy
# This is the most defensible artifact in the new architecture: Kenya has no
# common fraud-typology standard. Every bank names these differently today.

RISK_CODES = {
    "IP-401": {"family": "Identity & Profile", "name": "Shared device across unrelated customers",
               "severity_base": 0.30, "evidence_fields": ["device_profile_hash", "device_id"]},
    "IP-402": {"family": "Identity & Profile", "name": "Recent SIM swap preceding transaction",
               "severity_base": 0.35, "evidence_fields": ["sim_swap_age_days"]},
    "IP-403": {"family": "Identity & Profile", "name": "Emulator or rooted device",
               "severity_base": 0.25, "evidence_fields": ["is_emulator", "is_rooted"]},
    "IP-404": {"family": "Identity & Profile", "name": "Access from foreign or anonymising ASN",
               "severity_base": 0.22, "evidence_fields": ["ip_asn", "ip_org"]},
    "VEL-429": {"family": "Velocity", "name": "Transaction burst within one hour",
                "severity_base": 0.28, "evidence_fields": ["velocity_1h"]},
    "VEL-430": {"family": "Velocity", "name": "Beneficiary added and used within minutes",
                "severity_base": 0.30, "evidence_fields": ["is_new_beneficiary"]},
    "VEL-431": {"family": "Velocity", "name": "Structuring below reporting threshold",
                "severity_base": 0.26, "evidence_fields": ["amount"]},
    "MUL-440": {"family": "Mule Network", "name": "New account, high fan-in from unrelated senders",
                "severity_base": 0.38, "evidence_fields": ["account_age_days", "distinct_senders_7d"]},
    "MUL-441": {"family": "Mule Network", "name": "Flow-through ratio near unity",
                "severity_base": 0.34, "evidence_fields": ["flow_through_ratio"]},
    "MUL-442": {"family": "Mule Network", "name": "Dwell time under ten minutes",
                "severity_base": 0.30, "evidence_fields": ["dwell_minutes"]},
    "MUL-443": {"family": "Mule Network", "name": "Cash-out MSISDN reused across institutions",
                "severity_base": 0.36, "evidence_fields": ["beneficiary_msisdn"]},
    "BEN-450": {"family": "Beneficiary", "name": "New beneficiary, high-value first transfer",
                "severity_base": 0.24, "evidence_fields": ["is_new_beneficiary", "amount"]},
    "BEN-451": {"family": "Beneficiary", "name": "Multiple unrelated senders converging on one destination",
                "severity_base": 0.40, "evidence_fields": ["beneficiary_id"]},
    "ATO-460": {"family": "Account Takeover", "name": "New device followed by beneficiary change",
                "severity_base": 0.42, "evidence_fields": ["is_new_device", "is_new_beneficiary"]},
    "ATO-461": {"family": "Account Takeover", "name": "Off-hours session with credential change",
                "severity_base": 0.20, "evidence_fields": ["timestamp"]},
}

# bank SOC rule id -> central standard code. This mapping IS the normalisation step.
RULE_MAP = {
    "DEV_UNRECOGNISED_DEVICE": "IP-401", "SIM_RECENT_SWAP": "IP-402",
    "DEV_EMULATOR_DETECTED": "IP-403", "DEV_ROOTED_DEVICE": "IP-403",
    "NET_FOREIGN_ASN": "IP-404", "VEL_BURST_1H": "VEL-429",
    "BEN_NEW_BENEFICIARY": "BEN-450", "AMT_HIGH_VALUE": "BEN-450",
    "AMT_ELEVATED": "VEL-431", "ACCT_NEW_INBOUND": "MUL-440",
    "TIME_OFF_HOURS": "ATO-461", "ANOM_STATISTICAL_OUTLIER": None,
}

SUBMISSION_CHANNELS = {
    "bank_a":  "soc_connector",   # Sentinel connector
    "bank_b":  "rest",            # direct API
    "psp_c":   "webhook",
    "sacco_d": "batch",           # nightly file - deliberately the laggard
}
BANK_THRESHOLDS = {"bank_a": 0.45, "bank_b": 0.50, "psp_c": 0.40, "sacco_d": 0.60}

INSTITUTION_NAMES = {"bank_a": "Tier-1 Bank A", "bank_b": "Tier-2 Bank B",
                     "psp_c": "Mobile Money PSP C", "sacco_d": "SACCO D"}

SALT = "kifaru-demo-salt-2026"


def h(v):
    return "sha256:" + hashlib.sha256((SALT + str(v)).encode()).hexdigest()[:20] if v else ""


events = list(csv.DictReader(open("kifaru_events.csv")))
alerts = list(csv.DictReader(open("kifaru_soc_alerts.csv")))
by_id = {e["event_id"]: e for e in events}

# destination -> institution directory (how Kifaru knows who to alert)
dest_directory = {}
for e in events:
    if e["beneficiary_id"] and e["beneficiary_institution"]:
        dest_directory[e["beneficiary_id"]] = e["beneficiary_institution"]
    if e["account_id"]:
        dest_directory[e["account_id"]] = e["institution_id"]


def codes_for(ev):
    out = []
    for r in filter(None, ev["soc_rules_fired"].split("|")):
        c = RULE_MAP.get(r)
        if c and c not in out:
            out.append(c)
    # behavioural codes the bank's own engine would add from account context
    try:
        if ev["flow_through_ratio"] and float(ev["flow_through_ratio"]) > 0.90:
            out.append("MUL-441")
        if ev["dwell_minutes"] and int(ev["dwell_minutes"]) < 10:
            out.append("MUL-442")
        if ev["distinct_senders_7d"] and int(ev["distinct_senders_7d"]) >= 5 \
                and int(ev["account_age_days"]) < 14:
            if "MUL-440" not in out: out.append("MUL-440")
    except ValueError:
        pass
    if ev["is_new_device"] == "true" and ev["is_new_beneficiary"] == "true":
        out.append("ATO-460")
    return out


# ---------------------------------------------------------------- reports
reports = []
candidates = [a for a in alerts if a["severity"] in ("Medium", "High")] + \
             [{"related_event_id": e["event_id"], "risk_score": e["soc_risk_score"],
               "severity": e["soc_severity"], "analyst_disposition": "escalated"}
              for e in events if e["is_fraud"] == "true" and e["soc_severity"] == "Low"]

seen = set()
for a in candidates:
    eid = a["related_event_id"]
    if eid in seen or eid not in by_id:
        continue
    seen.add(eid)
    ev = by_id[eid]
    inst = ev["institution_id"]
    cds = codes_for(ev)
    if not cds:
        continue

    dest = ev["beneficiary_id"] or ev["account_id"]
    dest_inst = ev["beneficiary_institution"] or dest_directory.get(dest, "")
    if dest_inst == inst:
        dest_inst = ""          # internal, no receiving bank to alert

    t = datetime.strptime(ev["timestamp"], "%Y-%m-%dT%H:%M:%SZ")
    lag = random.randint(2, 40) if SUBMISSION_CHANNELS[inst] != "batch" else random.randint(300, 1400)

    reports.append({
        "report_id": "rpt-" + uuid.uuid4().hex[:12],
        "submitted_at": (t + timedelta(minutes=lag)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "submission_channel": SUBMISSION_CHANNELS[inst],
        "reporting_institution": inst,
        "reporting_institution_name": INSTITUTION_NAMES[inst],
        "reporting_system": "AG Screener",
        "transaction_ref": ev["event_id"],
        "transaction_timestamp": ev["timestamp"],
        "subject_account_hash": h(ev["account_id"]),
        "subject_customer_hash": h(ev["customer_id"]),
        "destination_account_hash": h(dest),
        "destination_msisdn_hash": h(ev["beneficiary_msisdn"]),
        "destination_institution": dest_inst,
        "amount": ev["amount"],
        "currency": "KES",
        "channel": ev["channel"],
        "bank_risk_score": float(ev["soc_risk_score"]),
        "bank_threshold": BANK_THRESHOLDS[inst],
        "risk_codes": "|".join(cds),
        "evidence_device_profile": ev["device_profile_hash"],
        "evidence_account_age_days": ev["account_age_days"],
        "evidence_distinct_senders_7d": ev["distinct_senders_7d"],
        "evidence_flow_through_ratio": ev["flow_through_ratio"],
        "evidence_dwell_minutes": ev["dwell_minutes"],
        "evidence_sim_swap_age_days": ev["sim_swap_age_days"],
        "narrative": f"{INSTITUTION_NAMES[inst]} AG Screener flagged {ev['event_type']} "
                     f"of KES {ev['amount']} at score {ev['soc_risk_score']}.",
        "_gt_is_fraud": ev["is_fraud"],
        "_gt_campaign": ev["campaign_id"],
    })

reports.sort(key=lambda r: r["submitted_at"])


# ------------------------------------------------- validation agent (reference)
def validate(report, prior_reports, kb):
    """Deterministic. This is the reference implementation of the agent."""
    score, reasons = 0.0, []

    for c in filter(None, report["risk_codes"].split("|")):
        score += RISK_CODES[c]["severity_base"] * 0.5
        reasons.append(f"CODE:{c}")

    # corroboration: has another institution reported an overlapping artefact?
    corro = set()
    for p in prior_reports:
        if p["reporting_institution"] == report["reporting_institution"]:
            continue
        if report["destination_account_hash"] and \
           p["destination_account_hash"] == report["destination_account_hash"]:
            corro.add(p["reporting_institution"]); reasons.append("CORRO:destination")
        elif report["destination_msisdn_hash"] and \
                p["destination_msisdn_hash"] == report["destination_msisdn_hash"]:
            corro.add(p["reporting_institution"]); reasons.append("CORRO:msisdn")
        elif report["evidence_device_profile"] and \
                p["evidence_device_profile"] == report["evidence_device_profile"]:
            corro.add(p["reporting_institution"]); reasons.append("CORRO:device_profile")
    score += min(len(corro), 3) * 0.18

    # knowledge base: known-bad artefact, or known-good suppression
    dh = report["destination_account_hash"]
    if dh in kb["known_bad"]:
        score += 0.30; reasons.append("KB:known_bad")
    if dh in kb["known_good"]:
        score -= 0.55; reasons.append("KB:suppressed_legitimate_fanin")

    if report["bank_risk_score"] >= report["bank_threshold"]:
        score += 0.10; reasons.append("BANK:above_threshold")

    score = round(max(0.0, min(score, 0.99)), 2)
    if score >= 0.60:   status = "VALIDATED_FRAUD"
    elif score >= 0.35: status = "INSUFFICIENT_EVIDENCE"
    else:               status = "NOT_FRAUD"
    return score, status, sorted(set(reasons)), sorted(corro)


# knowledge base seed
gt = json.load(open("kifaru_ground_truth.json"))
kb = {
    "known_good": [h(d["id"]) for d in gt["legit_fanin_decoys"]],
    "known_good_labels": {h(d["id"]): d["label"] for d in gt["legit_fanin_decoys"]},
    "known_bad": [],
    "version": "kb-2026.09.1",
}

history, prior = [], []
for r in reports:
    s, status, reasons, corro = validate(r, prior[-400:], kb)
    alert_id = "alt-" + uuid.uuid4().hex[:10] if status == "VALIDATED_FRAUD" and r["destination_institution"] else ""
    history.append({
        "validation_id": "val-" + uuid.uuid4().hex[:10],
        "report_id": r["report_id"],
        "validated_at": r["submitted_at"],
        "agent_version": "kifaru-agent-0.3.0",
        "validation_score": s,
        "status": status,
        "reason_codes": "|".join(reasons),
        "corroborating_institutions": "|".join(corro),
        "corroboration_count": len(corro),
        "receiving_institution_notified": r["destination_institution"] if alert_id else "",
        "alert_id": alert_id,
        "latency_ms": random.randint(180, 2400),
        "_gt_is_fraud": r["_gt_is_fraud"],
    })
    prior.append(r)
    if status == "VALIDATED_FRAUD" and r["destination_account_hash"]:
        kb["known_bad"].append(r["destination_account_hash"])

kb["known_bad"] = sorted(set(kb["known_bad"]))[:200]

# ---------------------------------------------------------------- write
with open("kifaru_risk_codes.json", "w") as f:
    json.dump({"standard": "KIFARU Central Fraud Standard", "version": "1.0",
               "codes": RISK_CODES, "bank_rule_mapping": RULE_MAP}, f, indent=2)
with open("kifaru_reports.csv", "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=list(reports[0].keys())); w.writeheader(); w.writerows(reports)
with open("kifaru_validation_history.csv", "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=list(history[0].keys())); w.writeheader(); w.writerows(history)
with open("kifaru_knowledge_base.json", "w") as f:
    json.dump(kb, f, indent=2)

# ---------------------------------------------------------------- report card
tp = sum(1 for h_ in history if h_["status"] == "VALIDATED_FRAUD" and h_["_gt_is_fraud"] == "true")
fp = sum(1 for h_ in history if h_["status"] == "VALIDATED_FRAUD" and h_["_gt_is_fraud"] != "true")
tn = sum(1 for h_ in history if h_["status"] == "NOT_FRAUD" and h_["_gt_is_fraud"] != "true")
fn = sum(1 for h_ in history if h_["status"] == "NOT_FRAUD" and h_["_gt_is_fraud"] == "true")
ins = sum(1 for h_ in history if h_["status"] == "INSUFFICIENT_EVIDENCE")
alerts_issued = sum(1 for h_ in history if h_["alert_id"])

print(f"  risk codes in standard      {len(RISK_CODES)}")
print(f"  reports submitted           {len(reports)}")
print(f"  by channel                  " + ", ".join(
    f"{c}:{sum(1 for r in reports if r['submission_channel']==c)}"
    for c in ("soc_connector", "rest", "webhook", "batch")))
print(f"  VALIDATED_FRAUD             {tp+fp}   (true {tp} / false {fp})")
print(f"  NOT_FRAUD                   {tn+fn}   (correct {tn} / missed {fn})")
print(f"  INSUFFICIENT_EVIDENCE       {ins}")
print(f"  receiving-bank alerts sent  {alerts_issued}")
if tp + fp:
    print(f"  precision on validated      {tp/(tp+fp)*100:.0f}%")
print(f"  noise suppressed at centre  {tn} reports never reached a receiving bank")
