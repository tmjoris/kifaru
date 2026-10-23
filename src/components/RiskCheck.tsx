import { type FormEvent, useState } from "react";
import { runRiskCheck } from "../api";
import { protectIdentifier } from "../identifiers";
import type { Bank, RiskCheckResult, RiskSignal } from "../types";
import { Card, Icon } from "./Shared";

const TIER_LABELS: Record<string, string> = {
  corroborated: "Corroborated report",
  single_source_review: "Single-source review",
  single_source_notice: "Single-source notice",
  known_risk: "On the known-risk list",
  reported: "Reported, awaiting corroboration",
};

/** Suggested next step for a flag. The institution still makes the decision. */
function guidance(signal: RiskSignal, subject: "payment" | "device") {
  if (!signal.flagged) {
    return "No active reports from other institutions. Your own fraud checks still apply.";
  }
  if (signal.tier === "corroborated" || signal.tier === "single_source_review") {
    return subject === "payment"
      ? "Hold the payment and contact the customer before releasing it."
      : "Step up authentication and review this session before any payment.";
  }
  if (signal.tier === "known_risk" || signal.tier === "single_source_notice") {
    return subject === "payment"
      ? "Warn the customer and confirm the payee before sending."
      : "Add friction, such as a confirmation call, before any payment.";
  }
  return "Proceed with caution: one report is still awaiting corroboration.";
}

function ResultTile({ title, subject, signal }: { title: string; subject: "payment" | "device"; signal: RiskSignal }) {
  if (!signal.checked) {
    return <div className="risk-result muted-result"><span className="risk-result-title">{title}</span>
      <p className="muted">Not checked.</p></div>;
  }
  const strong = signal.tier === "corroborated" || signal.tier === "single_source_review";
  return <div className={`risk-result ${signal.flagged ? (strong ? "flagged" : "caution") : "clear"}`}>
    <span className="risk-result-title">{title}</span>
    <strong className="risk-result-verdict">
      <Icon name={signal.flagged ? "gpp_maybe" : "verified_user"} />
      {signal.flagged ? "Flagged" : "Not flagged"}
    </strong>
    {signal.flagged && <span className={`pill ${strong ? "fraud" : "review"}`}>{TIER_LABELS[signal.tier] || signal.tier}</span>}
    {signal.flagged && <p className="muted">Reported by {signal.institutions} other institution{signal.institutions === 1 ? "" : "s"}.</p>}
    <p>{guidance(signal, subject)}</p>
  </div>;
}

export function RiskCheck({ bank, banks }: { bank: Bank; banks: Bank[] }) {
  const destinations = banks.filter((item) => item.backendCode);
  const [account, setAccount] = useState("");
  const [destination, setDestination] = useState(
    () => destinations.find((item) => item.id !== bank.id)?.backendCode ?? "");
  const [device, setDevice] = useState("");
  const [runKey, setRunKey] = useState("");
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<RiskCheckResult | null>(null);

  async function check(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setChecking(true);
    setError("");
    try {
      const destinationAccountHash = account.trim() ? await protectIdentifier(account, runKey) : "";
      const deviceProfile = device.trim() ? await protectIdentifier(device, runKey) : "";
      setResult(await runRiskCheck(bank.backendCode, {
        deviceProfile, destinationAccountHash, destinationInstitution: destination,
      }));
    } catch (checkError) {
      setResult(null);
      setError(checkError instanceof Error ? checkError.message : "The risk check failed.");
    } finally {
      setChecking(false);
    }
  }

  return <Card className="wide-card risk-check-card" title="Pre-payment risk check"
    subtitle="Ask whether other institutions hold active reports on a beneficiary account or a device before you act."
    actions={<span className="pill clear"><Icon name="lock" className="btn-icon" />Hashed in this browser</span>}>
    <div className="risk-check-grid">
      <form className="risk-check-form" onSubmit={(event) => void check(event)}>
        <label htmlFor="riskAccount">Beneficiary account number</label>
        <input id="riskAccount" className="input" value={account} autoComplete="off"
          onChange={(event) => setAccount(event.target.value)} placeholder="0114417" />
        <label htmlFor="riskDestination">Beneficiary institution</label>
        <select id="riskDestination" className="select" value={destination}
          onChange={(event) => setDestination(event.target.value)}>
          {destinations.map((item) => <option key={item.id} value={item.backendCode}>{item.name}</option>)}
        </select>
        <label htmlFor="riskDevice">Device ID</label>
        <input id="riskDevice" className="input" value={device} autoComplete="off"
          onChange={(event) => setDevice(event.target.value)} placeholder="browser-1a2b3c4d" />
        <label htmlFor="riskRunKey">Demo run key <span className="muted">(optional)</span></label>
        <input id="riskRunKey" className="input" value={runKey} autoComplete="off"
          onChange={(event) => setRunKey(event.target.value)} placeholder="Shown on each demo bank's operations page" />
        <p className="field-help">Identifiers are hashed here before they leave the browser. The demo banks
          prefix them with a run key, so enter it to match their reports.</p>
        <button className="btn primary" type="submit" disabled={checking || (!account.trim() && !device.trim())}>
          <Icon name="policy" className="btn-icon" />{checking ? "Checking..." : "Run risk check"}
        </button>
        {error && <p className="risk-check-error" role="alert">{error}</p>}
      </form>
      <div className="risk-check-results" aria-live="polite">
        {result
          ? <>
            <ResultTile title="Beneficiary account" subject="payment" signal={result.destination} />
            <ResultTile title="Device" subject="device" signal={result.device} />
            <p className="field-help">Kifaru answers only whether an identifier is flagged, the strongest tier
              and how many institutions reported it. It never says who reported it or why, and every check is
              audited. Your institution decides whether to warn, delay or block.</p>
          </>
          : <div className="risk-result muted-result"><span className="risk-result-title">No check yet</span>
            <p className="muted">Enter a beneficiary account, a device ID or both, then run the check.</p></div>}
      </div>
    </div>
  </Card>;
}
