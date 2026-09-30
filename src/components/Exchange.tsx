import { maskedFingerprint, reportingBankId } from "../domain";
import { riskCodeInfo } from "../explain";
import type { Transaction } from "../types";
import { Card, Status } from "./Shared";

/**
 * The KIFARU exchange: the ecosystem-wide feed of protected fraud fingerprints.
 * Deliberately shows every institution's shared indicators, not just the signed-in
 * bank's own records, because the whole point of KIFARU is that intelligence
 * discovered by one institution must be visible to every other participant.
 */
export function Exchange({ transactions, bankName, onOpen }: {
  transactions: Transaction[]; bankName: (id: string) => string; onOpen: (key: string) => void;
}) {
  const fingerprints = transactions.filter((item) => item.destinationHash);
  const matched = fingerprints.filter((item) => item.corroborationCount > 0);
  const institutions = new Set(fingerprints.flatMap((item) => [item.sourceBank, item.destinationBank])
    .filter((id) => id !== "external" && !id.startsWith("unknown:")));
  const sorted = [...fingerprints].sort((a, b) => b.corroborationCount - a.corroborationCount || b.score - a.score);

  return <div className="stack">
    <Card title="Fraud intelligence exchange" subtitle="Detect &rarr; Fingerprint &rarr; Share &rarr; Match &rarr; Act. Every institution below publishes protected indicators, never raw customer data.">
      <div className="grid metrics">
        <div className="metric"><div className="metric-label"><span>Fingerprints shared</span></div>
          <div className="metric-value">{fingerprints.length}</div>
          <div className="metric-detail">Protected destination indicators on the exchange</div></div>
        <div className="metric"><div className="metric-label"><span>Cross-institution matches</span></div>
          <div className="metric-value">{matched.length}</div>
          <div className="metric-detail">Fingerprints recognised by more than one institution</div></div>
        <div className="metric"><div className="metric-label"><span>Institutions participating</span></div>
          <div className="metric-value">{institutions.size}</div>
          <div className="metric-detail">Banks and mobile money providers publishing or matching indicators</div></div>
      </div>
    </Card>
    <Card title="Shared fingerprints" subtitle="Each card is one receiving account, turned into a protected code. Click a card to see why Kifaru flagged it.">
      {sorted.length ? <div className="fingerprint-grid">{sorted.map((item) => <button key={item.key} className="fingerprint-card" onClick={() => onOpen(item.key)}>
        <div className="fingerprint-head">
          <span className="mono fp-hash">{maskedFingerprint(item.destinationHash)}</span>
          <Status status={item.validationStatus} />
        </div>
        <strong>{riskCodeInfo(item.riskCode.code, item.riskCode.label).title}</strong>
        <span className="muted">Reported by {bankName(reportingBankId(item))} &middot; Kifaru score {item.score}%</span>
        <div className="fingerprint-foot">
          {item.corroborationCount > 0
            ? <span className="pill fraud">Matched by {item.corroborationCount} institution{item.corroborationCount === 1 ? "" : "s"}</span>
            : <span className="pill review">Awaiting a match</span>}
        </div>
      </button>)}</div> : <p className="muted">No fingerprints have been published yet. Upload a CSV log or refresh backend data.</p>}
    </Card>
  </div>;
}
