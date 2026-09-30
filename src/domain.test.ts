import test from "node:test";
import assert from "node:assert/strict";
import {
  campaignChain, counterpartyBankId, displayTransactionId, isVisible, moneyDirection,
  pipelineSteps, reportingBankId, resolveSession, riskCounts, transactionFromValidation, validationLabel,
} from "./domain.ts";
import { parseCsv, protectIdentifiers, toCsv } from "./identifiers.ts";
import type { Transaction, Validation } from "./types.ts";

const transaction: Transaction = {
  key: "case-1", id: "TX-123", sourceBank: "bank-a", destinationBank: "bank-b",
  customerRef: "*1234", merchant: "Transfer", country: "Kenya", amount: "KES 600,000",
  score: 99, flagSource: "Kifaru agent", validationStatus: "validated_fraud",
  riskCode: { code: "DEV-403", label: "Device anomaly" },
  evidence: ["New device"], action: "Review",
  destinationHash: "sha256:abcd1234ef", corroboratingInstitutions: ["psp-c"], corroborationCount: 1,
};

test("bank visibility includes counterparties but excludes unrelated banks", () => {
  assert.equal(isVisible(transaction, "bank-a"), true);
  assert.equal(isVisible(transaction, "bank-b"), true);
  assert.equal(isVisible(transaction, "psp-c"), false);
  assert.equal(isVisible(transaction, "sacco-d"), false);
  assert.equal(reportingBankId(transaction), "bank-a");
  assert.equal(counterpartyBankId(transaction), "bank-b");
  assert.equal(moneyDirection(transaction, "bank-a"), "Outgoing");
  assert.equal(moneyDirection(transaction, "bank-b"), "Incoming");
});

test("external incoming transfers retain prototype reporting-bank rules", () => {
  const incoming = { ...transaction, sourceBank: "external" };
  assert.equal(reportingBankId(incoming), "bank-b");
  assert.equal(counterpartyBankId(incoming), "external");
  assert.equal(isVisible(incoming, "bank-a"), false);
});

test("report IDs and risk counts are scoped to the supplied records", () => {
  assert.equal(displayTransactionId(transaction, "Bank B"), "BankB-123");
  assert.deepEqual(riskCounts([transaction, { ...transaction, key: "case-2" }]), [["DEV-403", 2]]);
  assert.deepEqual(riskCounts([]), []);
  assert.equal(validationLabel("needs_review"), "Under review");
});

test("API mapping preserves every outcome, untrusted text and unique row identity", () => {
  const payload: Validation = {
    transaction_id: "TX-123", reporting_bank: "Bank A", receiving_bank: "Bank B",
    customer_ref: "*1234", amount: "USD 100", currency: "USD", confidence: 70,
    status: "needs_review", validated_by: "Kifaru agent", risk_codes: [],
    key_signals: [], short_explanation: "<b>Missing data</b>", recommended_action: "Review",
  };
  const mapped = transactionFromValidation(payload, (name) => name.toLowerCase().replace(" ", "-"), "unique-row");
  assert.equal(mapped.key, "unique-row");
  assert.equal(mapped.validationStatus, "needs_review");
  assert.deepEqual(mapped.evidence, ["<b>Missing data</b>"]);
  assert.equal(mapped.riskCode.code, "GEN-400");
  assert.equal(mapped.sourceBank, "bank-a");
  assert.equal(mapped.destinationBank, "bank-b");
  assert.equal(mapped.amount, "USD 100");
  for (const status of ["validated_fraud", "not_fraud", "needs_review"] as const) {
    assert.equal(transactionFromValidation({ ...payload, status }, (name) => name, status).validationStatus, status);
  }
});

test("advisory alerts are not described as held transfers", () => {
  const advisory = { ...transaction, alertType: "advisory" as const, amount: "KES 0" };
  assert.equal(pipelineSteps(advisory).at(-1)?.detail, "Advisory sent, no money moved yet");
  assert.equal(campaignChain(advisory, (id) => id).at(-1)?.label, "Watch the account");
  assert.equal(pipelineSteps(transaction).at(-1)?.detail, "Alert sent, transaction held");
  assert.equal(campaignChain(transaction, (id) => id).at(-1)?.label, "Held for review");
});

test("CSV parsing keeps quoted commas and round-trips", () => {
  const rows = parseCsv('a,b\r\n"x, y","say ""hi"""\n');
  assert.deepEqual(rows, [["a", "b"], ["x, y", 'say "hi"']]);
  assert.deepEqual(parseCsv(toCsv(rows)), rows);
  assert.deepEqual(parseCsv('\uFEFFa,b\n"line one\nline two",2\n\n'), [["a", "b"], ["line one\nline two", "2"]]);
});

test("malformed CSV is refused before anything is sent", async () => {
  assert.throws(() => parseCsv('a,b\n5" TV,2\n'), /quote appears inside an unquoted field/);
  assert.throws(() => parseCsv('a,b\n"open,2\n3,4\n'), /never closed/);
  assert.throws(() => parseCsv('a,b\n"x"y,2\n'), /after a closing quote/);
  assert.throws(() => parseCsv("a,b\n1,2,3\n"), /has 3 fields; the header has 2/);
  const smuggled = "reporting_bank,customer_ref,destination_msisdn,amount,narrative\n"
    + 'Bank A,Alice Wanjiru,0711222333,500,Paid for 5" TV\n'
    + "Bank B,John Kamau,0722000111,100,Refund\n";
  await assert.rejects(() => protectIdentifiers(smuggled, "test-key"), /quote appears inside an unquoted field/);
});

test("saved sessions are upgraded or dropped when their institution is gone", () => {
  const ids = ["bank-a", "bank-b", "psp-c", "sacco-d"];
  assert.deepEqual(resolveSession({ scope: "institution", bankId: "equity" }, ids), { scope: "institution", bankId: "psp-c" });
  assert.deepEqual(resolveSession({ scope: "institution", bankId: "bank-b" }, ids), { scope: "institution", bankId: "bank-b" });
  assert.equal(resolveSession({ scope: "institution", bankId: "dir-absa-bank-kenya" }, ids), null);
  assert.deepEqual(resolveSession({ scope: "exchange", bankId: null }, ids), { scope: "exchange", bankId: null });
  assert.deepEqual(resolveSession({ stage: "kifaru", bankId: "ncba" }, ids), { scope: "exchange", bankId: null });
  assert.deepEqual(resolveSession({ stage: "bank", bankId: "im" }, ids), { scope: "institution", bankId: "sacco-d" });
  assert.equal(resolveSession(null, ids), null);
});

test("identifier columns are hashed in the browser before upload", async () => {
  const input = "reporting_bank,customer_ref,destination_msisdn,amount\nBank A,Jane Wanjiku,0712 345 678,5000\n";
  const { csv, hashed } = await protectIdentifiers(input, "test-key");
  const [header, row] = parseCsv(csv);
  assert.equal(hashed, 2);
  assert.deepEqual(header, ["reporting_bank", "customer_ref", "destination_msisdn", "amount"]);
  assert.equal(row[0], "Bank A");
  assert.equal(row[3], "5000");
  assert.match(row[1], /^sha256:[0-9a-f]{64}$/);
  assert.match(row[2], /^sha256:[0-9a-f]{64}$/);
  assert.equal(csv.includes("Jane"), false);
  assert.equal(csv.includes("0712"), false);
  const again = await protectIdentifiers("customer_ref\njane wanjiku\n", "test-key");
  assert.equal(parseCsv(again.csv)[1][0], row[1], "the same person must hash to the same value");
  const unchanged = await protectIdentifiers(csv, "test-key");
  assert.equal(unchanged.hashed, 0, "values that are already hashed are left alone");
});
