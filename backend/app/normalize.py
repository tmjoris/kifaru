"""
Normalize step: a bank's own rule ids become codes in the central fraud standard,
plus behavioural codes derived from the evidence block.

This is what makes reports from four different institutions comparable.
"""
import json
from pathlib import Path

STANDARD = json.loads((Path(__file__).parent.parent / "data" / "kifaru_risk_codes.json").read_text())
CODES = STANDARD["codes"]
RULE_MAP = STANDARD["bank_rule_mapping"]


class NormalizationError(ValueError):
    pass


def _f(ev, key, cast=float, default=None):
    v = ev.get(key, "")
    if v in ("", None):
        return default
    try:
        return cast(v)
    except (TypeError, ValueError):
        return default


def normalize(report: dict) -> list[str]:
    """Returns the ordered list of standard risk codes for this report."""
    codes: list[str] = []

    # 1. codes the bank already asserted, kept if they are in the standard
    for c in report.get("risk_codes") or []:
        if c in CODES and c not in codes:
            codes.append(c)

    # 2. map the bank's native rule ids onto the standard
    for rule in report.get("bank_rule_ids") or []:
        c = RULE_MAP.get(rule)
        if c and c not in codes:
            codes.append(c)

    # 3. derive behavioural codes from evidence the bank supplied
    ev = report.get("evidence") or {}
    ftr = _f(ev, "flow_through_ratio")
    dwell = _f(ev, "dwell_minutes", int)
    age = _f(ev, "account_age_days", int)
    senders = _f(ev, "distinct_senders_7d", int)
    sim = _f(ev, "sim_swap_age_days", int)

    if ftr is not None and ftr > 0.90 and "MUL-441" not in codes:
        codes.append("MUL-441")
    if dwell is not None and dwell < 10 and "MUL-442" not in codes:
        codes.append("MUL-442")
    if age is not None and senders is not None and age < 14 and senders >= 5 \
            and "MUL-440" not in codes:
        codes.append("MUL-440")
    if sim is not None and sim <= 3 and "IP-402" not in codes:
        codes.append("IP-402")
    if str(ev.get("is_new_device")).lower() == "true" and \
       str(ev.get("is_new_beneficiary")).lower() == "true" and "ATO-460" not in codes:
        codes.append("ATO-460")

    unknown = [c for c in codes if c not in CODES]
    if unknown:
        raise NormalizationError(f"codes not in standard {STANDARD['version']}: {unknown}")
    if not codes:
        raise NormalizationError("report produced no risk codes — nothing to validate")
    return codes


def describe(codes: list[str]) -> list[dict]:
    return [{"code": c, "family": CODES[c]["family"], "name": CODES[c]["name"]} for c in codes]
