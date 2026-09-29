import { useEffect, useRef } from "react";
import {
  campaignChain, counterpartyBankId, displayTransactionId, maskedFingerprint, moneyDirection, pipelineSteps,
  reportingBankId, transactionDirection, validationLabel,
} from "../domain";
import type { Bank, Transaction } from "../types";
import { CampaignChain, Card, Pipeline, Status } from "./Shared";

export function TransactionTable({ records, bank, bankName, history, title, subtitle, onOpen }: {
  records: Transaction[]; bank: Bank; bankName: (id: string) => string; history?: boolean;
  title: string; subtitle: string; onOpen: (key: string) => void;
}) {
  const headings = history
    ? ["Report ID", "Role", "Reporting bank", "Customer ref", "Amount", "Risk code", "Matches", "Status", "More"]
    : ["Report ID", "Reporting bank", "Direction", "Customer ref", "Amount", "Confidence", "Risk code", "Matches", "Status", "Validated by", "More"];
  return <Card title={title} subtitle={subtitle} className="card-flat"><div className="table-wrap">
    <table><thead><tr>{headings.map((heading) => <th key={heading} scope="col">{heading}</th>)}</tr></thead>
      <tbody>{records.length ? records.map((transaction) => <tr key={transaction.key} onClick={() => onOpen(transaction.key)}>
        <td><span className="mono">{displayTransactionId(transaction, bank.name)}</span><br /><span className="muted">{history ? "Kifaru validation record" : transaction.merchant}</span></td>
        {history && <td>{transactionDirection(transaction, bank.id)}</td>}
        <td><strong>{bankName(reportingBankId(transaction))}</strong></td>
        {!history && <td><span className="pill">{moneyDirection(transaction, bank.id)}</span></td>}
        <td><span className="mono">{transaction.customerRef}</span>{!history && <><br /><span className="muted">{transaction.country}</span></>}</td>
        <td><strong>{transaction.amount}</strong></td>
        {!history && <td><strong>{transaction.score}%</strong></td>}
        <td><span className="pill">{transaction.riskCode.code}</span></td>
        <td>{transaction.corroborationCount > 0
          ? <span className="pill fraud" title={transaction.corroboratingInstitutions.map(bankName).join(", ")}>{transaction.corroborationCount} inst.</span>
          : <span className="muted">none yet</span>}</td>
        <td><Status status={transaction.validationStatus} /></td>
        {!history && <td><span className="pill">{transaction.flagSource}</span></td>}
        <td><button className="mini-btn" onClick={(event) => { event.stopPropagation(); onOpen(transaction.key); }}>View more</button></td>
      </tr>) : <tr><td colSpan={headings.length} className="muted">No {title.toLowerCase()} match this bank and the current filters.</td></tr>}</tbody>
    </table>
  </div></Card>;
}

export function Investigation({ transaction, bank, bankName, onClose }: {
  transaction: Transaction; bank: Bank; bankName: (id: string) => string;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current!;
    const previousFocus = document.activeElement;
    element.showModal();
    return () => {
      element.close();
      if (previousFocus instanceof HTMLElement) previousFocus.focus();
    };
  }, []);
  const details = [
    ["Status", validationLabel(transaction.validationStatus)],
    ["Validated by", transaction.flagSource],
    ["Confidence", `${transaction.score}%`],
    ["Risk code", `${transaction.riskCode.code} - ${transaction.riskCode.label}`],
    ["Reporting bank", bankName(reportingBankId(transaction))],
    ["Receiving bank", bankName(counterpartyBankId(transaction))],
    ["Transaction direction", moneyDirection(transaction, bank.id)],
    ["Customer reference", transaction.customerRef],
    ["Transaction value", transaction.amount],
    ["Protected fingerprint", maskedFingerprint(transaction.destinationHash)],
    ["Cross-institution matches", transaction.corroborationCount > 0
      ? `${transaction.corroborationCount} - ${transaction.corroboratingInstitutions.map(bankName).join(", ")}`
      : "None reported yet"],
    ["Risk signals", transaction.evidence.join(" - ")],
    ["Action", transaction.action],
  ];
  return <dialog ref={dialog} className="drawer open" aria-labelledby="drawerTitle" onCancel={onClose}>
    <div className="drawer-inner">
      <div className="drawer-head"><p className="eyebrow">Transaction investigation</p>
        <h3 className="card-title" id="drawerTitle">{displayTransactionId(transaction, bank.name)} - {validationLabel(transaction.validationStatus)}</h3>
        <p className="card-subtitle">{transaction.customerRef} at {transaction.merchant}</p>
      </div>
      <div className="drawer-body">
        <p className="sidebar-label">Kifaru pipeline</p>
        <Pipeline steps={pipelineSteps(transaction)} />
        <p className="sidebar-label">Fraud chain across institutions</p>
        <CampaignChain nodes={campaignChain(transaction, bankName)} />
        <div className="detail-list">{details.map(([label, value]) =>
          <div className="detail-row" key={label}><span>{label}</span><b>{value}</b></div>,
        )}</div>
      </div>
      <div className="drawer-foot">
        <p className="muted">Read-only record loaded from the shared Kifaru backend.</p>
        <button className="btn" onClick={onClose} autoFocus>Close</button>
      </div>
    </div>
  </dialog>;
}
