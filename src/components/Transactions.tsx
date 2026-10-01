import { useEffect, useRef, type KeyboardEvent } from "react";
import {
  campaignChain, counterpartyBankId, displayTransactionId, maskedFingerprint, moneyDirection, pipelineSteps,
  reportingBankId, transactionDirection,
} from "../domain";
import { ALERT_STATE_HELP, STATUS_HELP, describeEvidence, explainReasons, riskCodeInfo } from "../explain";
import type { Bank, Transaction } from "../types";
import { CampaignChain, Card, Icon, Pipeline, Status } from "./Shared";

function matchesLabel(count: number) {
  return count === 1 ? "1 institution" : `${count} institutions`;
}

export function TransactionTable({ records, bank, bankName, bankRef, history, title, subtitle, onOpen }: {
  records: Transaction[]; bank: Bank; bankName: (id: string) => string; bankRef: (id: string) => string;
  history?: boolean; title: string; subtitle: string; onOpen: (key: string) => void;
}) {
  const headings = history
    ? ["Report ID", "Role", "Reported by", "Customer ref", "Amount", "Main risk", "Matched by", "Status", ""]
    : ["Report ID", "Reported by", "Direction", "Customer ref", "Amount", "Score", "Main risk", "Matched by", "Status", "Checked by", ""];
  const openOnKey = (event: KeyboardEvent<HTMLTableRowElement>, key: string) => {
    if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    onOpen(key);
  };
  return <Card title={title} subtitle={`${subtitle} Click any row to see why Kifaru reached its result.`} className="card-flat"><div className="table-wrap">
    <table className="transaction-table"><thead><tr>{headings.map((heading, index) => <th key={heading || index} scope="col"
      className={heading === "Customer ref" ? "table-secondary"
        : heading === "Checked by" ? "table-provenance"
          : heading === "Direction" || heading === "Role" ? "table-direction" : undefined}>{heading}</th>)}</tr></thead>
      <tbody>{records.length ? records.map((transaction) => {
        const risk = riskCodeInfo(transaction.riskCode.code, transaction.riskCode.label);
        const reporter = reportingBankId(transaction);
        return <tr key={transaction.key} className="clickable-row" tabIndex={0}
          aria-label={`Open report ${displayTransactionId(transaction, bankRef(reporter))}`}
          onClick={() => onOpen(transaction.key)} onKeyDown={(event) => openOnKey(event, transaction.key)}>
          <td data-label="Report ID"><span className="mono">{displayTransactionId(transaction, bankRef(reporter))}</span><br /><span className="muted">{history ? "Kifaru validation record" : transaction.merchant}</span></td>
          {history && <td data-label="Role" className="table-direction">{transactionDirection(transaction, bank.id)}</td>}
          <td data-label="Reported by"><strong>{bankName(reporter)}</strong></td>
          {!history && <td data-label="Direction" className="table-direction"><span className="pill">{moneyDirection(transaction, bank.id)}</span></td>}
          <td data-label="Customer ref" className="table-secondary"><span className="mono">{transaction.customerRef}</span>{!history && <><br /><span className="muted">{transaction.country}</span></>}</td>
          <td data-label="Amount"><strong>{transaction.amount}</strong></td>
          {!history && <td data-label="Score" title="Kifaru's validation score"><strong>{transaction.score}%</strong></td>}
          <td data-label="Main risk" className="risk-cell" title={risk.detail}>
            <span className="risk-title">{risk.title}</span>
            <span className="code-pill">{transaction.riskCode.code}</span>
          </td>
          <td data-label="Matched by">{transaction.corroborationCount > 0
            ? <span className="pill fraud" title={`Reported independently by ${transaction.corroboratingInstitutions.map(bankName).join(", ")}`}>{matchesLabel(transaction.corroborationCount)}</span>
            : <span className="muted">No one yet</span>}</td>
          <td data-label="Status"><Status status={transaction.validationStatus} /></td>
          {!history && <td data-label="Checked by" className="table-provenance"><span className="pill">{transaction.flagSource}</span></td>}
          <td data-label="Details"><button className="mini-btn open-btn" tabIndex={-1} onClick={(event) => { event.stopPropagation(); onOpen(transaction.key); }}>
            Open<Icon name="chevron_right" /></button></td>
        </tr>;
      }) : <tr className="transaction-empty"><td colSpan={headings.length} className="muted">No {title.toLowerCase()} match this institution and the current filters.</td></tr>}</tbody>
    </table>
  </div></Card>;
}

export function Investigation({ transaction, viewerBankId, bankName, bankRef, onClose, onAlertAction }: {
  transaction: Transaction; viewerBankId: string | null;
  bankName: (id: string) => string; bankRef: (id: string) => string;
  onClose: () => void;
  onAlertAction?: (
    alertId: string,
    state: "acknowledged" | "actioned" | "disputed",
    comment?: string,
  ) => Promise<void>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const pressStartedOutside = useRef(false);
  useEffect(() => {
    const element = dialog.current!;
    const previousFocus = document.activeElement;
    element.showModal();
    return () => {
      element.close();
      if (previousFocus instanceof HTMLElement) previousFocus.focus();
    };
  }, []);
  const reporter = reportingBankId(transaction);
  const receiver = counterpartyBankId(transaction);
  const risk = riskCodeInfo(transaction.riskCode.code, transaction.riskCode.label);
  const reasons = explainReasons(transaction.reasonCodes ?? []);
  const evidence = describeEvidence(transaction.evidenceFields ?? {});
  const details = [
    ["Amount", transaction.amount],
    ["Reported by", `${bankName(reporter)}, using ${transaction.merchant}`],
    ["Money going to", bankName(receiver)],
    ...(viewerBankId ? [["For your institution", moneyDirection(transaction, viewerBankId) === "Incoming"
      ? "Money coming in" : moneyDirection(transaction, viewerBankId) === "Outgoing" ? "Money going out" : "Not your transaction"]] : []),
    ["Kifaru score", `${transaction.score}%`],
    ["Receiving account (protected code)", maskedFingerprint(transaction.destinationHash)],
    ["Customer (last characters of the protected code)", transaction.customerRef],
    ["Matched by", transaction.corroborationCount > 0
      ? `${matchesLabel(transaction.corroborationCount)}: ${transaction.corroboratingInstitutions.map(bankName).join(", ")}`
      : "No other institution yet"],
    ["What Kifaru recommends", transaction.action],
    ...(transaction.alertType ? [["Alert type", transaction.alertType === "advisory"
      ? "Advisory: no money has moved yet" : "Hold: stop the money until it is checked"]] : []),
    ...(transaction.alertState ? [["Alert status", ALERT_STATE_HELP[transaction.alertState] ?? transaction.alertState]] : []),
    ["Checked by", transaction.flagSource],
  ];
  const canAct = transaction.alertId && onAlertAction
    && viewerBankId === receiver
    && transaction.alertState !== "actioned"
    && transaction.alertState !== "disputed";
  return <dialog ref={dialog} className="drawer open" aria-labelledby="drawerTitle"
    onCancel={(event) => { event.preventDefault(); onClose(); }}
    onMouseDown={(event) => { pressStartedOutside.current = event.target === event.currentTarget; }}
    onClick={(event) => { if (pressStartedOutside.current && event.target === event.currentTarget) onClose(); }}>
    <div className="drawer-inner">
      <header className="drawer-head">
        <div className="drawer-title-row">
          <div>
            <p className="eyebrow">Transaction investigation</p>
            <h3 className="card-title" id="drawerTitle">{displayTransactionId(transaction, bankRef(reporter))}</h3>
          </div>
          <button type="button" className="drawer-close" onClick={onClose} aria-label="Close" title="Close (Esc)">
            <Icon name="close" />
          </button>
        </div>
        <div className="drawer-status"><Status status={transaction.validationStatus} />
          <span>{STATUS_HELP[transaction.validationStatus]}</span></div>
        {canAct && <div className="drawer-actions">
          {transaction.alertState === "sent" && <button className="btn primary" onClick={() =>
            void onAlertAction(transaction.alertId!, "acknowledged")}>Acknowledge</button>}
          {transaction.alertState === "acknowledged" && <button className="btn primary" onClick={() =>
            void onAlertAction(transaction.alertId!, "actioned")}>Mark actioned</button>}
          <button className="btn" onClick={() => {
            const comment = window.prompt("Why is this alert being disputed?");
            if (comment?.trim()) void onAlertAction(transaction.alertId!, "disputed", comment.trim());
          }}>Dispute</button>
        </div>}
      </header>
      <div className="drawer-body">
        <p className="sidebar-label">Main risk</p>
        <div className="risk-summary">
          <strong>{risk.title}</strong><span className="code-pill">{transaction.riskCode.code}</span>
          <p>{risk.detail}</p>
        </div>
        {reasons.length > 0 && <>
          <p className="sidebar-label">Why Kifaru reached this result</p>
          <ul className="reason-list">{reasons.map((reason) => <li key={reason.code}>
            <strong>{reason.title}</strong>{reason.detail && <span>{reason.detail}</span>}
            <span className="code-pill">{reason.code}</span>
          </li>)}</ul>
        </>}
        {evidence.length > 0 && <>
          <p className="sidebar-label">What the reporting institution saw</p>
          <ul className="evidence-list">{evidence.map((line) => <li key={line}>{line}</li>)}</ul>
        </>}
        <p className="sidebar-label">Kifaru pipeline</p>
        <Pipeline steps={pipelineSteps(transaction, bankName(reporter))} />
        <p className="sidebar-label">Fraud chain across institutions</p>
        <CampaignChain nodes={campaignChain(transaction, bankName)} />
        <p className="sidebar-label">Details</p>
        <div className="detail-list">{details.map(([label, value]) =>
          <div className="detail-row" key={label}><span>{label}</span><b>{value}</b></div>,
        )}</div>
        <p className="muted drawer-note">{canAct
          ? "Use the buttons at the top to record your institution's response. "
          : ""}Synthetic demo record. Press Esc or click outside this panel to close it.</p>
      </div>
    </div>
  </dialog>;
}
