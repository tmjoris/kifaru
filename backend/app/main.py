"""
KIFARU platform API.

Pipeline:  ingest -> normalize -> validate -> route or suppress -> persist -> stream
"""
import json, uuid, asyncio, csv, io, hashlib
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException, Query, Request
from fastapi.responses import StreamingResponse, JSONResponse, FileResponse
from fastapi.middleware.cors import CORSMiddleware

from .schemas import ReportIn, Report, Channel, Status, AlertState, ConfigPatch, KBEntry, utcnow
from .normalize import normalize, describe, NormalizationError, STANDARD, CODES
from .agent.validator import score_report, explain, AGENT_VERSION
from . import store

DATA = Path(__file__).parent.parent / "data"
app = FastAPI(title="KIFARU Platform", version="0.3.0",
              description="Banks detect. KIFARU validates. Only validated fraud reaches a receiving bank.")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

conn = store.init()
_subscribers: dict[str, list[asyncio.Queue]] = {}

DISPLAY_TO_CODE = {
    "NCBA": "bank_a",
    "KCB": "bank_b",
    "Equity": "psp_c",
    "I&M": "sacco_d",
    "bank_a": "bank_a",
    "bank_b": "bank_b",
    "psp_c": "psp_c",
    "sacco_d": "sacco_d",
}
CODE_TO_DISPLAY = {
    "bank_a": "NCBA",
    "bank_b": "KCB",
    "psp_c": "Equity",
    "sacco_d": "I&M",
}


def _publish(institution: str, payload: dict):
    for q in _subscribers.get(institution, []):
        try:
            q.put_nowait(payload)
        except asyncio.QueueFull:
            pass
    for q in _subscribers.get("*", []):
        try:
            q.put_nowait(payload)
        except asyncio.QueueFull:
            pass


# =====================================================================
# the pipeline
# =====================================================================
def process(payload: ReportIn, channel: Channel) -> dict:
    cfg = store.get_config(conn)
    if channel.value not in cfg["enabled_sources"]:
        raise HTTPException(403, f"submission channel '{channel.value}' is disabled by admin")

    inst = conn.execute("SELECT * FROM institutions WHERE code=?",
                        (payload.reporting_institution,)).fetchone()
    if not inst:
        raise HTTPException(400, f"unknown institution '{payload.reporting_institution}'")

    report = payload.model_dump()
    report["report_id"] = "rpt-" + uuid.uuid4().hex[:12]
    report["submitted_at"] = utcnow()
    report["submission_channel"] = channel.value
    if not report.get("bank_threshold"):
        report["bank_threshold"] = inst["threshold"]

    # ---- normalize
    try:
        codes = normalize(report)
    except NormalizationError as e:
        raise HTTPException(422, str(e))
    report["risk_codes"] = codes

    # ---- validate (agent)
    validation = score_report(conn, report, codes)
    validation["explanation"] = explain(report, validation, codes)

    # ---- persist the report
    store.save_report(conn, {
        **{k: report[k] for k in (
            "report_id", "submitted_at", "submission_channel", "reporting_institution",
            "reporting_system", "transaction_ref", "transaction_timestamp",
            "subject_account_hash", "subject_customer_hash", "destination_account_hash",
            "destination_msisdn_hash", "destination_institution", "amount", "currency",
            "channel", "bank_risk_score", "bank_threshold", "narrative")},
        "risk_codes": json.dumps(codes),
        "evidence": json.dumps(report.get("evidence") or {}),
    })

    # ---- route or suppress
    alert = None
    if validation["status"] == Status.VALIDATED_FRAUD.value and report["destination_institution"]:
        alert = {
            "alert_id": "alt-" + uuid.uuid4().hex[:10],
            "validation_id": validation["validation_id"],
            "report_id": report["report_id"],
            "issued_at": utcnow(),
            "receiving_institution": report["destination_institution"],
            "reporting_institution": report["reporting_institution"],
            "destination_account_hash": report["destination_account_hash"],
            "destination_msisdn_hash": report["destination_msisdn_hash"],
            "amount": report["amount"],
            "currency": report["currency"],
            "risk_codes": json.dumps(codes),
            "validation_score": validation["validation_score"],
            "validated_by": "KIFARU validation agent",
            "explanation": validation["explanation"],
            "state": AlertState.sent.value,
        }
        validation["alert_id"] = alert["alert_id"]
        store.save_alert(conn, alert)
        # agent learns: a validated destination joins known_bad
        if report["destination_account_hash"]:
            store.kb_add(conn, report["destination_account_hash"], "known_bad",
                         f"validated via {report['report_id']}", actor="agent")

    store.save_validation(conn, {
        **validation,
        "reason_codes": json.dumps(validation["reason_codes"]),
        "corroborating_institutions": json.dumps(validation["corroborating_institutions"]),
        "alert_id": validation.get("alert_id"),
    })

    _publish(report["reporting_institution"],
             {"type": "validation", "report_id": report["report_id"],
              "status": validation["status"], "score": validation["validation_score"]})
    if alert:
        _publish(alert["receiving_institution"],
                 {"type": "alert", **{k: alert[k] for k in
                  ("alert_id", "reporting_institution", "amount", "currency",
                   "validation_score", "explanation")}})

    return {"report_id": report["report_id"], "risk_codes": describe(codes),
            "validation": validation, "alert": alert}


def _hash_ref(value: str) -> str:
    raw = str(value or "missing")
    return "sha256:" + hashlib.sha256(("kifaru-upload-salt:" + raw).encode()).hexdigest()[:20]


def _bool(value) -> bool:
    return str(value).strip().lower() in {"1", "true", "yes", "y"}


def _float(value, default=0.0) -> float:
    try:
        return float(str(value or "").replace(",", "").replace("KES", "").replace("USD", "").strip())
    except ValueError:
        return default


def _institution(value: str) -> str:
    return DISPLAY_TO_CODE.get(str(value or "").strip(), str(value or "").strip())


def _display_institution(value: str) -> str:
    return CODE_TO_DISPLAY.get(str(value or "").strip(), str(value or "").strip() or "External network")


def _codes_from_upload_row(row: dict) -> list[str]:
    if row.get("risk_codes"):
        return [c for c in str(row["risk_codes"]).replace(",", "|").split("|") if c]

    codes: list[str] = []
    amount = _float(row.get("amount"))
    if amount >= 250000:
        codes.append("BEN-450")
    if _float(row.get("transfers_5m")) >= 4 or _float(row.get("transfers_1h")) >= 8:
        codes.append("VEL-429")
    if _bool(row.get("ip_country_changed")) or _bool(row.get("vpn_proxy_tor")):
        codes.append("IP-404")
    if str(row.get("device_status", "")).lower() == "new_device" or _bool(row.get("new_device")):
        codes.append("IP-401")
    if _float(row.get("beneficiary_age_minutes"), 999999) <= 60:
        codes.extend(["VEL-430", "BEN-450"])
    if _bool(row.get("password_reset_within_1h")) and "ATO-461" not in codes:
        codes.append("ATO-461")
    if _bool(row.get("vendor_bank_change")) or _bool(row.get("bec_signal")):
        codes.append("BEN-450")

    return list(dict.fromkeys(codes or ["VEL-431"]))


def _payload_from_csv_row(row: dict) -> ReportIn:
    if "reporting_institution" in row:
        evidence = {
            "device_profile": row.get("evidence_device_profile", ""),
            "account_age_days": row.get("evidence_account_age_days", ""),
            "distinct_senders_7d": row.get("evidence_distinct_senders_7d", ""),
            "flow_through_ratio": row.get("evidence_flow_through_ratio", ""),
            "dwell_minutes": row.get("evidence_dwell_minutes", ""),
            "sim_swap_age_days": row.get("evidence_sim_swap_age_days", ""),
        }
        return ReportIn(
            reporting_institution=row["reporting_institution"],
            reporting_system=row.get("reporting_system", "AG Screener"),
            transaction_ref=row["transaction_ref"],
            transaction_timestamp=row.get("transaction_timestamp", utcnow()),
            subject_account_hash=row.get("subject_account_hash", ""),
            subject_customer_hash=row.get("subject_customer_hash", ""),
            destination_account_hash=row.get("destination_account_hash", ""),
            destination_msisdn_hash=row.get("destination_msisdn_hash", ""),
            destination_institution=row.get("destination_institution", ""),
            amount=_float(row.get("amount")),
            currency=row.get("currency", "KES"),
            channel=row.get("channel", ""),
            bank_risk_score=_float(row.get("bank_risk_score")),
            bank_threshold=_float(row.get("bank_threshold"), 0.5),
            risk_codes=_codes_from_upload_row(row),
            evidence={k: v for k, v in evidence.items() if v not in ("", None)},
            narrative=row.get("narrative", ""),
        )

    customer_ref = row.get("customer_ref") or row.get("customer") or row.get("subject_customer_hash") or "unknown"
    transaction_ref = row.get("transaction_id") or row.get("transaction_ref") or "uploaded-transaction"
    reporting = _institution(row.get("reporting_bank") or row.get("reporting_institution"))
    receiving = _institution(row.get("receiving_bank") or row.get("destination_institution"))
    evidence = {
        "is_new_device": str(str(row.get("device_status", "")).lower() == "new_device" or _bool(row.get("new_device"))).lower(),
        "is_new_beneficiary": str(_float(row.get("beneficiary_age_minutes"), 999999) <= 60).lower(),
        "sim_swap_age_days": "1" if _bool(row.get("sim_swap_signal")) else "",
        "distinct_senders_7d": row.get("transfers_1h", ""),
    }
    return ReportIn(
        reporting_institution=reporting,
        reporting_system=row.get("bank_flag_source", "AG Screener"),
        transaction_ref=transaction_ref,
        transaction_timestamp=row.get("transaction_timestamp", utcnow()),
        subject_account_hash=_hash_ref(customer_ref),
        subject_customer_hash=_hash_ref(customer_ref),
        destination_account_hash=_hash_ref(row.get("destination_account") or f"{receiving}:{transaction_ref}"),
        destination_msisdn_hash="",
        destination_institution=receiving,
        amount=_float(row.get("amount")),
        currency=row.get("currency") or ("USD" if "USD" in str(row.get("amount", "")).upper() else "KES"),
        channel=row.get("channel") or row.get("payment_rail", ""),
        bank_risk_score=_float(row.get("bank_risk_score"), 0.8),
        bank_threshold=_float(row.get("bank_threshold"), 0.5),
        risk_codes=_codes_from_upload_row(row),
        evidence={k: v for k, v in evidence.items() if v not in ("", None)},
        narrative=row.get("narrative", f"{reporting} submitted {transaction_ref} from CSV upload."),
    )


def _status_for_dashboard(status: str) -> str:
    return {
        Status.VALIDATED_FRAUD.value: "validated_fraud",
        Status.NOT_FRAUD.value: "not_fraud",
        Status.INSUFFICIENT_EVIDENCE.value: "needs_review",
    }.get(status, "needs_review")


def _dashboard_validation(result: dict, payload: ReportIn) -> dict:
    validation = result["validation"]
    risk_codes = result["risk_codes"]
    return {
        "status": _status_for_dashboard(validation["status"]),
        "confidence": round(validation["validation_score"] * 100),
        "validated_by": "Kifaru agent",
        "bank_flag_source": payload.reporting_system,
        "reporting_bank": _display_institution(payload.reporting_institution),
        "receiving_bank": _display_institution(payload.destination_institution),
        "transaction_id": payload.transaction_ref,
        "customer_ref": "*" + payload.subject_customer_hash[-4:],
        "amount": f"{payload.currency} {payload.amount:,.0f}",
        "currency": payload.currency,
        "risk_codes": [{"code": item["code"], "label": item["name"], "evidence": item["family"]} for item in risk_codes],
        "key_signals": validation["reason_codes"],
        "destination_hash": payload.destination_account_hash or payload.destination_msisdn_hash,
        "corroborating_institutions": validation["corroborating_institutions"],
        "corroboration_count": validation["corroboration_count"],
        "missing_fields": [],
        "recommended_action": (
            f"Send fraud alert to {_display_institution(payload.destination_institution)}."
            if validation["status"] == Status.VALIDATED_FRAUD.value
            else "No receiving-bank alert; keep result in history."
        ),
        "human_review_required": validation["status"] == Status.INSUFFICIENT_EVIDENCE.value,
        "short_explanation": validation.get("explanation", ""),
        "created_at": validation["validated_at"],
    }


def _summary_for_dashboard(validations: list[dict]) -> dict:
    counts = {}
    for validation in validations:
        for risk_code in validation["risk_codes"]:
            counts[risk_code["code"]] = counts.get(risk_code["code"], 0) + 1
    return {
        "total_rows": len(validations),
        "validated_fraud": sum(1 for v in validations if v["status"] == "validated_fraud"),
        "not_fraud": sum(1 for v in validations if v["status"] == "not_fraud"),
        "needs_review": sum(1 for v in validations if v["status"] == "needs_review"),
        "top_risk_codes": [
            {"code": code, "count": count}
            for code, count in sorted(counts.items(), key=lambda item: item[1], reverse=True)
        ],
    }


async def _process_csv_upload(request: Request, channel: Channel = Channel.batch) -> dict:
    body = (await request.body()).decode("utf-8-sig")
    rows = list(csv.DictReader(io.StringIO(body)))
    if not rows:
        raise HTTPException(400, "CSV must include headers and at least one data row")
    validations = []
    errors = []
    for index, row in enumerate(rows):
        try:
            payload = _payload_from_csv_row(row)
            validations.append(_dashboard_validation(process(payload, channel), payload))
        except HTTPException as exc:
            errors.append({"index": index, "error": exc.detail})
    return {
        "summary": _summary_for_dashboard(validations),
        "validations": validations,
        "errors": errors,
    }


# =====================================================================
# ingestion - four channels, one path
# =====================================================================
@app.post("/v1/reports", tags=["ingest"])
def submit_report(payload: ReportIn):
    return process(payload, Channel.rest)


@app.post("/v1/reports/batch", tags=["ingest"])
def submit_batch(payloads: list[ReportIn]):
    out, errs = [], []
    for i, p in enumerate(payloads):
        try:
            out.append(process(p, Channel.batch))
        except HTTPException as e:
            errs.append({"index": i, "error": e.detail})
    return {"accepted": len(out), "rejected": len(errs), "errors": errs, "results": out}


@app.post("/v1/reports/csv", tags=["ingest"])
async def submit_csv(request: Request):
    return await _process_csv_upload(request, Channel.batch)


@app.post("/validate-csv", tags=["ingest"], include_in_schema=False)
async def submit_csv_compat(request: Request):
    return await _process_csv_upload(request, Channel.batch)


@app.post("/v1/hooks/{source}", tags=["ingest"])
def webhook(source: str, payload: ReportIn):
    ch = Channel.soc_connector if source in ("sentinel", "soc") else Channel.webhook
    return process(payload, ch)


# =====================================================================
# platform reads - what the web app ingests
# =====================================================================
@app.get("/v1/standard", tags=["platform"])
def standard():
    return STANDARD


@app.get("/v1/alerts", tags=["platform"])
def alerts(institution: str = Query(...), limit: int = 200):
    return {"institution": institution, "alerts": store.alerts_for(conn, institution, limit)}


@app.get("/v1/reports", tags=["platform"])
def reports(institution: str = Query(...), limit: int = 500):
    return {"institution": institution, "reports": store.reports_by(conn, institution, limit)}


@app.get("/v1/history", tags=["platform"])
def history(institution: str = Query(...), limit: int = 500):
    return {"institution": institution, "history": store.history_for(conn, institution, limit)}


@app.get("/v1/validations/{report_id}", tags=["platform"])
def validation_detail(report_id: str):
    r = conn.execute("SELECT * FROM validations WHERE report_id=?", (report_id,)).fetchone()
    if not r:
        raise HTTPException(404, "no validation for that report")
    return dict(r)


@app.post("/v1/alerts/{alert_id}/state", tags=["platform"])
def set_alert_state(alert_id: str, state: AlertState):
    conn.execute("UPDATE alerts SET state=? WHERE alert_id=?", (state.value, alert_id))
    store.audit(conn, "analyst", "alert.state", alert_id, state.value)
    conn.commit()
    return {"alert_id": alert_id, "state": state.value}


@app.get("/v1/stats", tags=["platform"])
def stats():
    return store.stats(conn)


@app.get("/v1/institutions", tags=["platform"])
def institutions():
    return [dict(r) for r in conn.execute("SELECT * FROM institutions")]


# =====================================================================
# admin controls
# =====================================================================
@app.get("/v1/admin/config", tags=["admin"])
def get_cfg():
    return store.get_config(conn)


@app.patch("/v1/admin/config", tags=["admin"])
def patch_cfg(p: ConfigPatch):
    for k in ("validated_threshold", "insufficient_threshold", "enabled_sources"):
        v = getattr(p, k)
        if v is not None:
            store.set_config(conn, k, v)
    if p.institution_thresholds:
        for code, th in p.institution_thresholds.items():
            conn.execute("UPDATE institutions SET threshold=? WHERE code=?", (th, code))
        conn.commit()
    return store.get_config(conn)


@app.get("/v1/admin/kb", tags=["admin"])
def get_kb(list_name: Optional[str] = None):
    q = "SELECT * FROM knowledge_base" + (" WHERE list_name=?" if list_name else "")
    return [dict(r) for r in (conn.execute(q, (list_name,)) if list_name else conn.execute(q))]


@app.post("/v1/admin/kb", tags=["admin"])
def post_kb(e: KBEntry):
    store.kb_add(conn, e.artefact_hash, e.list_name, e.label, e.added_by)
    return {"ok": True, **e.model_dump()}


@app.delete("/v1/admin/kb", tags=["admin"])
def delete_kb(artefact_hash: str, list_name: str):
    store.kb_remove(conn, artefact_hash, list_name)
    return {"ok": True}


@app.post("/v1/admin/revalidate", tags=["admin"])
def revalidate(report_id: str):
    """Re-run the agent against current config/KB. Proves threshold changes are real."""
    r = conn.execute("SELECT * FROM reports WHERE report_id=?", (report_id,)).fetchone()
    if not r:
        raise HTTPException(404, "unknown report")
    rep = dict(r)
    rep["risk_codes"] = json.loads(rep["risk_codes"])
    rep["evidence"] = json.loads(rep["evidence"])
    v = score_report(conn, rep, rep["risk_codes"])
    v["explanation"] = explain(rep, v, rep["risk_codes"])
    store.save_validation(conn, {**v, "reason_codes": json.dumps(v["reason_codes"]),
                                 "corroborating_institutions": json.dumps(v["corroborating_institutions"]),
                                 "alert_id": None})
    return v


@app.get("/v1/admin/audit", tags=["admin"])
def audit(limit: int = 100):
    return [dict(r) for r in conn.execute(
        "SELECT * FROM audit_log ORDER BY id DESC LIMIT ?", (limit,))]


# =====================================================================
# live stream
# =====================================================================
@app.get("/v1/stream/{institution}", tags=["platform"])
async def stream(institution: str, request: Request):
    q: asyncio.Queue = asyncio.Queue(maxsize=500)
    _subscribers.setdefault(institution, []).append(q)

    async def gen():
        try:
            yield f"data: {json.dumps({'type':'connected','institution':institution})}\n\n"
            while True:
                if await request.is_disconnected():
                    break
                try:
                    item = await asyncio.wait_for(q.get(), timeout=15)
                    yield f"data: {json.dumps(item)}\n\n"
                except asyncio.TimeoutError:
                    yield ": keepalive\n\n"
        finally:
            _subscribers[institution].remove(q)

    return StreamingResponse(gen(), media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})


@app.get("/health", tags=["platform"])
def health():
    return {"status": "ok", "agent": AGENT_VERSION, "standard": STANDARD["version"],
            "db": str(store.DB_PATH), **store.stats(conn)}


@app.get("/", include_in_schema=False)
def root():
    idx = Path(__file__).parent.parent / "ui" / "index.html"
    return FileResponse(idx) if idx.exists() else JSONResponse(
        {"service": "KIFARU", "docs": "/docs", "health": "/health"})
