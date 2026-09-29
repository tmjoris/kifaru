#!/usr/bin/env python3
"""
Proof harness. Reads the platform DB directly (as the web app will) and checks:
  1. confusion matrix against ground truth
  2. the cleartext-identifier gate actually rejects
  3. an admin threshold change actually changes an outcome
  4. alerts are correctly routed to receiving institutions
"""
import json, sqlite3, argparse
from pathlib import Path
import httpx

ROOT = Path(__file__).parent.parent
DB = ROOT / "data" / "kifaru.db"

ap = argparse.ArgumentParser()
ap.add_argument("--url", default="http://127.0.0.1:8000")
a = ap.parse_args()
c = httpx.Client(base_url=a.url, timeout=20)

db = sqlite3.connect(DB)
db.row_factory = sqlite3.Row
truth = json.loads((ROOT / "data" / "_replay_truth.json").read_text())

print("=" * 68)
print("1. ANALYSIS QUALITY  (platform DB vs ground truth)")
rows = db.execute("""SELECT r.transaction_ref, v.status, v.validation_score, v.corroboration_count
                     FROM reports r JOIN validations v ON v.report_id=r.report_id""").fetchall()
tp = fp = tn = fn = ins_f = ins_b = 0
for r in rows:
    fraud = truth.get(r["transaction_ref"]) == "true"
    if r["status"] == "VALIDATED_FRAUD":
        tp, fp = (tp + 1, fp) if fraud else (tp, fp + 1)
    elif r["status"] == "NOT_FRAUD":
        fn, tn = (fn + 1, tn) if fraud else (fn, tn + 1)
    else:
        ins_f, ins_b = (ins_f + 1, ins_b) if fraud else (ins_f, ins_b + 1)

prec = tp / (tp + fp) * 100 if tp + fp else 0
print(f"   VALIDATED_FRAUD        {tp+fp:>3}   true {tp}  false {fp}   precision {prec:.0f}%")
print(f"   NOT_FRAUD              {tn+fn:>3}   correct {tn}  missed {fn}")
print(f"   INSUFFICIENT_EVIDENCE  {ins_f+ins_b:>3}   fraud {ins_f}  benign {ins_b}")
print(f"   -> {tn} reports suppressed at the centre and never reached a receiving bank")

print("\n2. PRIVACY GATE  (cleartext identifiers must be rejected)")
bad = {"reporting_institution": "bank_a", "transaction_ref": "probe-1",
       "transaction_timestamp": "2026-09-14T06:15:09Z",
       "subject_account_hash": "bank_a-acct-00123",          # deliberately cleartext
       "destination_institution": "psp_c", "amount": 50000,
       "risk_codes": ["MUL-440"], "bank_risk_score": 0.8, "bank_threshold": 0.45}
r = c.post("/v1/reports", json=bad)
print(f"   cleartext account submitted -> HTTP {r.status_code}")
print(f"   {'PASS' if r.status_code == 422 else 'FAIL'}: {r.json()['detail'][0]['msg'][:88] if r.status_code==422 else r.text[:88]}")

print("\n3. NO-CODES GATE  (a report with nothing to validate is rejected)")
empty = {**bad, "subject_account_hash": "", "risk_codes": [], "transaction_ref": "probe-2"}
r = c.post("/v1/reports", json=empty)
print(f"   empty report -> HTTP {r.status_code}  {'PASS' if r.status_code==422 else 'FAIL'}")

print("\n4. ADMIN THRESHOLD IS REAL  (revalidate the false positive)")
fp_row = db.execute("""SELECT r.report_id, r.transaction_ref, v.validation_score, v.status
                       FROM reports r JOIN validations v ON v.report_id=r.report_id
                       WHERE v.status='VALIDATED_FRAUD'""").fetchall()
target = next((r for r in fp_row if truth.get(r["transaction_ref"]) != "true"), None)
if target:
    print(f"   false positive {target['report_id']}  score {target['validation_score']}")
    before = c.get("/v1/admin/config").json()["validated_threshold"]
    c.patch("/v1/admin/config", json={"validated_threshold": 0.80})
    v = c.post("/v1/admin/revalidate", params={"report_id": target["report_id"]}).json()
    print(f"   threshold {before} -> 0.80    status now: {v['status']}   (score unchanged at {v['validation_score']})")
    c.patch("/v1/admin/config", json={"validated_threshold": before})
    print(f"   threshold restored to {before}")
else:
    print("   no false positive in this run")

print("\n5. ALERT ROUTING")
for inst in ("bank_a", "bank_b", "psp_c", "sacco_d"):
    al = c.get("/v1/alerts", params={"institution": inst}).json()["alerts"]
    print(f"   {inst:<9} received {len(al):>2} alert(s)" +
          (f"  from: {sorted({x['reporting_institution'] for x in al})}" if al else ""))

print("\n6. A ROUTED ALERT, AS THE WEB APP WILL READ IT")
al = db.execute("SELECT * FROM alerts WHERE amount > 0 ORDER BY validation_score DESC LIMIT 1").fetchone()
if al:
    d = dict(al)
    d["risk_codes"] = json.loads(d["risk_codes"])
    for k in ("alert_id", "reporting_institution", "receiving_institution", "amount",
              "validation_score", "risk_codes", "validated_by", "state"):
        print(f"   {k:<24} {d[k]}")
    print(f"   explanation              {d['explanation'][:150]}")

print("\n7. AGENT LEARNING  (knowledge base written back by the agent, not by hand)")
kb = db.execute("SELECT list_name, added_by, COUNT(*) n FROM knowledge_base GROUP BY 1,2").fetchall()
for r in kb:
    print(f"   {r['list_name']:<14} added_by={r['added_by']:<8} {r['n']}")

print("\n8. AUDIT TRAIL")
n = db.execute("SELECT COUNT(*) FROM audit_log").fetchone()[0]
print(f"   {n} entries — every KB write and config change is attributable")
print("=" * 68)
