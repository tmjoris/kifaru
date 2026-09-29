"""
KIFARU contract objects. FREEZE THIS FILE FIRST.

Four objects move through the platform:
  Report      what a bank submits from AG Screener
  Validation  what the agent decides
  Alert       what a receiving institution is told
  RiskCode    the central fraud standard
"""
from __future__ import annotations
from datetime import datetime, timezone
from enum import Enum
from typing import Optional, List, Dict, Any
from pydantic import BaseModel, Field, field_validator


def utcnow() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class Channel(str, Enum):
    soc_connector = "soc_connector"
    rest = "rest"
    webhook = "webhook"
    batch = "batch"


class Status(str, Enum):
    VALIDATED_FRAUD = "VALIDATED_FRAUD"
    INSUFFICIENT_EVIDENCE = "INSUFFICIENT_EVIDENCE"
    NOT_FRAUD = "NOT_FRAUD"


class AlertState(str, Enum):
    sent = "sent"
    acknowledged = "acknowledged"
    actioned = "actioned"
    disputed = "disputed"


class ReportIn(BaseModel):
    """Submitted by a bank. Identifiers MUST already be hashed by the bank."""
    reporting_institution: str
    reporting_system: str = "AG Screener"
    transaction_ref: str
    transaction_timestamp: str

    subject_account_hash: str = ""
    subject_customer_hash: str = ""
    destination_account_hash: str = ""
    destination_msisdn_hash: str = ""
    destination_institution: str = ""

    amount: float = 0.0
    currency: str = "KES"
    channel: str = ""

    bank_risk_score: float = 0.0
    bank_threshold: float = 0.5
    bank_rule_ids: List[str] = Field(default_factory=list)
    risk_codes: List[str] = Field(default_factory=list)

    evidence: Dict[str, Any] = Field(default_factory=dict)
    narrative: str = ""

    @field_validator("subject_account_hash", "subject_customer_hash",
                     "destination_account_hash", "destination_msisdn_hash")
    @classmethod
    def must_be_hashed(cls, v: str) -> str:
        """Hard gate. Cleartext identifiers are rejected at the door."""
        if v and not v.startswith("sha256:"):
            raise ValueError("identifiers must be submitted as 'sha256:...' — "
                             "KIFARU does not accept cleartext customer data")
        return v


class Report(ReportIn):
    report_id: str
    submitted_at: str
    submission_channel: Channel


class Validation(BaseModel):
    validation_id: str
    report_id: str
    validated_at: str
    agent_version: str
    validation_score: float
    status: Status
    reason_codes: List[str]
    corroborating_institutions: List[str]
    corroboration_count: int
    explanation: str = ""
    latency_ms: int = 0
    alert_id: Optional[str] = None


class Alert(BaseModel):
    alert_id: str
    validation_id: str
    report_id: str
    issued_at: str
    receiving_institution: str
    reporting_institution: str
    destination_account_hash: str
    destination_msisdn_hash: str
    amount: float
    currency: str
    risk_codes: List[str]
    validation_score: float
    validated_by: str = "KIFARU validation agent"
    explanation: str = ""
    state: AlertState = AlertState.sent


class ConfigPatch(BaseModel):
    validated_threshold: Optional[float] = None
    insufficient_threshold: Optional[float] = None
    enabled_sources: Optional[List[str]] = None
    institution_thresholds: Optional[Dict[str, float]] = None


class KBEntry(BaseModel):
    artefact_hash: str
    list_name: str            # "known_good" | "known_bad"
    label: str = ""
    added_by: str = "admin"
