#!/usr/bin/env python3
"""
KIFARU synthetic dataset generator.

Produces a multi-institution transaction stream with SOC/fraud-engine
scoring attached, containing deliberately seeded cross-institution fraud
campaigns plus legitimate high-fan-in decoys.

Design rule: the SOC scorer is INSTITUTION-BLIND. It only sees what one
institution can see. That is what creates the demo moment - fraudulent
transactions that score LOW locally but are provably part of a campaign.

Stdlib only. Deterministic (seed=42).
"""

import csv, json, random, hashlib, uuid
from datetime import datetime, timedelta, timezone

SEED = 42
random.seed(SEED)

START = datetime(2026, 9, 1, 0, 0, 0, tzinfo=timezone.utc)
DAYS = 14
N_BENIGN = 11000

INSTITUTIONS = {
    "bank_a":  {"name": "Tier-1 Bank A",         "type": "bank",        "customers": 2600, "platform": "Microsoft Sentinel + in-house fraud engine"},
    "bank_b":  {"name": "Tier-2 Bank B",         "type": "bank",        "customers": 900,  "platform": "Vendor SIEM (rules only)"},
    "psp_c":   {"name": "Mobile Money PSP C",    "type": "psp",         "customers": 4200, "platform": "In-house risk engine"},
    "sacco_d": {"name": "Deposit-taking SACCO D","type": "sacco",       "customers": 500,  "platform": "Core banking alerts only"},
}

EVENT_TYPES = ["login", "transfer_out", "transfer_in", "beneficiary_add",
               "wallet_topup", "agent_withdrawal", "device_registration", "sim_change_flag"]

CHANNELS = ["mobile_app", "ussd", "internet_banking", "agent", "api"]

OS_BUILDS = ["Android 13; SM-A047F", "Android 14; TECNO KI5k", "Android 12; Infinix X6819",
             "iOS 17.4; iPhone13,2", "Android 13; RMX3627", "Android 11; itel A662L"]
SCREENS   = ["720x1600", "1080x2400", "1080x2340", "828x1792", "720x1612"]
LOCALES   = ["en_KE", "sw_KE", "en_GB"]

# Safaricom / Airtel-style prefixes
MSISDN_PREFIX = ["2547", "2541"]
KE_ASNS = [("AS33771", "Safaricom"), ("AS36866", "Airtel KE"), ("AS15399", "Wananchi"),
           ("AS37061", "Jamii Telecom"), ("AS327687", "Liquid KE")]
FOREIGN_ASNS = [("AS9009", "M247 (VPN)"), ("AS16509", "AWS"), ("AS14061", "DigitalOcean")]


# ---------------------------------------------------------------- helpers

def ts(offset_minutes, jitter=True):
    t = START + timedelta(minutes=offset_minutes)
    if jitter:
        t += timedelta(seconds=random.randint(0, 59))
    return t


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def device_profile_hash(os_build, screen, locale, tz_off, fonts):
    raw = f"{os_build}|{screen}|{locale}|{tz_off}|{fonts}"
    return "dp:" + hashlib.sha256(raw.encode()).hexdigest()[:16]


def make_device(fraud=False):
    os_build = random.choice(OS_BUILDS)
    screen = random.choice(SCREENS)
    locale = random.choice(LOCALES)
    tz_off = "+03:00" if not fraud or random.random() > 0.35 else random.choice(["+03:00", "+00:00", "+01:00"])
    fonts = random.randint(180, 260)
    return {
        "device_id": ("device-" + uuid.uuid4().hex[:10]),
        "device_profile_hash": device_profile_hash(os_build, screen, locale, tz_off, fonts),
        "os_build": os_build,
        "is_emulator": fraud and random.random() < 0.25,
        "is_rooted": fraud and random.random() < 0.30,
    }


def make_ip(foreign=False):
    if foreign:
        asn, org = random.choice(FOREIGN_ASNS)
        return f"{random.randint(45,199)}.{random.randint(0,255)}.{random.randint(0,255)}.{random.randint(1,254)}", asn, org
    asn, org = random.choice(KE_ASNS)
    return f"41.{random.randint(60,220)}.{random.randint(0,255)}.{random.randint(1,254)}", asn, org


def make_msisdn():
    return random.choice(MSISDN_PREFIX) + str(random.randint(10000000, 99999999))


def diurnal_minute(day):
    """Realistic time-of-day weighting; small night-time tail."""
    r = random.random()
    if r < 0.06:      hour = random.choice([0, 1, 2, 3, 4])          # night
    elif r < 0.30:    hour = random.randint(7, 11)                   # morning peak
    elif r < 0.62:    hour = random.randint(12, 16)                  # afternoon
    else:             hour = random.randint(17, 22)
    return day * 1440 + hour * 60 + random.randint(0, 59)


# ---------------------------------------------------------------- populations

customers, accounts, devices_by_customer = {}, {}, {}

for inst, meta in INSTITUTIONS.items():
    for i in range(meta["customers"]):
        cid = f"{inst}-cust-{i:05d}"
        customers[cid] = {"institution_id": inst, "msisdn": make_msisdn()}
        accounts[cid] = {
            "account_id": f"{inst}-acct-{i:05d}",
            "account_age_days": random.choices(
                [random.randint(1, 30), random.randint(31, 365), random.randint(366, 3000)],
                weights=[0.05, 0.25, 0.70])[0],
        }
        devices_by_customer[cid] = [make_device() for _ in range(random.choices([1, 2, 3], [0.72, 0.22, 0.06])[0])]

by_inst = {k: [c for c in customers if customers[c]["institution_id"] == k] for k in INSTITUTIONS}

# Legitimate high-fan-in entities. These MUST NOT be flagged - they are the
# suppression test. Real Kenyan equivalents: school fees, chama, paybill, SACCO.
LEGIT_FANIN = [
    {"id": "paybill-400200-schoolfees", "label": "Secondary school fees collection", "age_days": 1840, "merchant": True},
    {"id": "paybill-247247-utility",    "label": "Utility paybill",                  "age_days": 2600, "merchant": True},
    {"id": "psp_c-acct-chama-0001",     "label": "Registered chama group account",   "age_days": 900,  "merchant": False},
    {"id": "sacco_d-acct-dividend",     "label": "SACCO dividend disbursement",      "age_days": 2200, "merchant": True},
    {"id": "paybill-888880-church",     "label": "Church offertory paybill",         "age_days": 1500, "merchant": True},
    # HARD CASES: genuinely legitimate, but newly onboarded. These look
    # statistically identical to a mule account. Not in the knowledge base.
    {"id": "psp_c-acct-NEWMERCH-01",    "label": "New online retailer, onboarded day 3",  "age_days": 3,  "merchant": True, "hard": True},
    {"id": "bank_b-acct-NEWSACCO-02",   "label": "New chama, first contribution cycle",   "age_days": 6,  "merchant": False, "hard": True},
    {"id": "psp_c-acct-NEWEVENT-03",    "label": "Funeral contribution drive (harambee)", "age_days": 2,  "merchant": False, "hard": True},
]

events, soc_alerts, ground_truth = [], [], {"campaigns": [], "legit_fanin_decoys": LEGIT_FANIN}


# ---------------------------------------------------------------- SOC scorer
# INSTITUTION-BLIND BY DESIGN. Sees only this institution's own context.

def soc_score(ev):
    score, rules = 0.0, []
    hour = int(ev["timestamp"][11:13])

    if ev["amount"] >= 100000:
        score += 0.22; rules.append("AMT_HIGH_VALUE")
    elif ev["amount"] >= 50000:
        score += 0.12; rules.append("AMT_ELEVATED")

    if ev["is_new_beneficiary"] == "true":
        score += 0.15; rules.append("BEN_NEW_BENEFICIARY")
    if ev["is_new_device"] == "true":
        score += 0.14; rules.append("DEV_UNRECOGNISED_DEVICE")
    if ev["is_emulator"] == "true":
        score += 0.20; rules.append("DEV_EMULATOR_DETECTED")
    if ev["is_rooted"] == "true":
        score += 0.12; rules.append("DEV_ROOTED_DEVICE")
    if ev["ip_is_foreign"] == "true":
        score += 0.18; rules.append("NET_FOREIGN_ASN")
    if 0 <= hour <= 4:
        score += 0.10; rules.append("TIME_OFF_HOURS")
    if ev["velocity_1h"] >= 4:
        score += 0.16; rules.append("VEL_BURST_1H")
    if ev["sim_swap_age_days"] != "" and int(ev["sim_swap_age_days"]) <= 3:
        score += 0.25; rules.append("SIM_RECENT_SWAP")
    if ev["account_age_days"] <= 14 and ev["event_type"] in ("transfer_in", "wallet_topup"):
        score += 0.08; rules.append("ACCT_NEW_INBOUND")

    # deliberate noise: benign activity that trips rules (false positives)
    if random.random() < 0.012:
        score += 0.35; rules.append("ANOM_STATISTICAL_OUTLIER")

    score = round(min(score, 0.99), 2)
    if score >= 0.70:   sev, verdict = "High",     "BLOCK"
    elif score >= 0.45: sev, verdict = "Medium",   "REVIEW"
    elif score >= 0.25: sev, verdict = "Low",      "MONITOR"
    else:               sev, verdict = "Informational", "APPROVE"
    return score, sev, verdict, rules


def emit(inst, etype, cust, device, amount, t, **kw):
    ip, asn, org = kw.get("ip_tuple") or make_ip(kw.get("foreign", False))
    ev = {
        "event_id": "evt-" + uuid.uuid4().hex[:12],
        "timestamp": iso(t),
        "institution_id": inst,
        "institution_type": INSTITUTIONS[inst]["type"],
        "event_type": etype,
        "channel": kw.get("channel", random.choice(CHANNELS)),
        "customer_id": cust,
        "account_id": accounts[cust]["account_id"] if cust in accounts else kw.get("account_id", ""),
        "customer_msisdn": customers[cust]["msisdn"] if cust in customers else "",
        "device_id": device["device_id"],
        "device_profile_hash": device["device_profile_hash"],
        "is_emulator": str(device.get("is_emulator", False)).lower(),
        "is_rooted": str(device.get("is_rooted", False)).lower(),
        "ip_address": ip,
        "ip_asn": asn,
        "ip_org": org,
        "ip_is_foreign": str(kw.get("foreign", False)).lower(),
        "beneficiary_id": kw.get("beneficiary_id", ""),
        "beneficiary_msisdn": kw.get("beneficiary_msisdn", ""),
        "beneficiary_institution": kw.get("beneficiary_institution", ""),
        "amount": round(amount, 2),
        "currency": "KES",
        "is_new_beneficiary": str(kw.get("new_ben", False)).lower(),
        "is_new_device": str(kw.get("new_device", False)).lower(),
        "account_age_days": kw.get("account_age_days", accounts.get(cust, {}).get("account_age_days", 400)),
        "distinct_senders_7d": kw.get("distinct_senders_7d", 0),
        "flow_through_ratio": kw.get("flow_through_ratio", ""),
        "dwell_minutes": kw.get("dwell_minutes", ""),
        "velocity_1h": kw.get("velocity_1h", random.choices([0, 1, 2, 5], [0.72, 0.18, 0.08, 0.02])[0]),
        "sim_swap_age_days": kw.get("sim_swap_age_days", ""),
        "campaign_id": kw.get("campaign_id", ""),
        "is_fraud": str(kw.get("is_fraud", False)).lower(),
        "fraud_role": kw.get("fraud_role", ""),
    }
    s, sev, verdict, rules = soc_score(ev)
    ev["soc_risk_score"] = s
    ev["soc_severity"] = sev
    ev["soc_verdict"] = verdict
    ev["soc_rules_fired"] = "|".join(rules)
    ev["soc_platform"] = INSTITUTIONS[inst]["platform"]

    if s >= 0.25:
        disp = "true_positive" if ev["is_fraud"] == "true" else random.choices(
            ["false_positive", "benign_true_positive", "pending"], [0.62, 0.23, 0.15])[0]
        soc_alerts.append({
            "alert_id": "alrt-" + uuid.uuid4().hex[:10],
            "timestamp": ev["timestamp"],
            "institution_id": inst,
            "source_platform": ev["soc_platform"],
            "related_event_id": ev["event_id"],
            "rule_ids": ev["soc_rules_fired"],
            "entity_type": "account",
            "entity_value": ev["account_id"],
            "risk_score": s,
            "severity": sev,
            "verdict": verdict,
            "status": random.choices(["new", "in_progress", "closed"], [0.35, 0.30, 0.35])[0],
            "analyst_disposition": disp,
            "queue_age_minutes": random.randint(3, 4200),
        })
    events.append(ev)
    return ev


# ---------------------------------------------------------------- benign traffic

print("generating benign traffic...")
for _ in range(N_BENIGN):
    inst = random.choices(list(INSTITUTIONS), [0.34, 0.14, 0.42, 0.10])[0]
    cust = random.choice(by_inst[inst])
    dev = random.choice(devices_by_customer[cust])
    day = random.randint(0, DAYS - 1)
    t = ts(diurnal_minute(day))
    etype = random.choices(EVENT_TYPES, [0.30, 0.24, 0.16, 0.05, 0.14, 0.08, 0.02, 0.01])[0]
    amt = 0.0 if etype in ("login", "device_registration", "sim_change_flag") else \
        random.choices([random.uniform(100, 3000), random.uniform(3000, 25000),
                        random.uniform(25000, 90000), random.uniform(90000, 240000)],
                       [0.46, 0.34, 0.16, 0.04])[0]
    emit(inst, etype, cust, dev, amt, t,
         new_ben=(etype == "beneficiary_add"),
         new_device=random.random() < 0.05,
         beneficiary_msisdn=make_msisdn() if etype in ("transfer_out", "wallet_topup") else "")

# legitimate high fan-in: many unrelated senders -> one old, registered destination
print("generating legitimate high-fan-in decoys...")
for ent in LEGIT_FANIN:
    for _ in range(random.randint(90, 160)):
        inst = random.choices(list(INSTITUTIONS), [0.30, 0.15, 0.45, 0.10])[0]
        cust = random.choice(by_inst[inst])
        dev = random.choice(devices_by_customer[cust])
        t = ts(diurnal_minute(random.randint(0, DAYS - 1)))
        hard = ent.get("hard", False)
        emit(inst, "transfer_out", cust, dev, random.uniform(500, 45000), t,
             beneficiary_id=ent["id"], new_ben=True if hard else random.random() < 0.3,
             distinct_senders_7d=random.randint(6, 24) if hard else random.randint(60, 300),
             account_age_days=accounts[cust]["account_age_days"],
             fraud_role="legit_hard_case" if hard else "legit_high_fanin_decoy")


# ---------------------------------------------------------------- campaigns

def register(cid, typology, description, note):
    ground_truth["campaigns"].append({
        "campaign_id": cid, "typology": typology,
        "description": description, "detection_note": note, "event_ids": []})
    return ground_truth["campaigns"][-1]


# --- CAMP-001: shared device, bank_a. The classic. Easy to detect locally.
print("seeding CAMP-001 shared_device...")
c1 = register("CAMP-001", "shared_device",
              "Three unrelated bank_a customers operated from one device profile, funds converging.",
              "Detectable by bank_a alone. This is the publishing event in the demo.")
fraud_dev = make_device(fraud=True)
mule_a = "bank_b-acct-MULE-0001"
base = 3 * 1440 + 6 * 60
for i, cust in enumerate(random.sample(by_inst["bank_a"], 3)):
    for k, (etype, amt, off) in enumerate([
            ("login", 0.0, 0), ("beneficiary_add", 0.0, 4), ("transfer_out", [62000, 48500, 71000][i], 9)]):
        ev = emit("bank_a", etype, cust, fraud_dev, amt, ts(base + i * 95 + off),
                  beneficiary_id=mule_a, beneficiary_institution="bank_b",
                  new_ben=(etype == "beneficiary_add"), new_device=True,
                  campaign_id="CAMP-001", is_fraud=True, fraud_role="victim_account",
                  velocity_1h=3, channel="mobile_app")
        c1["event_ids"].append(ev["event_id"])

# --- CAMP-002: mule PROFILE. Every destination is a DIFFERENT account.
# Destination matching fails here by construction. Only the behavioural
# signature generalises. This is the campaign that proves the model.
print("seeding CAMP-002 mule_profile (rotating destinations)...")
c2 = register("CAMP-002", "mule_profile",
              "Layering ring using 6 distinct, newly-opened psp_c wallets. No shared destination, "
              "no shared device. Linked only by mule behavioural signature: new account, high fan-in "
              "from unrelated senders, flow-through ratio near 1.0, dwell under 10 minutes.",
              "Undetectable by destination or device matching. Requires the mule_profile signature.")
controller_msisdns = [make_msisdn() for _ in range(2)]   # controller cash-out numbers - the durable link
base = 5 * 1440 + 9 * 60
for m in range(6):
    mule_acct = f"psp_c-acct-MULE-{m:04d}"
    mule_age = random.randint(2, 11)
    senders = []
    for src_inst in ("bank_a", "bank_b", "sacco_d"):
        senders += random.sample(by_inst[src_inst], random.randint(2, 3))
    total_in = 0.0
    for j, s in enumerate(senders):
        amt = random.uniform(18000, 74000)
        total_in += amt
        ev = emit(customers[s]["institution_id"], "transfer_out", s,
                  random.choice(devices_by_customer[s]), amt,
                  ts(base + m * 210 + j * 7),
                  beneficiary_id=mule_acct, beneficiary_institution="psp_c",
                  new_ben=True, distinct_senders_7d=len(senders),
                  campaign_id="CAMP-002", is_fraud=True, fraud_role="layering_inbound")
        c2["event_ids"].append(ev["event_id"])
    # cash-out: fast, near-total, to a controller number
    out_dev = make_device(fraud=True)
    ev = emit("psp_c", "agent_withdrawal", random.choice(by_inst["psp_c"]), out_dev,
              total_in * random.uniform(0.93, 0.985),
              ts(base + m * 210 + len(senders) * 7 + random.randint(2, 9)),
              account_id=mule_acct, account_age_days=mule_age,
              beneficiary_msisdn=random.choice(controller_msisdns),
              distinct_senders_7d=len(senders),
              flow_through_ratio=round(random.uniform(0.93, 0.985), 3),
              dwell_minutes=random.randint(2, 9),
              campaign_id="CAMP-002", is_fraud=True, fraud_role="mule_cashout",
              channel="agent")
    c2["event_ids"].append(ev["event_id"])

# --- CAMP-003: SIM swap -> takeover. The hero transaction of the demo.
print("seeding CAMP-003 sim_swap_takeover...")
c3 = register("CAMP-003", "sim_swap_takeover",
              "SIM swap at bank_b, then account takeover and onward transfer to psp_c. "
              "The receiving PSP transaction scores LOW on its own engine.",
              "HERO PATH: psp_c local score is Informational/APPROVE. Only the indicator "
              "published by bank_a (CAMP-001 device profile) flips it to HOLD.")
victim = random.choice(by_inst["bank_b"])
base = 6 * 1440 + 13 * 60
for etype, amt, off, kw in [
        ("sim_change_flag", 0.0, 0, {}),
        ("device_registration", 0.0, 38, {"new_device": True}),
        ("login", 0.0, 41, {"new_device": True}),
        ("beneficiary_add", 0.0, 44, {"new_ben": True, "new_device": True}),
        ("transfer_out", 96500.0, 49, {"new_ben": True, "new_device": True})]:
    ev = emit("bank_b", etype, victim, fraud_dev, amt, ts(base + off),
              beneficiary_id="psp_c-acct-MULE-0002", beneficiary_institution="psp_c",
              sim_swap_age_days=1, campaign_id="CAMP-003", is_fraud=True,
              fraud_role="takeover", velocity_1h=4, **kw)
    c3["event_ids"].append(ev["event_id"])

# The hero event: arrives at psp_c, local engine says APPROVE.
hero_dev = make_device()          # clean device - nothing locally suspicious
hero = emit("psp_c", "wallet_topup", random.choice(by_inst["psp_c"]), hero_dev,
            96500.0, ts(base + 51),
            account_id="psp_c-acct-MULE-0002", account_age_days=9,
            beneficiary_msisdn=controller_msisdns[0],
            distinct_senders_7d=7, flow_through_ratio=0.962, dwell_minutes=4,
            campaign_id="CAMP-003", is_fraud=True, fraud_role="HERO_low_local_score",
            velocity_1h=1, channel="mobile_app")
c3["event_ids"].append(hero["event_id"])
c3["hero_event_id"] = hero["event_id"]
c3["hero_local_score"] = hero["soc_risk_score"]
c3["hero_local_verdict"] = hero["soc_verdict"]

# --- CAMP-004: recipient MSISDN reuse across two institutions.
print("seeding CAMP-004 msisdn_reuse...")
c4 = register("CAMP-004", "msisdn_reuse",
              "Same two cash-out MSISDNs receive funds via different accounts at bank_b and sacco_d.",
              "Accounts and devices all differ. Only the recipient MSISDN links the two institutions.")
base = 9 * 1440 + 14 * 60
for i in range(8):
    src_inst = "bank_b" if i % 2 == 0 else "sacco_d"
    s = random.choice(by_inst[src_inst])
    ev = emit(src_inst, "transfer_out", s, make_device(fraud=True),
              random.uniform(24000, 68000), ts(base + i * 34),
              beneficiary_id=f"{src_inst}-acct-DROP-{i:03d}",
              beneficiary_msisdn=random.choice(controller_msisdns),
              new_ben=True, campaign_id="CAMP-004", is_fraud=True,
              fraud_role="msisdn_cashout")
    c4["event_ids"].append(ev["event_id"])

# hard-case destinations sweeping their own float - looks exactly like a mule
for ent in [e for e in LEGIT_FANIN if e.get("hard")]:
    inst = "psp_c" if ent["id"].startswith("psp_c") else "bank_b"
    for k in range(random.randint(2, 4)):
        emit(inst, "agent_withdrawal" if inst == "psp_c" else "transfer_out",
             random.choice(by_inst[inst]), make_device(),
             random.uniform(40000, 180000),
             ts(random.randint(2, 12) * 1440 + random.randint(9, 18) * 60),
             account_id=ent["id"], account_age_days=ent["age_days"],
             distinct_senders_7d=random.randint(6, 24),
             flow_through_ratio=round(random.uniform(0.91, 0.97), 3),
             dwell_minutes=random.randint(3, 9),
             fraud_role="legit_hard_case", channel="agent")

events.sort(key=lambda e: e["timestamp"])
soc_alerts.sort(key=lambda a: a["timestamp"])

# --- pre-published exchange indicators (what the exchange looks like at t=0)
exchange_seed = [{
    "indicator_id": "ind-" + uuid.uuid4().hex[:10],
    "published_by": "bank_a",
    "pattern_type": "shared_device_profile",
    "indicator_value": fraud_dev["device_profile_hash"],
    "confidence": 0.95,
    "sensitivity": "shared",
    "published_at": iso(ts(3 * 1440 + 6 * 60 + 25)),
    "expires_at": iso(ts(3 * 1440 + 6 * 60 + 25) + timedelta(days=30)),
    "retracted": False,
    "source_campaign": "CAMP-001",
}, {
    "indicator_id": "ind-" + uuid.uuid4().hex[:10],
    "published_by": "bank_a",
    "pattern_type": "mule_profile",
    "indicator_value": {"account_age_days_lt": 14, "distinct_senders_7d_gte": 5,
                        "flow_through_ratio_gt": 0.90, "dwell_minutes_lt": 10,
                        "sender_relationship": "none"},
    "confidence": 0.88,
    "sensitivity": "shared",
    "published_at": iso(ts(5 * 1440 + 11 * 60)),
    "expires_at": iso(ts(5 * 1440 + 11 * 60) + timedelta(days=14)),
    "retracted": False,
    "source_campaign": "CAMP-002",
}]

# ---------------------------------------------------------------- write

cols = list(events[0].keys())
with open("kifaru_events.csv", "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=cols); w.writeheader(); w.writerows(events)
with open("kifaru_events.jsonl", "w") as f:
    for e in events: f.write(json.dumps(e) + "\n")
with open("kifaru_soc_alerts.csv", "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=list(soc_alerts[0].keys())); w.writeheader(); w.writerows(soc_alerts)
with open("kifaru_ground_truth.json", "w") as f:
    json.dump(ground_truth, f, indent=2)
with open("kifaru_exchange_seed.json", "w") as f:
    json.dump(exchange_seed, f, indent=2)

fraud_n = sum(1 for e in events if e["is_fraud"] == "true")
missed = [e for e in events if e["is_fraud"] == "true" and e["soc_verdict"] in ("APPROVE", "MONITOR")]
fp = [a for a in soc_alerts if a["analyst_disposition"] == "false_positive"]

print(f"\n  events              {len(events):,}")
print(f"  fraud events        {fraud_n} ({fraud_n/len(events)*100:.2f}%)")
print(f"  SOC alerts          {len(soc_alerts):,}")
print(f"  high severity       {sum(1 for a in soc_alerts if a['severity']=='High'):,}")
print(f"  false positives     {len(fp):,} ({len(fp)/len(soc_alerts)*100:.1f}% of alerts)")
print(f"  fraud MISSED by local SOC (APPROVE/MONITOR): {len(missed)}  <-- KIFARU's addressable gap")
print(f"  HERO event {hero['event_id']}  local score {hero['soc_risk_score']} -> {hero['soc_verdict']}")
