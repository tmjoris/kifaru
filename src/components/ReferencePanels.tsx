import { useEffect, useState } from "react";
import { knowledgeBase } from "../data";
import type { Bank, KnowledgeBaseEntry, RiskCodeReference } from "../types";
import { Card, KnowledgeItems } from "./Shared";

export function KnowledgeBase({ bank, entries, riskCodes }: {
  bank: Bank; entries: KnowledgeBaseEntry[]; riskCodes: RiskCodeReference[];
}) {
  const backendItems = entries.map((entry) => ({
    title: `${entry.listName.replaceAll("_", " ")} - ${entry.label || entry.artefactHash.slice(-12)}`,
    text: `${entry.artefactHash} - added by ${entry.addedBy || "unknown"}${entry.addedAt ? ` on ${entry.addedAt}` : ""}`,
  }));
  return <div className="grid kb-layout">
    <Card title="Backend knowledge base" subtitle={`${bank.name} can view the known-good and known-bad artefacts used by the validation agent.`}
      actions={<span className="pill clear">Live backend data</span>}>
      {backendItems.length
        ? <KnowledgeItems items={backendItems} />
        : <p className="muted">No knowledge-base entries are currently stored.</p>}
    </Card>
    <div className="stack">
      <Card title="Detection playbooks" subtitle="Analyst guidance connected to model evidence."><KnowledgeItems items={knowledgeBase.playbooks} /></Card>
      <Card title="Model reference sources" subtitle="Trusted inputs the AI agent can cite during investigation."><KnowledgeItems items={knowledgeBase.sources} /></Card>
      <Card title="Central fraud standard" subtitle="Risk codes loaded from the backend validation standard.">
        <KnowledgeItems items={riskCodes.map((item) => ({ title: `${item.code} - ${item.label}`, text: item.text }))} />
      </Card>
    </div>
  </div>;
}

export function AdminDetails({ bank, onThresholdChange, notify }: {
  bank: Bank; onThresholdChange: (threshold: number) => Promise<void>; notify: (message: string) => void;
}) {  const [threshold, setThreshold] = useState(bank.threshold);
  const [saving, setSaving] = useState(false);

  useEffect(() => setThreshold(bank.threshold), [bank.id, bank.threshold]);

  async function saveThreshold() {
    if (!Number.isFinite(threshold)) {
      notify("Enter a threshold between 50 and 99.");
      return;
    }
    const nextThreshold = Math.max(50, Math.min(99, Math.round(threshold)));
    setSaving(true);
    try {
      await onThresholdChange(nextThreshold);
      setThreshold(nextThreshold);
      notify(`${bank.name} backend threshold updated to ${nextThreshold}%.`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Threshold update failed.");
    } finally {
      setSaving(false);
    }
  }
  return <div className="grid admin-grid">
    <Card className="wide-card" title="Input source connections"
      subtitle={`${bank.name} connector inventory. Transaction and validation data below is loaded from the backend.`}>
      <div className="source-grid">{bank.inputSources.map((source) => <div className="source-item" key={source.name}>
        <div><strong>{source.name}</strong><p className="card-subtitle">{source.type} - {source.method}</p></div>
        <span className={`pill ${source.status === "Connected" ? "clear" : "review"}`}>{source.status}</span>
        <p className="muted">Cadence: {source.cadence}</p>
      </div>)}</div>
    </Card>
    <Card title="SOC connection details" subtitle={`${bank.name} illustrative connector status.`}>
      <KnowledgeItems items={[
        { title: "Connector status", text: bank.health === "healthy" ? "Live and healthy (demo)" : "Live with warning (demo)" },
        { title: "Endpoint", text: bank.connector.endpoint },
        { title: "Connected systems", text: bank.connector.systems },
        { title: "Stream latency", text: bank.connector.latency },
        { title: "Last sync (sample)", text: bank.connector.lastSync },
      ]} />
    </Card>
    <Card title="Tenant controls" subtitle="Threshold updates are persisted by the backend.">
      <div className="kb-list"><div className="kb-item">
        <strong>Kifaru validation threshold</strong>
        <div className="threshold-control">
          <input aria-label="Kifaru validation threshold slider" type="range" min="50" max="99" value={threshold} onChange={(event) => setThreshold(Number(event.target.value))} />
          <input className="input" aria-label="Kifaru validation threshold percentage" type="number" min="50" max="99" value={threshold} onChange={(event) => setThreshold(Number(event.target.value))} />
          <button className="btn primary" disabled={saving || threshold === bank.threshold} onClick={() => void saveThreshold()}>
            {saving ? "Saving..." : "Save threshold"}
          </button>
          <span className="muted">Backend threshold: {bank.threshold}% confidence</span>
        </div>
      </div></div>
      <KnowledgeItems items={[
        { title: "Demo structure", text: "The 3-stage walkthrough governs which institution is acting: reporting bank, the neutral Kifaru exchange, then the receiving bank." },
        { title: "Tenant data boundary", text: `${bank.name} views only related records. This client-side filter is not production authorization.` },
        { title: "Alert routing", text: "Validated fraud appears for the receiving bank. Not-fraud outcomes stay in history." },
      ]} />
    </Card>
  </div>;
}
