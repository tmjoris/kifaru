"""
Persistence. SQLite so the web app can read the shared platform DB directly
with zero setup. Swap the connection for Postgres / Azure Table in production -
every query below is plain SQL.
"""
import sqlite3, json, os, threading
from pathlib import Path

DB_PATH = Path(os.getenv("KIFARU_DB", Path(__file__).parent.parent / "data" / "kifaru.db"))
_lock = threading.Lock()

SCHEMA = """
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS institutions (
  code TEXT PRIMARY KEY, name TEXT, type TEXT,
  threshold REAL DEFAULT 0.5, active INTEGER DEFAULT 1
);

CREATE TABLE IF NOT EXISTS reports (
  report_id TEXT PRIMARY KEY,
  submitted_at TEXT, submission_channel TEXT,
  reporting_institution TEXT, reporting_system TEXT,
  transaction_ref TEXT, transaction_timestamp TEXT,
  subject_account_hash TEXT, subject_customer_hash TEXT,
  destination_account_hash TEXT, destination_msisdn_hash TEXT,
  destination_institution TEXT,
  amount REAL, currency TEXT, channel TEXT,
  bank_risk_score REAL, bank_threshold REAL,
  risk_codes TEXT, evidence TEXT, narrative TEXT
);
CREATE INDEX IF NOT EXISTS ix_rep_inst ON reports(reporting_institution);
CREATE INDEX IF NOT EXISTS ix_rep_dest ON reports(destination_account_hash);
CREATE INDEX IF NOT EXISTS ix_rep_msisdn ON reports(destination_msisdn_hash);

CREATE TABLE IF NOT EXISTS validations (
  validation_id TEXT PRIMARY KEY,
  report_id TEXT, validated_at TEXT, agent_version TEXT,
  validation_score REAL, status TEXT,
  reason_codes TEXT, corroborating_institutions TEXT, corroboration_count INTEGER,
  explanation TEXT, latency_ms INTEGER, alert_id TEXT,
  FOREIGN KEY(report_id) REFERENCES reports(report_id)
);
CREATE INDEX IF NOT EXISTS ix_val_status ON validations(status);

CREATE TABLE IF NOT EXISTS alerts (
  alert_id TEXT PRIMARY KEY,
  validation_id TEXT, report_id TEXT, issued_at TEXT,
  receiving_institution TEXT, reporting_institution TEXT,
  destination_account_hash TEXT, destination_msisdn_hash TEXT,
  amount REAL, currency TEXT, risk_codes TEXT,
  validation_score REAL, validated_by TEXT, explanation TEXT, state TEXT
);
CREATE INDEX IF NOT EXISTS ix_alert_recv ON alerts(receiving_institution);

CREATE TABLE IF NOT EXISTS knowledge_base (
  artefact_hash TEXT, list_name TEXT, label TEXT, added_by TEXT, added_at TEXT,
  PRIMARY KEY (artefact_hash, list_name)
);

CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT, actor TEXT, action TEXT, target TEXT, detail TEXT
);
"""

DEFAULT_CONFIG = {
    "validated_threshold": 0.60,
    "insufficient_threshold": 0.35,
    "enabled_sources": ["soc_connector", "rest", "webhook", "batch"],
    "agent_version": "kifaru-agent-0.3.0",
}

INSTITUTIONS = [
    ("bank_a", "Tier-1 Bank A", "bank", 0.45),
    ("bank_b", "Tier-2 Bank B", "bank", 0.50),
    ("psp_c", "Mobile Money PSP C", "psp", 0.40),
    ("sacco_d", "Deposit-taking SACCO D", "sacco", 0.60),
]


def connect():
    c = sqlite3.connect(DB_PATH, check_same_thread=False, timeout=15)
    c.row_factory = sqlite3.Row
    return c


def init(reset: bool = False):
    DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    if reset and DB_PATH.exists():
        for suffix in ("", "-wal", "-shm"):
            p = Path(str(DB_PATH) + suffix)
            if p.exists():
                p.unlink()
    c = connect()
    c.executescript(SCHEMA)
    for code, name, typ, th in INSTITUTIONS:
        c.execute("INSERT OR IGNORE INTO institutions(code,name,type,threshold) VALUES (?,?,?,?)",
                  (code, name, typ, th))
    for k, v in DEFAULT_CONFIG.items():
        c.execute("INSERT OR IGNORE INTO config(key,value) VALUES (?,?)", (k, json.dumps(v)))
    c.commit()
    return c


# ------------------------------------------------------------------ config
def get_config(c):
    return {r["key"]: json.loads(r["value"]) for r in c.execute("SELECT * FROM config")}


def set_config(c, key, value, actor="admin"):
    with _lock:
        c.execute("INSERT INTO config(key,value) VALUES (?,?) "
                  "ON CONFLICT(key) DO UPDATE SET value=excluded.value", (key, json.dumps(value)))
        audit(c, actor, "config.set", key, json.dumps(value))
        c.commit()


def audit(c, actor, action, target, detail=""):
    from .schemas import utcnow
    c.execute("INSERT INTO audit_log(at,actor,action,target,detail) VALUES (?,?,?,?,?)",
              (utcnow(), actor, action, target, detail))


# ------------------------------------------------------------------ writes
def save_report(c, r: dict):
    with _lock:
        c.execute("""INSERT OR REPLACE INTO reports VALUES
            (:report_id,:submitted_at,:submission_channel,:reporting_institution,:reporting_system,
             :transaction_ref,:transaction_timestamp,:subject_account_hash,:subject_customer_hash,
             :destination_account_hash,:destination_msisdn_hash,:destination_institution,
             :amount,:currency,:channel,:bank_risk_score,:bank_threshold,
             :risk_codes,:evidence,:narrative)""", r)
        c.commit()


def save_validation(c, v: dict):
    with _lock:
        c.execute("""INSERT OR REPLACE INTO validations VALUES
            (:validation_id,:report_id,:validated_at,:agent_version,:validation_score,:status,
             :reason_codes,:corroborating_institutions,:corroboration_count,
             :explanation,:latency_ms,:alert_id)""", v)
        c.commit()


def save_alert(c, a: dict):
    with _lock:
        c.execute("""INSERT OR REPLACE INTO alerts VALUES
            (:alert_id,:validation_id,:report_id,:issued_at,:receiving_institution,
             :reporting_institution,:destination_account_hash,:destination_msisdn_hash,
             :amount,:currency,:risk_codes,:validation_score,:validated_by,:explanation,:state)""", a)
        c.commit()


def kb_add(c, artefact_hash, list_name, label="", actor="agent"):
    from .schemas import utcnow
    with _lock:
        c.execute("INSERT OR IGNORE INTO knowledge_base VALUES (?,?,?,?,?)",
                  (artefact_hash, list_name, label, actor, utcnow()))
        audit(c, actor, "kb.add", artefact_hash, list_name)
        c.commit()


def kb_remove(c, artefact_hash, list_name, actor="admin"):
    with _lock:
        c.execute("DELETE FROM knowledge_base WHERE artefact_hash=? AND list_name=?",
                  (artefact_hash, list_name))
        audit(c, actor, "kb.remove", artefact_hash, list_name)
        c.commit()


# ------------------------------------------------------------------ reads
def kb_lists(c):
    out = {"known_good": set(), "known_bad": set()}
    for r in c.execute("SELECT artefact_hash,list_name FROM knowledge_base"):
        out.setdefault(r["list_name"], set()).add(r["artefact_hash"])
    return out


def corroboration_candidates(c, dest_hash, msisdn_hash, device_profile, exclude_inst, limit=500):
    """Prior reports sharing any artefact with this one, from other institutions."""
    q = """SELECT report_id, reporting_institution, destination_account_hash,
                  destination_msisdn_hash, evidence
           FROM reports
           WHERE reporting_institution != ?
             AND (
               (destination_account_hash != '' AND destination_account_hash = ?)
               OR (destination_msisdn_hash != '' AND destination_msisdn_hash = ?)
               OR (? != '' AND evidence LIKE ?)
             )
           ORDER BY submitted_at DESC LIMIT ?"""
    like = f'%"device_profile": "{device_profile}"%' if device_profile else "%__never__%"
    return list(c.execute(q, (exclude_inst, dest_hash or "\x00", msisdn_hash or "\x00",
                              device_profile or "", like, limit)))


def alerts_for(c, institution, limit=200):
    return [dict(r) for r in c.execute(
        "SELECT * FROM alerts WHERE receiving_institution=? ORDER BY issued_at DESC LIMIT ?",
        (institution, limit))]


def reports_by(c, institution, limit=500):
    return [dict(r) for r in c.execute(
        """SELECT r.*, v.status, v.validation_score, v.reason_codes, v.explanation, v.alert_id
           FROM reports r LEFT JOIN validations v ON v.report_id = r.report_id
           WHERE r.reporting_institution=? ORDER BY r.submitted_at DESC LIMIT ?""",
        (institution, limit))]


def history_for(c, institution, limit=500):
    """Every record touching this institution, in either direction, both outcomes."""
    return [dict(r) for r in c.execute(
        """SELECT r.report_id, r.submitted_at, r.reporting_institution, r.destination_institution,
                  r.reporting_system, r.transaction_ref, r.subject_customer_hash,
                  r.destination_account_hash, r.destination_msisdn_hash,
                  r.amount, r.currency, r.channel, r.risk_codes, r.evidence, r.narrative,
                  v.validated_at, v.agent_version, v.status, v.validation_score,
                  v.reason_codes, v.corroborating_institutions, v.corroboration_count,
                  v.explanation, v.alert_id
           FROM reports r LEFT JOIN validations v ON v.report_id = r.report_id
           WHERE r.reporting_institution=? OR r.destination_institution=?
           ORDER BY r.submitted_at DESC LIMIT ?""",
        (institution, institution, limit))]


def stats(c):
    g = lambda q, *a: c.execute(q, a).fetchone()[0]
    return {
        "reports": g("SELECT COUNT(*) FROM reports"),
        "validations": g("SELECT COUNT(*) FROM validations"),
        "validated_fraud": g("SELECT COUNT(*) FROM validations WHERE status='VALIDATED_FRAUD'"),
        "insufficient": g("SELECT COUNT(*) FROM validations WHERE status='INSUFFICIENT_EVIDENCE'"),
        "not_fraud": g("SELECT COUNT(*) FROM validations WHERE status='NOT_FRAUD'"),
        "alerts": g("SELECT COUNT(*) FROM alerts"),
        "kb_known_bad": g("SELECT COUNT(*) FROM knowledge_base WHERE list_name='known_bad'"),
        "kb_known_good": g("SELECT COUNT(*) FROM knowledge_base WHERE list_name='known_good'"),
        "alerts_by_institution": {r["receiving_institution"]: r["n"] for r in c.execute(
            "SELECT receiving_institution, COUNT(*) n FROM alerts GROUP BY 1")},
        "reports_by_channel": {r["submission_channel"]: r["n"] for r in c.execute(
            "SELECT submission_channel, COUNT(*) n FROM reports GROUP BY 1")},
        "avg_latency_ms": round(g("SELECT COALESCE(AVG(latency_ms),0) FROM validations"), 1),
    }
