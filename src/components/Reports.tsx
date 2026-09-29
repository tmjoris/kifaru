import { counterpartyBankId, moneyDirection, reportingBankId, riskCounts } from "../domain";
import { riskCodeCatalog } from "../data";
import type { Bank, Transaction } from "../types";
import { Card } from "./Shared";

export function Reports({ records, bank, view, onView }: {
  records: Transaction[]; bank: Bank; view: string; onView: (view: string) => void;
}) {
  const submitted = records.filter((item) => reportingBankId(item) === bank.id);
  const received = records.filter((item) => counterpartyBankId(item) === bank.id && item.validationStatus === "validated_fraud").length;
  const fraud = records.filter((item) => item.validationStatus === "validated_fraud");
  const notFraud = records.filter((item) => item.validationStatus === "not_fraud");
  const cards = [
    { view: "all", label: "All reports", value: records.length, detail: "Every validation record tied to this bank" },
    { view: "validated_fraud", label: "Fraud blocked", value: fraud.length, detail: "Kifaru-confirmed fraud cases" },
    { view: "not_fraud", label: "False positives", value: notFraud.length, detail: "Bank flags Kifaru marked not fraud" },
    { view: "submitted", label: "Submitted flags", value: submitted.length, detail: `${received} validated alerts received` },
  ];
  let title = "All reports";
  let subtitle = "Aggregate validation numbers involving this bank.";
  let metrics: [string, number, string][] = [
    ["All reports", records.length, "All validation records tied to this bank"],
    ["Alerts received", received, "Validated fraud alerts received by this bank"],
    ["Submitted flags", submitted.length, "Reports this bank sent to Kifaru"],
    ["Fraud blocked", fraud.length, "Kifaru-confirmed fraud outcomes"],
    ["False positives", notFraud.length, "Bank flags Kifaru marked not fraud"],
    ["Incoming transactions", records.filter((item) => moneyDirection(item, bank.id) === "Incoming").length, "Records where funds entered this bank"],
    ["Outgoing transactions", records.filter((item) => moneyDirection(item, bank.id) === "Outgoing").length, "Records where funds left this bank"],
  ];
  if (view === "validated_fraud" || view === "not_fraud") {
    title = view === "validated_fraud" ? "Fraud blocked" : "False positives";
    subtitle = `${title} grouped by risk code.`;
    metrics = riskCounts(view === "validated_fraud" ? fraud : notFraud).map(([code, count]) => [code, count, title]);
  } else if (view === "submitted") {
    title = "Submitted flags";
    subtitle = "Aggregate numbers for reports sent by this bank's fraud system.";
    metrics = [
      ["Submitted flags", submitted.length, "Reports sent by this bank to Kifaru"],
      ["Incoming submitted", submitted.filter((item) => moneyDirection(item, bank.id) === "Incoming").length, "Submitted flags on incoming transactions"],
      ["Outgoing submitted", submitted.filter((item) => moneyDirection(item, bank.id) === "Outgoing").length, "Submitted flags on outgoing transactions"],
      ["Validated after submission", submitted.filter((item) => item.validationStatus === "validated_fraud").length, "Submitted flags Kifaru confirmed as fraud"],
    ];
  } else if (view.startsWith("code:")) {
    const code = view.slice(5);
    const catalog = riskCodeCatalog.find((item) => item.code === code);
    const matching = records.filter((item) => item.riskCode.code === code);
    title = `${code} reports`;
    subtitle = catalog?.text ?? "Risk-code aggregate validation numbers.";
    metrics = [
      [code, matching.length, catalog?.label ?? "Risk-code total"],
      ["Validated fraud", matching.filter((item) => item.validationStatus === "validated_fraud").length, "Confirmed fraud for this risk code"],
      ["False positives", matching.filter((item) => item.validationStatus === "not_fraud").length, "Marked not fraud for this risk code"],
      ["Submitted flags", matching.filter((item) => reportingBankId(item) === bank.id).length, "Submitted by this bank with this risk code"],
    ];
  }
  return <div className="report-detail">
    <Card title="Bank reports" subtitle={`${bank.name} validation outcomes, false positives, and risk-code distribution.`}>
      <div className="grid report-dashboard">{cards.map((card) =>
        <button key={card.view} className={`report-card ${view === card.view ? "active" : ""}`} onClick={() => onView(card.view)} aria-pressed={view === card.view}>
          <span className="metric-label"><span>{card.label}</span><span>Details</span></span>
          <strong>{card.value}</strong><span className="metric-detail">{card.detail}</span>
        </button>,
      )}</div>
    </Card>
    <Card title={title} subtitle={subtitle} actions={<div className="control-row">
      {riskCounts(records).slice(0, 3).map(([code, count]) =>
        <button className="mini-btn" key={code} onClick={() => onView(`code:${code}`)} aria-pressed={view === `code:${code}`}>{code} - {count}</button>,
      )}
    </div>}>
      <div className="table-wrap"><table>
        <thead><tr><th scope="col">Report metric</th><th scope="col">Count</th><th scope="col">Description</th></tr></thead>
        <tbody>{metrics.length ? metrics.map(([label, count, description]) =>
          <tr key={label}><td><strong>{label}</strong></td><td className="report-count">{count}</td><td className="muted">{description}</td></tr>,
        ) : <tr><td colSpan={3} className="muted">No numbers available for this report view.</td></tr>}</tbody>
      </table></div>
    </Card>
  </div>;
}
