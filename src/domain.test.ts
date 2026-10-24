import test from "node:test";
import assert from "node:assert/strict";
import {
  campaignChain, counterpartyBankId, displayTransactionId, isAlerting, isVisible, moneyDirection,
  pipelineSteps, reportingBankId, riskCounts, statusClass, transactionFromValidation, validationLabel,
} from "./domain.ts";
import { parseCsv, protectIdentifier, protectIdentifiers, toCsv } from "./identifiers.ts";
import { initialBanks } from "./data.ts";
import type { Transaction, Validation } from "./types.ts";

const transaction: Transaction = {
  key: "case-1", id: "TX-123", sourceBank: "ncba", destinationBank: "kcb",
  customerRef: "*1234", merchant: "Transfer", country: "Kenya", amount: "KES 600,000",
  score: 99, flagSource: "Kifaru policy engine", validationStatus: "corroborated",
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
  assert.equal(validationLabel("needs_review"), "Awaiting corroboration");
});

test("API mapping preserves every outcome, untrusted text and unique row identity", () => {
  const payload: Validation = {
    transaction_id: "TX-123", reporting_bank: "NCBA", receiving_bank: "KCB",
    customer_ref: "*1234", amount: "USD 100", currency: "USD", confidence: 70,
    status: "needs_review", validated_by: "Kifaru policy engine", risk_codes: [],
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
  for (const status of ["corroborated", "below_threshold", "needs_review", "quarantined", "retracted"] as const) {
    assert.equal(transactionFromValidation({ ...payload, status }, (name) => name, status).validationStatus, status);
  }
});

test("alert outcomes remain decisions recorded by the receiving institution", () => {
  const advisory = { ...transaction, alertType: "advisory" as const, amount: "KES 0" };
  assert.equal(pipelineSteps(advisory).at(-1)?.detail, "Advisory delivered, no money moved yet");
  assert.equal(campaignChain(advisory, (id) => id).at(-1)?.label, "Advisory delivered");
  const held = { ...transaction, alertType: "review" as const, alertOutcome: "held" as const };
  assert.equal(pipelineSteps(held).at(-1)?.detail, "Receiving institution recorded a review hold");
  assert.equal(campaignChain(held, (id) => id).at(-1)?.label, "Held");
});

test("single-source alerts reach the receiver but stay distinct from corroborated ones", () => {
  for (const status of ["corroborated", "single_source_review", "single_source_notice"]) {
    assert.equal(isAlerting(status), true);
  }
  assert.equal(isAlerting("needs_review"), false);
  assert.equal(validationLabel("single_source_review"), "Single-source review");
  assert.equal(validationLabel("single_source_notice"), "Single-source notice");
  assert.equal(statusClass("single_source_review"), "fraud");
  assert.equal(statusClass("single_source_notice"), "review");
  const notice = {
    ...transaction, validationStatus: "single_source_notice" as const, alertTier: "single_source_notice" as const,
    corroboratingInstitutions: [], corroborationCount: 0,
  };
  const steps = pipelineSteps(notice);
  assert.equal(steps.find((step) => step.id === "match")?.state, "pending");
  assert.equal(steps.at(-1)?.detail, "Lower-priority notice delivered; receiver decision pending");
  assert.equal(campaignChain(notice, (id) => id).at(-1)?.label, "Notice delivered");
  const review = { ...notice, validationStatus: "single_source_review" as const, alertTier: "single_source_review" as const };
  assert.equal(pipelineSteps(review).at(-1)?.detail, "Single-source review delivered; receiver decision pending");
  assert.match(campaignChain(review, (id) => id).at(-1)?.detail ?? "", /one institution's evidence/);
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

test("the demo lists every licensed bank and the two largest mobile money providers once", () => {
  assert.equal(initialBanks.length, 40);
  assert.equal(initialBanks.filter((bank) => bank.kind === "bank").length, 37);
  assert.deepEqual(initialBanks.filter((bank) => bank.kind === "mortgage").map((bank) => bank.name), ["HFC"]);
  assert.deepEqual(initialBanks.filter((bank) => bank.kind === "psp").map((bank) => bank.name), ["M-Pesa", "Airtel Money"]);
  for (const key of ["id", "backendCode", "name", "shortName"] as const) {
    assert.equal(new Set(initialBanks.map((bank) => bank[key])).size, 40, `${key} values must be unique`);
  }
  const byCode = new Map(initialBanks.map((bank) => [bank.backendCode, bank.name]));
  assert.equal(byCode.get("bank_a"), "NCBA");
  assert.equal(byCode.get("bank_b"), "KCB");
  assert.equal(byCode.get("psp_c"), "Equity Bank");
  assert.equal(byCode.get("sacco_d"), "I&M Bank");
  for (const name of ["Co-op Bank", "Absa Bank Kenya", "Stanbic Bank Kenya", "DTB", "Standard Chartered Kenya",
    "Premier Bank Kenya", "National Bank of Kenya", "Kingdom Bank", "Victoria Commercial Bank"]) {
    assert.ok(initialBanks.some((bank) => bank.name === name), `${name} is missing`);
  }
  assert.ok(initialBanks.every((bank) => bank.soc.startsWith("Synthetic demo feed.")));
});

test("identifier columns are hashed in the browser before upload", async () => {
  const input = "reporting_bank,customer_ref,destination_msisdn,device_profile,amount\nBank A,Jane Wanjiku,0712 345 678,device-42,5000\n";
  const { csv, hashed } = await protectIdentifiers(input, "test-key");
  const [header, row] = parseCsv(csv);
  assert.equal(hashed, 3);
  assert.deepEqual(header, ["reporting_bank", "customer_ref", "destination_msisdn", "device_profile", "amount"]);
  assert.equal(row[0], "Bank A");
  assert.equal(row[4], "5000");
  assert.match(row[1], /^sha256:[0-9a-f]{64}$/);
  assert.match(row[2], /^sha256:[0-9a-f]{64}$/);
  assert.match(row[3], /^sha256:[0-9a-f]{64}$/);
  assert.equal(csv.includes("Jane"), false);
  assert.equal(csv.includes("0712"), false);
  assert.equal(csv.includes("device-42"), false);
  const again = await protectIdentifiers("customer_ref\njane wanjiku\n", "test-key");
  assert.equal(parseCsv(again.csv)[1][0], row[1], "the same normalized identifier must hash to the same value");
  const unchanged = await protectIdentifiers(csv, "test-key");
  assert.equal(unchanged.hashed, 0, "values that are already hashed are left alone");
});

test("single identifiers hash like the demo banks, with an optional run key", async () => {
  // Reference digests: HMAC-SHA256 over "<run key>:<normalized value>" with the
  // shared demo key, as demo-banks/kifaru.go protect() computes them.
  assert.equal(await protectIdentifier(" 011 4417 ", "abcd"),
    "sha256:ff3c0425cf5dfb97a95242bcb49038a2ad9641f1175e49ff645308c52f1c6f46");
  assert.equal(await protectIdentifier("Browser-1A2B"),
    "sha256:59ec62a3bb715c158fca08ca20fe1ef9f8056b22bd8a5afa5ccec9acc000bb9e");
});
