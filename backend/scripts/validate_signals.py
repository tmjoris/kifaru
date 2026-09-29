#!/usr/bin/env python3
"""
Validates the KIFARU dataset - and doubles as a starter detector.

Proves four things:
  1. CAMP-001 is detectable inside one institution by shared device profile
  2. CAMP-002 is NOT detectable by destination matching, but IS detectable
     by the mule_profile behavioural signature
  3. Legitimate high fan-in decoys are correctly suppressed
  4. The hero event scores APPROVE locally but matches a published indicator
"""

import csv, json
from collections import defaultdict

events = list(csv.DictReader(open("kifaru_events.csv")))
truth = json.load(open("kifaru_ground_truth.json"))
exchange = json.load(open("kifaru_exchange_seed.json"))
legit = {e["id"] for e in truth["legit_fanin_decoys"]}
legit_age = {e["id"]: e["age_days"] for e in truth["legit_fanin_decoys"]}

print("=" * 66)

# --- 1. shared device: naive rule vs conjunction ------------------------
by_dev, dev_dest = defaultdict(set), defaultdict(set)
for e in events:
    if e["institution_id"] == "bank_a" and e["device_profile_hash"]:
        by_dev[e["device_profile_hash"]].add(e["customer_id"])
        if e["beneficiary_id"]:
            dev_dest[(e["device_profile_hash"], e["beneficiary_id"])].add(e["customer_id"])

naive = {d: c for d, c in by_dev.items() if len(c) >= 3}
conj  = {k: c for k, c in dev_dest.items() if len(c) >= 3}

print("1. SHARED DEVICE (bank_a)")
print(f"   NAIVE  'profile shared by >=3 customers':      {len(naive)} hits")
print("          -> unusable. Cheap identical Android handsets in Kenya produce")
print("             genuine profile collisions. Profile alone is a WEAK signal.")
print(f"   CONJUNCTION  'shared profile AND converging destination':  {len(conj)} hits")
for (d, dest), c in conj.items():
    hit = [e for e in events if e["device_profile_hash"] == d and e["beneficiary_id"] == dest]
    fr = sum(1 for e in hit if e["is_fraud"] == "true")
    print(f"     {d} -> {dest}")
    print(f"     {len(c)} customers, {len(hit)} events, {fr} confirmed fraud")
print("   => convergence is what turns a weak signal into a campaign.\n")

# --- 2a. destination matching on CAMP-002 -------------------------------
c2 = next(c for c in truth["campaigns"] if c["campaign_id"] == "CAMP-002")
c2_ids = set(c2["event_ids"])
dests = {e["beneficiary_id"] for e in events if e["event_id"] in c2_ids and e["beneficiary_id"]}
devs = {e["device_profile_hash"] for e in events if e["event_id"] in c2_ids}
print(f"2a. DESTINATION / DEVICE MATCHING on CAMP-002")
print(f"    distinct destination accounts: {len(dests)}  (no reuse -> matching fails)")
print(f"    distinct device profiles:      {len(devs)}  (no reuse -> matching fails)")
print(f"    => entity matching CANNOT link this campaign\n")

# --- 2b. mule_profile behavioural signature ----------------------------
sig = next(i for i in exchange if i["pattern_type"] == "mule_profile")["indicator_value"]

def is_mule(e):
    try:
        if int(e["account_age_days"]) >= sig["account_age_days_lt"]: return False
        if int(e["distinct_senders_7d"] or 0) < sig["distinct_senders_7d_gte"]: return False
        if float(e["flow_through_ratio"] or 0) <= sig["flow_through_ratio_gt"]: return False
        if int(e["dwell_minutes"] or 999) >= sig["dwell_minutes_lt"]: return False
    except (ValueError, TypeError):
        return False
    return True

hits = [e for e in events if is_mule(e)]
tp = [e for e in hits if e["is_fraud"] == "true"]
fp = [e for e in hits if e["is_fraud"] != "true"]
print(f"2b. MULE_PROFILE SIGNATURE (age<14d, senders>=5, flow>0.90, dwell<10m)")
print(f"    matches: {len(hits)}   true fraud: {len(tp)}   false: {len(fp)}")
print(f"    precision: {len(tp)/max(len(hits),1)*100:.0f}%")
print(f"    => behavioural signature links what entity matching cannot\n")

# --- 3. suppression of legitimate high fan-in --------------------------
fanin = defaultdict(set)
for e in events:
    if e["beneficiary_id"]:
        fanin[e["beneficiary_id"]].add(e["customer_id"])
high = {d: s for d, s in fanin.items() if len(s) >= 5}
caught_legit = [d for d in high if d in legit]
print(f"3. FAN-IN SUPPRESSION")
print(f"   destinations with >=5 distinct senders: {len(high)}")
print(f"   of those, legitimate decoys: {len(caught_legit)}  <-- would be FALSE POSITIVES")
for d in caught_legit:
    print(f"     {d}  senders={len(high[d])}  age={legit_age[d]}d")
suppressed = [d for d in caught_legit if legit_age[d] > 365]
survivors = [d for d in high if d not in legit]
mules = [d for d in survivors if "MULE" in d]
print(f"   suppressed by 'destination age > 365d' rule: {len(suppressed)}/{len(caught_legit)}")
print(f"   surviving after suppression: {len(survivors)}  of which seeded mules: {len(mules)}")
print("   => fan-in alone is NOT a fraud signal. Without the age/merchant")
print("      suppression list you would freeze school fees across the sector.\n")

# --- 4. the hero event --------------------------------------------------
c3 = next(c for c in truth["campaigns"] if c["campaign_id"] == "CAMP-003")
hero = next(e for e in events if e["event_id"] == c3["hero_event_id"])
print(f"4. HERO EVENT  {hero['event_id']}")
print(f"   institution      {hero['institution_id']}  ({hero['amount']} {hero['currency']})")
print(f"   local SOC score  {hero['soc_risk_score']}  ->  {hero['soc_verdict']}")
print(f"   rules fired      {hero['soc_rules_fired'] or '(none)'}")
print(f"   ground truth     is_fraud={hero['is_fraud']}  campaign={hero['campaign_id']}")
print(f"   matches published mule_profile indicator: {is_mule(hero)}")
print(f"   => local engine approves it. KIFARU holds it.\n")

# --- headline -----------------------------------------------------------
fr = [e for e in events if e["is_fraud"] == "true"]
missed = [e for e in fr if e["soc_verdict"] in ("APPROVE", "MONITOR")]
print("=" * 66)
print(f"ADDRESSABLE GAP: {len(missed)}/{len(fr)} fraud events ({len(missed)/len(fr)*100:.0f}%) "
      f"were not stopped\n                 by institution-local scoring alone.")
print("=" * 66)
