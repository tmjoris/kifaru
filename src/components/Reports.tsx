import { counterpartyBankId, moneyDirection, reportingBankId, riskCounts } from "../domain";
import { riskCodeInfo } from "../explain";
import type { Bank, Transaction } from "../types";
import { Card } from "./Shared";

export function Reports({ records, bank, view, onView }: {
  records: Transaction[]; bank: Bank; view: string; onView: (view: string) => void;
}) {
  const submitted = records.filter((item) => reportingBankId(item) === bank.id);
  const received = records.filter((item) => counterpartyBankId(item) === bank.id && item.alertId).length;
  const corroborated = records.filter((item) => item.validationStatus === "corroborated");
  const belowPolicy = records.filter((item) => item.validationStatus === "below_threshold");
  const cards = [
    { view: "all", label: "All reports", value: records.length, detail: "Every shared-signal record tied to this bank" },
    { view: "corroborated", label: "Corroborated signals", value: corroborated.length, detail: "Signals with a qualified independent match" },
    { view: "below_threshold", label: "Below alert policy", value: belowPolicy.length, detail: "Signals retained without a receiving alert" },
    { view: "submitted", label: "Submitted signals", value: submitted.length, detail: `${received} alerts received` },
  ];
  let title = "All reports";
  let subtitle = "Aggregate shared-signal numbers involving this bank.";
  let metrics: [string, number, string][] = [
    ["All reports", records.length, "All shared-signal records tied to this bank"],
    ["Alerts received", received, "Corroborated alerts received by this bank"],
    ["Submitted signals", submitted.length, "Reports this bank sent to Kifaru"],
    ["Corroborated signals", corroborated.length, "Signals with an eligible independent institution match"],
    ["Below alert policy", belowPolicy.length, "Signals retained without a receiving-institution alert"],
    ["Incoming transactions", records.filter((item) => moneyDirection(item, bank.id) === "Incoming").length, "Records where funds entered this bank"],
    ["Outgoing transactions", records.filter((item) => moneyDirection(item, bank.id) === "Outgoing").length, "Records where funds left this bank"],
  ];
  if (view === "corroborated" || view === "below_threshold") {
    title = view === "corroborated" ? "Corroborated signals" : "Below alert policy";
    subtitle = `${title} grouped by main risk.`;
    metrics = riskCounts(view === "corroborated" ? corroborated : belowPolicy)
      .map(([code, count]) => [code, count, riskCodeInfo(code).title]);
  } else if (view === "submitted") {
    title = "Submitted signals";
    subtitle = "Aggregate numbers for reports sent by this bank's fraud system.";
    metrics = [
      ["Submitted signals", submitted.length, "Reports sent by this bank to Kifaru"],
      ["Incoming submitted", submitted.filter((item) => moneyDirection(item, bank.id) === "Incoming").length, "Submitted signals on incoming transactions"],
      ["Outgoing submitted", submitted.filter((item) => moneyDirection(item, bank.id) === "Outgoing").length, "Submitted signals on outgoing transactions"],
      ["Corroborated after submission", submitted.filter((item) => item.validationStatus === "corroborated").length, "Submitted signals independently matched"],
    ];
  } else if (view.startsWith("code:")) {
    const code = view.slice(5);
    const info = riskCodeInfo(code);
    const matching = records.filter((item) => item.riskCode.code === code);
    title = `${code}: ${info.title}`;
    subtitle = info.detail;
    metrics = [
      [code, matching.length, info.title],
      ["Corroborated", matching.filter((item) => item.validationStatus === "corroborated").length, "Independently matched signals for this risk code"],
      ["Below alert policy", matching.filter((item) => item.validationStatus === "below_threshold").length, "Signals retained without an alert"],
      ["Submitted signals", matching.filter((item) => reportingBankId(item) === bank.id).length, "Submitted by this bank with this risk code"],
    ];
  }
  return <div className="report-detail">
    <Card title="Bank reports" subtitle={`${bank.name} signal outcomes, receiver responses, and risk-code distribution.`}>
      <div className="grid report-dashboard">{cards.map((card) =>
        <button key={card.view} className={`report-card ${view === card.view ? "active" : ""}`} onClick={() => onView(card.view)} aria-pressed={view === card.view}>
          <span className="metric-label"><span>{card.label}</span><span>Details</span></span>
          <strong>{card.value}</strong><span className="metric-detail">{card.detail}</span>
        </button>,
      )}</div>
    </Card>
    <Card title={title} subtitle={subtitle} actions={<div className="control-row">
      {riskCounts(records).slice(0, 3).map(([code, count]) =>
        <button className="mini-btn" key={code} title={riskCodeInfo(code).title} onClick={() => onView(`code:${code}`)} aria-pressed={view === `code:${code}`}>{code} - {count}</button>,
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
