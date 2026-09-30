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
    <table className="transaction-table"><thead><tr>{headings.map((heading) => <th key={heading} scope="col"
      className={heading === "Customer ref" ? "table-secondary"
        : heading === "Validated by" ? "table-provenance"
          : heading === "Direction" || heading === "Role" ? "table-direction" : undefined}>{heading}</th>)}</tr></thead>
      <tbody>{records.length ? records.map((transaction) => <tr key={transaction.key} onClick={() => onOpen(transaction.key)}>
        <td data-label="Report ID"><span className="mono">{displayTransactionId(transaction, bank.shortName)}</span><br /><span className="muted">{history ? "Kifaru validation record" : transaction.merchant}</span></td>
        {history && <td data-label="Role" className="table-direction">{transactionDirection(transaction, bank.id)}</td>}
        <td data-label="Reporting bank"><strong>{bankName(reportingBankId(transaction))}</strong></td>
        {!history && <td data-label="Direction" className="table-direction"><span className="pill">{moneyDirection(transaction, bank.id)}</span></td>}
        <td data-label="Customer ref" className="table-secondary"><span className="mono">{transaction.customerRef}</span>{!history && <><br /><span className="muted">{transaction.country}</span></>}</td>
        <td data-label="Amount"><strong>{transaction.amount}</strong></td>
        {!history && <td data-label="Confidence"><strong>{transaction.score}%</strong></td>}
        <td data-label="Risk code"><span className="pill">{transaction.riskCode.code}</span></td>
        <td data-label="Matches">{transaction.corroborationCount > 0
          ? <span className="pill fraud" title={transaction.corroboratingInstitutions.map(bankName).join(", ")}>{transaction.corroborationCount} inst.</span>
          : <span className="muted">none yet</span>}</td>
        <td data-label="Status"><Status status={transaction.validationStatus} /></td>
        {!history && <td data-label="Validated by" className="table-provenance"><span className="pill">{transaction.flagSource}</span></td>}
        <td data-label="More"><button className="mini-btn" onClick={(event) => { event.stopPropagation(); onOpen(transaction.key); }}>View more</button></td>
      </tr>) : <tr className="transaction-empty"><td colSpan={headings.length} className="muted">No {title.toLowerCase()} match this bank and the current filters.</td></tr>}</tbody>
    </table>
  </div></Card>;
}

export function Investigation({ transaction, bank, bankName, onClose, onAlertAction }: {
  transaction: Transaction; bank: Bank; bankName: (id: string) => string;
  onClose: () => void;
  onAlertAction?: (
    alertId: string,
    state: "acknowledged" | "actioned" | "disputed",
    comment?: string,
  ) => Promise<void>;
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
    ...(transaction.alertType ? [["Alert type", transaction.alertType === "advisory" ? "Advisory" : "Hold requested"]] : []),
    ...(transaction.alertState ? [["Alert state", transaction.alertState]] : []),
  ];
  const canAct = transaction.alertId && onAlertAction
    && counterpartyBankId(transaction) === bank.id
    && transaction.alertState !== "actioned"
    && transaction.alertState !== "disputed";
  return <dialog ref={dialog} className="drawer open" aria-labelledby="drawerTitle" onCancel={onClose}>
    <div className="drawer-inner">
      <div className="drawer-head"><p className="eyebrow">Transaction investigation</p>
        <h3 className="card-title" id="drawerTitle">{displayTransactionId(transaction, bank.shortName)} - {validationLabel(transaction.validationStatus)}</h3>
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
        {canAct && <div className="actions">
          {transaction.alertState === "sent" && <button className="btn" onClick={() =>
            void onAlertAction(transaction.alertId!, "acknowledged")}>Acknowledge</button>}
          {transaction.alertState === "acknowledged" && <button className="btn primary" onClick={() =>
            void onAlertAction(transaction.alertId!, "actioned")}>Mark actioned</button>}
          <button className="btn" onClick={() => {
            const comment = window.prompt("Why is this alert being disputed?");
            if (comment?.trim()) void onAlertAction(transaction.alertId!, "disputed", comment.trim());
          }}>Dispute</button>
        </div>}
        <p className="muted">{canAct
          ? "Record the institution's response to this alert."
          : "Record loaded from the shared Kifaru backend."}</p>
        <button className="btn" onClick={onClose} autoFocus>Close</button>
      </div>
    </div>
  </dialog>;
}
