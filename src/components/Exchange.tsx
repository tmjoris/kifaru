import { useEffect, useMemo, useState } from "react";
import { maskedFingerprint, reportingBankId } from "../domain";
import { riskCodeInfo } from "../explain";
import type { Transaction } from "../types";
import { Card, Icon, Status } from "./Shared";

function matchDescription(transaction: Transaction) {
  const reasons = transaction.reasonCodes ?? [];
  const matches = [
    reasons.includes("CORRO:destination") ? "receiving account" : "",
    reasons.includes("CORRO:msisdn") ? "receiving phone number" : "",
    reasons.includes("CORRO:device_profile") ? "protected device" : "",
  ].filter(Boolean);
  return matches.length ? matches.join(" and ") : "protected destination";
}

function outcomeCopy(transaction: Transaction) {
  if (transaction.alertOutcome === "held") return ["Review hold recorded", "The receiving institution chose the response"];
  if (transaction.alertOutcome === "released") return ["Released after review", "The signal was cleared and removed from corroboration"];
  if (transaction.alertOutcome === "recovered") return ["Recovery recorded", "The receiving institution recorded a synthetic recovery"];
  if (transaction.validationStatus === "quarantined") return ["Signal quarantined", "A dispute removed it from active corroboration"];
  if (transaction.validationStatus === "retracted") return ["Signal retracted", "The reporter withdrew the indicator"];
  if (transaction.validationStatus === "corroborated") return ["Alert delivered", "The receiving institution decides what happens next"];
  if (transaction.validationStatus === "below_threshold") return ["Stored below policy", "No receiving-institution alert was issued"];
  return ["Awaiting an independent match", "The signal remains visible without a receiving-institution alert"];
}

/**
 * Staff view of the synthetic protected-indicator exchange.
 */
export function Exchange({ transactions, bankName, onOpen }: {
  transactions: Transaction[]; bankName: (id: string) => string; onOpen: (key: string) => void;
}) {
  const fingerprints = transactions.filter((item) => item.destinationHash);
  const institutions = new Set(fingerprints.flatMap((item) => [item.sourceBank, item.destinationBank])
    .filter((id) => id !== "external" && !id.startsWith("unknown:")));
  const indicators = useMemo(() => {
    const groups = new Map<string, Transaction[]>();
    for (const transaction of transactions) {
      if (!transaction.destinationHash) continue;
      const key = `${transaction.destinationBank}:${transaction.destinationHash}`;
      groups.set(key, [...(groups.get(key) ?? []), transaction]);
    }
    return [...groups.entries()].map(([key, records]) => ({
      key,
      records,
      transaction: [...records].sort((a, b) =>
        b.corroborationCount - a.corroborationCount || b.score - a.score)[0],
    })).sort((a, b) =>
      b.transaction.corroborationCount - a.transaction.corroborationCount
      || b.transaction.score - a.transaction.score);
  }, [transactions]);
  const matched = indicators.filter((item) => item.transaction.corroborationCount > 0);
  const [focusKey, setFocusKey] = useState("");
  useEffect(() => {
    if (!indicators.length) {
      setFocusKey("");
      return;
    }
    if (!indicators.some((item) => item.key === focusKey)) setFocusKey(indicators[0].key);
  }, [focusKey, indicators]);
  const focusIndicator = indicators.find((item) => item.key === focusKey) ?? indicators[0];
  const focus = focusIndicator?.transaction;
  const sourceInstitutions = focusIndicator
    ? [...new Set(focusIndicator.records.flatMap((item) =>
      [reportingBankId(item), ...item.corroboratingInstitutions]))]
    : [];
  const outcome = focus ? outcomeCopy(focus) : ["No signal selected", ""];

  return <div className="stack">
    <Card title="Protected signal exchange" subtitle="Institution-attested indicators are matched and routed. Each receiving institution keeps decision authority.">
      <div className="grid metrics">
        <div className="metric"><div className="metric-label"><span>Signals shared</span></div>
          <div className="metric-value">{fingerprints.length}</div>
          <div className="metric-detail">Protected destination indicators on the exchange</div></div>
        <div className="metric"><div className="metric-label"><span>Cross-institution matches</span></div>
          <div className="metric-value">{matched.length}</div>
          <div className="metric-detail">Distinct protected indicators recognised by more than one institution</div></div>
        <div className="metric"><div className="metric-label"><span>Institutions represented</span></div>
          <div className="metric-value">{institutions.size}</div>
          <div className="metric-detail">Synthetic bank and mobile-money workspaces in current records</div></div>
      </div>
    </Card>
    <Card title="Signal relationship map"
      subtitle="A data-grounded view of who reported, what matched, where the beneficiary sits, and what the receiver recorded."
      className="signal-map-card"
      actions={focus && <button className="mini-btn" onClick={() => onOpen(focus.key)}>Open report</button>}>
      {focus ? <>
        <div className="signal-map-selectors" aria-label="Choose a protected indicator">
          {indicators.slice(0, 6).map((item) => <button key={item.key}
            className={item.key === focusIndicator.key ? "active" : ""}
            onClick={() => setFocusKey(item.key)}>
            <span className="mono">{maskedFingerprint(item.transaction.destinationHash)}</span>
            <small>{item.transaction.corroborationCount
              ? `${item.transaction.corroborationCount} match${item.transaction.corroborationCount === 1 ? "" : "es"}`
              : "waiting"}</small>
          </button>)}
        </div>
        <div className="signal-map-canvas">
          <div className="signal-map-sources">
            <span className="signal-map-label">Institution-attested reports</span>
            {sourceInstitutions.map((institution, index) =>
              <div className="signal-map-node source" key={institution}>
                <Icon name="account_balance" />
                <span><strong>{bankName(institution)}</strong>
                  <small>{index === 0 ? "Selected report" : "Qualified matching institution"}</small></span>
              </div>)}
          </div>
          <div className="signal-map-thread" aria-hidden="true"><i /><i /><i /></div>
          <div className="signal-map-node fingerprint">
            <Icon name="fingerprint" />
            <span><strong>{maskedFingerprint(focus.destinationHash)}</strong>
              <small>Matched on {matchDescription(focus)}</small></span>
          </div>
          <div className="signal-map-thread onward" aria-hidden="true"><i /></div>
          <div className="signal-map-node receiver">
            <Icon name="domain" />
            <span><strong>{bankName(focus.destinationBank)}</strong><small>Controls the receiving account</small></span>
          </div>
          <div className="signal-map-thread onward" aria-hidden="true"><i /></div>
          <div className={`signal-map-node outcome status-${focus.validationStatus}`}>
            <Icon name={focus.alertOutcome === "held" ? "pause_circle" : focus.validationStatus === "corroborated" ? "notification_important" : "schedule"} />
            <span><strong>{outcome[0]}</strong><small>{outcome[1]}</small></span>
          </div>
        </div>
        <p className="signal-map-note">This map links submitted artefacts, not human identity. All records shown are synthetic.</p>
      </> : <p className="stream-empty">No protected indicators are available yet.</p>}
    </Card>
    <Card title="Protected indicators" subtitle="Each card represents a receiving account as a protected code. Open one to inspect the evidence and policy result.">
      {indicators.length ? <div className="fingerprint-grid">{indicators.map(({ key, transaction: item }) =>
        <button key={key} className="fingerprint-card" onClick={() => onOpen(item.key)}>
        <div className="fingerprint-head">
          <span className="mono fp-hash">{maskedFingerprint(item.destinationHash)}</span>
          <Status status={item.validationStatus} />
        </div>
        <strong>{riskCodeInfo(item.riskCode.code, item.riskCode.label).title}</strong>
        <span className="muted">Reported by {bankName(reportingBankId(item))} &middot; Policy score {item.score}%</span>
        <div className="fingerprint-foot">
          {item.corroborationCount > 0
            ? <span className="pill fraud">Matched by {item.corroborationCount} institution{item.corroborationCount === 1 ? "" : "s"}</span>
            : <span className="pill review">Awaiting a match</span>}
        </div>
      </button>)}</div> : <p className="muted">No protected indicators have been published yet. Upload a CSV log or refresh backend data.</p>}
    </Card>
  </div>;
}
