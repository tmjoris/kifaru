#!/usr/bin/env python3
"""
Replays kifaru_reports.csv into a running KIFARU platform, using the correct
submission channel per institution. Proves the full pipeline end to end.

    python scripts/replay.py --url http://127.0.0.1:8000
"""
import csv, json, argparse, sys, time
from pathlib import Path
import httpx

DATA = Path(__file__).parent.parent / "data"

EVIDENCE_COLS = {
    "evidence_device_profile": "device_profile",
    "evidence_account_age_days": "account_age_days",
    "evidence_distinct_senders_7d": "distinct_senders_7d",
    "evidence_flow_through_ratio": "flow_through_ratio",
    "evidence_dwell_minutes": "dwell_minutes",
    "evidence_sim_swap_age_days": "sim_swap_age_days",
}

ENDPOINT = {
    "rest": "/v1/reports",
    "webhook": "/v1/hooks/psp",
    "soc_connector": "/v1/hooks/sentinel",
    "batch": "/v1/reports/batch",
}


def to_payload(row):
    return {
        "reporting_institution": row["reporting_institution"],
        "reporting_system": row["reporting_system"],
        "transaction_ref": row["transaction_ref"],
        "transaction_timestamp": row["transaction_timestamp"],
        "subject_account_hash": row["subject_account_hash"],
        "subject_customer_hash": row["subject_customer_hash"],
        "destination_account_hash": row["destination_account_hash"],
        "destination_msisdn_hash": row["destination_msisdn_hash"],
        "destination_institution": row["destination_institution"],
        "amount": float(row["amount"] or 0),
        "currency": row["currency"],
        "channel": row["channel"],
        "bank_risk_score": float(row["bank_risk_score"] or 0),
        "bank_threshold": float(row["bank_threshold"] or 0.5),
        "risk_codes": [c for c in row["risk_codes"].split("|") if c],
        "bank_rule_ids": [],
        "evidence": {v: row[k] for k, v in EVIDENCE_COLS.items() if row.get(k)},
        "narrative": row["narrative"],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:8000")
    ap.add_argument("--delay", type=float, default=0.0)
    ap.add_argument("--limit", type=int, default=0)
    a = ap.parse_args()

    rows = list(csv.DictReader(open(DATA / "kifaru_reports.csv")))
    if a.limit:
        rows = rows[:a.limit]

    # ground truth held separately - never sent to the platform
    truth = {r["transaction_ref"]: r["_gt_is_fraud"] for r in rows}
    json.dump(truth, open(DATA / "_replay_truth.json", "w"))

    counts, batch_buf = {"ok": 0, "rejected": 0, "error": 0}, []
    per_status = {}
    client = httpx.Client(base_url=a.url, timeout=30)

    for row in rows:
        ch = row["submission_channel"]
        payload = to_payload(row)
        if ch == "batch":
            batch_buf.append(payload)
            continue
        try:
            r = client.post(ENDPOINT[ch], json=payload)
            if r.status_code == 200:
                st = r.json()["validation"]["status"]
                per_status[st] = per_status.get(st, 0) + 1
                counts["ok"] += 1
            elif r.status_code == 422:
                counts["rejected"] += 1
            else:
                counts["error"] += 1
                print(f"  ! {r.status_code} {r.text[:120]}", file=sys.stderr)
        except Exception as e:
            counts["error"] += 1
            print(f"  ! {e}", file=sys.stderr)
        if a.delay:
            time.sleep(a.delay)

    if batch_buf:
        r = client.post(ENDPOINT["batch"], json=batch_buf)
        if r.status_code == 200:
            b = r.json()
            counts["ok"] += b["accepted"]
            counts["rejected"] += b["rejected"]
            for res in b["results"]:
                st = res["validation"]["status"]
                per_status[st] = per_status.get(st, 0) + 1

    print(f"\n  submitted   {sum(counts.values())}")
    print(f"  accepted    {counts['ok']}")
    print(f"  rejected    {counts['rejected']}  (422 - no risk codes / cleartext identifiers)")
    print(f"  errors      {counts['error']}")
    for k, v in sorted(per_status.items()):
        print(f"    {k:<24} {v}")
    print(f"\n  platform stats: {json.dumps(client.get('/v1/stats').json(), indent=2)}")


if __name__ == "__main__":
    main()
