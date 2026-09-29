import test from "node:test";
import assert from "node:assert/strict";
import {
  counterpartyBankId, displayTransactionId, isVisible, moneyDirection,
  reportingBankId, riskCounts, transactionFromValidation, validationLabel,
} from "./domain.ts";
import type { Transaction, Validation } from "./types.ts";

const transaction: Transaction = {
  key: "case-1", id: "TX-123", sourceBank: "ncba", destinationBank: "kcb",
  customerRef: "*1234", merchant: "Transfer", country: "Kenya", amount: "KES 600,000",
  score: 99, flagSource: "Kifaru agent", validationStatus: "validated_fraud",
  riskCode: { code: "DEV-403", label: "Device anomaly" },
  evidence: ["New device"], action: "Review",
  destinationHash: "sha256:abcd1234ef", corroboratingInstitutions: ["equity"], corroborationCount: 1,
};

test("bank visibility includes counterparties but excludes unrelated banks", () => {
  assert.equal(isVisible(transaction, "ncba"), true);
  assert.equal(isVisible(transaction, "kcb"), true);
  assert.equal(isVisible(transaction, "equity"), false);
  assert.equal(isVisible(transaction, "im"), false);
  assert.equal(reportingBankId(transaction), "ncba");
  assert.equal(counterpartyBankId(transaction), "kcb");
  assert.equal(moneyDirection(transaction, "ncba"), "Outgoing");
  assert.equal(moneyDirection(transaction, "kcb"), "Incoming");
});

test("external incoming transfers retain prototype reporting-bank rules", () => {
  const incoming = { ...transaction, sourceBank: "external" };
  assert.equal(reportingBankId(incoming), "kcb");
  assert.equal(counterpartyBankId(incoming), "external");
  assert.equal(isVisible(incoming, "ncba"), false);
});

test("report IDs and risk counts are scoped to the supplied records", () => {
  assert.equal(displayTransactionId(transaction, "KCB"), "KCB-123");
  assert.deepEqual(riskCounts([transaction, { ...transaction, key: "case-2" }]), [["DEV-403", 2]]);
  assert.deepEqual(riskCounts([]), []);
  assert.equal(validationLabel("needs_review"), "Under review");
});

test("API mapping preserves every outcome, untrusted text and unique row identity", () => {
  const payload: Validation = {
    transaction_id: "TX-123", reporting_bank: "NCBA", receiving_bank: "KCB",
    customer_ref: "*1234", amount: "USD 100", currency: "USD", confidence: 70,
    status: "needs_review", validated_by: "Kifaru agent", risk_codes: [],
    key_signals: [], short_explanation: "<b>Missing data</b>", recommended_action: "Review",
  };
  const mapped = transactionFromValidation(payload, (name) => name.toLowerCase(), "unique-row");
  assert.equal(mapped.key, "unique-row");
  assert.equal(mapped.validationStatus, "needs_review");
  assert.deepEqual(mapped.evidence, ["<b>Missing data</b>"]);
  assert.equal(mapped.riskCode.code, "GEN-400");
  assert.equal(mapped.sourceBank, "ncba");
  assert.equal(mapped.destinationBank, "kcb");
  assert.equal(mapped.amount, "USD 100");
  for (const status of ["validated_fraud", "not_fraud", "needs_review"] as const) {
    assert.equal(transactionFromValidation({ ...payload, status }, (name) => name, status).validationStatus, status);
  }
});
