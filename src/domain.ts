import type { Transaction, Validation } from "./types.ts";
import { riskCodeInfo } from "./explain.ts";

export function reportingBankId(transaction: Transaction) {
  return transaction.sourceBank === "external" ? transaction.destinationBank : transaction.sourceBank;
}

export function counterpartyBankId(transaction: Transaction) {
  return reportingBankId(transaction) === transaction.sourceBank
    ? transaction.destinationBank
    : transaction.sourceBank;
}

export function isVisible(transaction: Transaction, bankId: string) {
  return reportingBankId(transaction) === bankId || counterpartyBankId(transaction) === bankId;
}

export function moneyDirection(transaction: Transaction, bankId: string) {
  if (transaction.sourceBank === bankId) return "Outgoing";
  if (transaction.destinationBank === bankId) return "Incoming";
  return "Network";
}

export function transactionDirection(transaction: Transaction, bankId: string) {
  return reportingBankId(transaction) === bankId ? "Submitted flag" : "Received alert";
}

export function displayTransactionId(transaction: Transaction, bankName: string) {
  return `${bankName.replace(/\s+/g, "")}-${transaction.id.split("-").at(-1)}`;
}

export function validationLabel(status: string) {
  if (status === "validated_fraud") return "Validated fraud";
  if (status === "not_fraud") return "Marked not fraud";
  return "Under review";
}

export function statusClass(status: string) {
  if (status === "validated_fraud") return "fraud";
  if (status === "not_fraud") return "clear";
  return "review";
}

export function riskCounts(records: Transaction[]) {
  const counts = new Map<string, number>();
  for (const record of records) {
    counts.set(record.riskCode.code, (counts.get(record.riskCode.code) ?? 0) + 1);
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1]);
}

export function transactionFromValidation(
  validation: Validation,
  bankIdFromName: (name: string) => string,
  key: string,
): Transaction {
  return {
    key,
    id: validation.transaction_id,
    sourceBank: bankIdFromName(validation.reporting_bank),
    destinationBank: bankIdFromName(validation.receiving_bank),
    customerRef: validation.customer_ref,
    merchant: "Uploaded CSV log",
    country: validation.currency,
    amount: validation.amount,
    score: validation.confidence,
    flagSource: validation.validated_by,
    validationStatus: validation.status,
    riskCode: validation.risk_codes[0] ?? { code: "GEN-400", label: "General fraud signal" },
    evidence: validation.key_signals.length ? validation.key_signals : [validation.short_explanation],
    action: validation.recommended_action,
    destinationHash: validation.destination_hash ?? "",
    corroboratingInstitutions: (validation.corroborating_institutions ?? []).map(bankIdFromName),
    corroborationCount: validation.corroboration_count ?? (validation.corroborating_institutions ?? []).length,
  };
}

/** Short, display-safe fragment of a protected hash (never the full artefact). */
export function maskedFingerprint(hash: string) {
  if (!hash) return "unavailable";
  const value = hash.includes(":") ? hash.split(":")[1] : hash;
  return `${hash.split(":")[0] ?? "hash"}:${value.slice(0, 4)}\u2026${value.slice(-4)}`;
}

export type PipelineStepState = "done" | "active" | "pending" | "cleared";
export interface PipelineStep {
  id: string;
  label: string;
  detail: string;
  state: PipelineStepState;
}

/** Detect -> Fingerprint -> Share -> Match -> Act: where this record sits in the KIFARU pipeline. */
export function pipelineSteps(transaction: Transaction, reporterName?: string): PipelineStep[] {
  const matched = transaction.corroborationCount > 0;
  const steps: PipelineStep[] = [
    { id: "detect", label: "Detect", detail: reporterName ? `Reported by ${reporterName}` : `Flagged by ${transaction.flagSource}`, state: "done" },
    { id: "fingerprint", label: "Fingerprint", detail: `Receiving account turned into a protected code, ${maskedFingerprint(transaction.destinationHash)}`, state: "done" },
    { id: "share", label: "Share", detail: "Published to the Kifaru exchange", state: "done" },
    {
      id: "match", label: "Match",
      detail: matched
        ? `Matched by ${transaction.corroborationCount} institution${transaction.corroborationCount === 1 ? "" : "s"}`
        : "No cross-institution match yet",
      state: matched ? "done" : "pending",
    },
  ];
  if (transaction.validationStatus === "validated_fraud") {
    steps.push({
      id: "act", label: "Act",
      detail: transaction.alertType === "advisory" ? "Advisory sent, no money moved yet" : "Alert sent, transaction held",
      state: "done",
    });
  } else if (transaction.validationStatus === "not_fraud") {
    steps.push({ id: "act", label: "Act", detail: "Cleared, no alert routed", state: "cleared" });
  } else {
    steps.push({ id: "act", label: "Act", detail: "Awaiting analyst decision", state: "active" });
  }
  return steps;
}

export interface ChainNode {
  label: string;
  kind: "bank" | "fingerprint" | "outcome";
  detail: string;
}

/** Visualizes the cross-institution money movement a single fingerprint reveals. */
export function campaignChain(transaction: Transaction, bankName: (id: string) => string): ChainNode[] {
  const chain: ChainNode[] = [
    { label: bankName(reportingBankId(transaction)), kind: "bank", detail: "Detected the pattern" },
    { label: maskedFingerprint(transaction.destinationHash), kind: "fingerprint", detail: riskCodeInfo(transaction.riskCode.code, transaction.riskCode.label).title },
    { label: bankName(counterpartyBankId(transaction)), kind: "bank", detail: transactionDirection(transaction, counterpartyBankId(transaction)) },
  ];
  if (transaction.validationStatus === "validated_fraud") {
    chain.push(transaction.alertType === "advisory"
      ? { label: "Watch the account", kind: "outcome", detail: "Advisory: verify the customer before any transfer" }
      : { label: "Held for review", kind: "outcome", detail: "Cash-out blocked pending investigation" });
  } else if (transaction.validationStatus === "not_fraud") {
    chain.push({ label: "Released", kind: "outcome", detail: "No cross-institution corroboration found" });
  } else {
    chain.push({ label: "Under review", kind: "outcome", detail: "Analyst decision pending" });
  }
  return chain;
}
