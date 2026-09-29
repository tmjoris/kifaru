import { useCallback, useEffect, useRef, useState } from "react";
import { loadDashboardData, updateInstitutionThreshold, validateCsv } from "./api";
import { initialBanks, riskCodeCatalog } from "./data";
import {
  counterpartyBankId, displayTransactionId, isVisible, moneyDirection, reportingBankId,
  transactionDirection,
} from "./domain";
import type { KnowledgeBaseEntry, RiskCodeReference, Stage, Tab, Transaction, UploadSummary } from "./types";
import { BrandDots, Icon, Logo } from "./components/Shared";
import { Investigation, TransactionTable } from "./components/Transactions";
import { Reports } from "./components/Reports";
import { Exchange } from "./components/Exchange";
import { AdminDetails, KnowledgeBase } from "./components/ReferencePanels";

const STAGES: { id: Stage; step: string; label: string; blurb: string; icon: string; railLabel: string }[] = [
  { id: "reporting", step: "1", label: "Reporting bank", blurb: "Detects fraud, publishes a fingerprint", icon: "flag", railLabel: "Report" },
  { id: "kifaru", step: "2", label: "Kifaru exchange", blurb: "Shares it across institutions", icon: "hub", railLabel: "Exchange" },
  { id: "receiving", step: "3", label: "Receiving bank", blurb: "Matches it, holds the transfer", icon: "shield", railLabel: "Receive" },
];

export default function App() {
  const [banks, setBanks] = useState(initialBanks);
  const [stage, setStage] = useState<Stage>("reporting");
  const [reportingBankSel, setReportingBankSel] = useState(initialBanks[0].id);
  const [receivingBankSel, setReceivingBankSel] = useState(initialBanks[1].id);
  const [tab, setTab] = useState<Tab>("outgoing");
  const [collapsed, setCollapsed] = useState(false);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const [reportView, setReportView] = useState("all");
  const [toast, setToast] = useState("");
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");
  const [uploadSummary, setUploadSummary] = useState<UploadSummary | null>(null);
  const [riskCodes, setRiskCodes] = useState<RiskCodeReference[]>(riskCodeCatalog);
  const [knowledgeBaseEntries, setKnowledgeBaseEntries] = useState<KnowledgeBaseEntry[]>([]);
  const [loadingData, setLoadingData] = useState(true);
  const [dataError, setDataError] = useState("");
  const uploadController = useRef<AbortController | null>(null);
  const dataController = useRef<AbortController | null>(null);

  const bankId = stage === "receiving" ? receivingBankSel : reportingBankSel;
  const bank = banks.find((item) => item.id === bankId)!;
  const bankName = (id: string) => id === "external" ? "External network" : banks.find((item) => item.id === id)?.name ?? id;
  const records = transactions.filter((item) => isVisible(item, bankId));
  const selected = transactions.find((item) => item.key === selectedKey);
  const submitted = records.filter((item) => reportingBankId(item) === bankId).length;
  const received = records.filter((item) => counterpartyBankId(item) === bankId && item.validationStatus === "validated_fraud").length;
  const visible = records.filter((item) => filter === "all" || item.validationStatus === filter).filter((item) => {
    const text = [item.customerRef, item.merchant, item.country, item.id, displayTransactionId(item, bank.name),
      transactionDirection(item, bankId), moneyDirection(item, bankId), bankName(reportingBankId(item))].join(" ").toLowerCase();
    return text.includes(query.trim().toLowerCase());
  });

  useEffect(() => {
    if (!toast) return;
    const timeout = window.setTimeout(() => setToast(""), 3000);
    return () => window.clearTimeout(timeout);
  }, [toast]);

  const refreshData = useCallback(async (showLoading = true) => {
    const controller = new AbortController();
    dataController.current?.abort();
    dataController.current = controller;
    if (showLoading) setLoadingData(true);
    setDataError("");
    try {
      const data = await loadDashboardData(initialBanks, controller.signal);
      setBanks(data.banks);
      setTransactions(data.transactions);
      setRiskCodes(data.riskCodes);
      setKnowledgeBaseEntries(data.knowledgeBaseEntries);
    } catch (error) {
      if (!controller.signal.aborted) {
        setDataError(error instanceof Error ? error.message : "Unable to load backend data.");
      }
      throw error;
    } finally {
      if (dataController.current === controller) {
        setLoadingData(false);
        dataController.current = null;
      }
    }
  }, []);

  useEffect(() => {
    void refreshData().catch(() => undefined);
    return () => {
      uploadController.current?.abort();
      dataController.current?.abort();
    };
  }, [refreshData]);

  function goToStage(next: Stage) {
    setStage(next);
    setSelectedKey(null);
    setQuery("");
    setFilter("all");
    setTab(next === "receiving" ? "incoming" : "outgoing");
  }

  function selectBank(id: string) {
    if (stage === "receiving") {
      setReceivingBankSel(id);
    } else {
      setReportingBankSel(id);
    }
    setQuery("");
    setFilter("all");
    setReportView("all");
    setSelectedKey(null);
  }

  async function upload(file: File) {
    const controller = new AbortController();
    uploadController.current?.abort();
    uploadController.current = controller;
    setUploading(true);
    setUploadError("");
    setUploadSummary(null);
    try {
      const payload = await validateCsv(await file.text(), controller.signal);
      await refreshData(false);
      setUploadSummary(payload.summary);
      setToast(`${payload.summary.total_rows} rows validated.`);
    } catch (error) {
      if (!controller.signal.aborted) {
        setUploadError(error instanceof Error ? error.message : "CSV upload failed.");
      }
    } finally {
      if (uploadController.current === controller) {
        setUploading(false);
        uploadController.current = null;
      }
    }
  }

  const reportingTabs: { id: Tab; label: string }[] = [
    { id: "outgoing", label: "Flags submitted" }, { id: "reports", label: "Reports" },
    { id: "knowledge", label: "Knowledge base" }, { id: "governance", label: "Governance" },
  ];
  const receivingTabs: { id: Tab; label: string }[] = [
    { id: "incoming", label: "Alerts received" }, { id: "history", label: "Related history" },
    { id: "reports", label: "Reports" }, { id: "knowledge", label: "Knowledge base" },
  ];
  const tabs = stage === "receiving" ? receivingTabs : reportingTabs;
  const metrics = [
    { label: "Related history", value: records.length, detail: "Records tied to this bank" },
    { label: "Submitted flags", value: submitted, detail: "From this bank's fraud system" },
    { label: "Alerts received", value: received, detail: "Validated fraud sent to this bank" },
    { label: "Cross-matched", value: records.filter((item) => item.corroborationCount > 0).length, detail: "Matched by another institution" },
  ];
  const transactionTabs = {
    incoming: { title: "Alerts received", subtitle: "Validated fraud alerts sent to this bank." },
    outgoing: { title: "Flags submitted", subtitle: "Transactions this bank sent for validation." },
    history: { title: "Related history", subtitle: "All validation records involving this bank." },
  };
  const stageMeta = STAGES.find((item) => item.id === stage)!;

  return <>
    <div className={`app ${collapsed ? "sidebar-collapsed" : ""}`} id="appShell">
      <nav className="nav-rail" aria-label="Demo walkthrough">
        <div className="nav-rail-brand"><Logo /></div>
        {STAGES.map((item) =>
          <button key={item.id} className={`rail-item ${stage === item.id ? "active" : ""}`} aria-pressed={stage === item.id}
            title={item.blurb} aria-label={item.label} onClick={() => goToStage(item.id)}>
            <span className="rail-icon"><Icon name={item.icon} /></span>
            <span className="rail-text">{item.railLabel}</span>
          </button>,
        )}
        <button className="rail-item rail-toggle" aria-label={collapsed ? "Expand panel" : "Collapse panel"} aria-expanded={!collapsed} onClick={() => setCollapsed(!collapsed)}>
          <span className="rail-icon"><Icon name={collapsed ? "chevron_right" : "chevron_left"} /></span>
        </button>
      </nav>
      <aside className="sidebar">
        <div className="sidebar-top">
          <div className="brand"><h1>{stageMeta.label}</h1><p>{stageMeta.blurb}</p></div>
        </div>
        {stage !== "kifaru" && <>
          <p className="sidebar-label">{stage === "receiving" ? "Receiving institution" : "Reporting institution"}</p>
          <div className="bank-list">{banks.map((item) =>
            <button className={`bank-card ${bankId === item.id ? "active" : ""}`} key={item.id} title={item.name} aria-label={item.name} aria-pressed={bankId === item.id} onClick={() => selectBank(item.id)}>
              <span><span className="bank-name">{item.name}</span><span className="bank-meta">{item.region}</span></span>
              <span className={`dot ${item.health === "warning" ? "warning" : ""}`} aria-hidden="true" />
            </button>,
          )}</div>
        </>}
        {stage === "reporting" && <div className="side-panel">
          <label className="pill" htmlFor="csvUpload">CSV log upload</label>
          <p className="muted">Upload a fraud log to test detection.</p>
          <input className="file-input" id="csvUpload" type="file" accept=".csv,text/csv" disabled={uploading}
            onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = "";
              if (file) void upload(file);
            }} />
          <div className="upload-output" aria-live="polite" aria-busy={uploading}>
            {uploading && <span className="muted">Processing...</span>}
            {uploadError && <div role="alert"><span className="pill review">Upload failed</span><p>{uploadError}</p></div>}
            {uploadSummary && <>
              <span><strong>{uploadSummary.total_rows}</strong> rows</span>
              <span><strong>{uploadSummary.validated_fraud}</strong> fraud</span>
              <span><strong>{uploadSummary.needs_review}</strong> review</span>
            </>}
            {!uploading && !uploadError && !uploadSummary && <span className="muted">Validation API: /api</span>}
          </div>
        </div>}
        {stage === "kifaru" && <div className="side-panel">
          <p className="sidebar-label">How it works</p>
          <ol className="how-it-works">
            <li>Reporting bank detects fraud, publishes a fingerprint</li>
            <li>Kifaru shares it across institutions</li>
            <li>Receiving bank matches it, holds the transfer</li>
          </ol>
        </div>}
      </aside>
      <main className="main">
        <section className="topbar"><div>
          <p className="eyebrow">Step {stageMeta.step} of 3</p><h2>{stageMeta.label}{stage !== "kifaru" && ` \u00b7 ${bank.name}`} <BrandDots /></h2>
          <p className="muted">{stage === "reporting" && "Flag suspicious activity and publish a protected fingerprint."}
            {stage === "kifaru" && "Every institution's shared fingerprints, matched across the ecosystem."}
            {stage === "receiving" && "See matches against the shared exchange and act on them."}</p>
          {dataError && <p className="muted" role="alert">Backend unavailable: {dataError}</p>}
        </div><div className="actions">
          <button className="btn" onClick={() => {
            const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
            document.documentElement.dataset.theme = next;
            window.localStorage.setItem("kifaru-theme", next);
          }}><Icon name="contrast" className="btn-icon" />Toggle theme</button>
          <button className="btn primary" disabled={loadingData} onClick={() => void refreshData().then(() => {
            setToast("Data refreshed.");
          }).catch(() => undefined)}><Icon name="refresh" className="btn-icon" />{loadingData ? "Loading..." : "Refresh data"}</button>
        </div></section>
        <nav className="tabs stage-tabs" aria-label="Demo stage">{STAGES.map((item) =>
          <button key={item.id} className={`tab-btn ${stage === item.id ? "active" : ""}`} aria-pressed={stage === item.id} onClick={() => goToStage(item.id)}>{item.step}. {item.label}</button>,
        )}</nav>
        {stage === "kifaru"
          ? <Exchange transactions={transactions} bankName={bankName} onOpen={setSelectedKey} />
          : <>
            <section className="grid metrics">{metrics.map((metric) =>
              <div className="metric" key={metric.label}><div className="metric-label"><span>{metric.label}</span></div>
                <div className="metric-value">{metric.value}</div><div className="metric-detail">{metric.detail}</div>
              </div>,
            )}</section>
            <nav className="tabs" aria-label="Institution sections">{tabs.map((item) =>
              <button key={item.id} className={`tab-btn ${tab === item.id ? "active" : ""}`} aria-pressed={tab === item.id} onClick={() => setTab(item.id)}>{item.label}</button>,
            )}</nav>
            {(tab === "incoming" || tab === "outgoing" || tab === "history") && <>
              <div className="transaction-controls">
                <div className="control-row">
                  <div className="search-field">
                    <Icon name="search" />
                    <input className="input" aria-label="Search transactions" placeholder="Search customer, bank, country" value={query} onChange={(event) => setQuery(event.target.value)} />
                  </div>
                  <select className="select" aria-label="Filter by risk" value={filter} onChange={(event) => setFilter(event.target.value)}>
                    <option value="all">All outcomes</option><option value="validated_fraud">Validated fraud</option>
                    <option value="not_fraud">Marked not fraud</option><option value="needs_review">Under review</option>
                  </select>
                </div>
              </div>
              <TransactionTable {...transactionTabs[tab]} records={visible.filter((item) => tab === "history"
                || (tab === "outgoing" ? reportingBankId(item) === bankId : counterpartyBankId(item) === bankId && item.validationStatus === "validated_fraud"))}
                bank={bank} bankName={bankName} history={tab === "history"} onOpen={setSelectedKey} />
            </>}
            {tab === "reports" && <Reports records={records} bank={bank} view={reportView} onView={setReportView} />}
            {tab === "knowledge" && <KnowledgeBase bank={bank} entries={knowledgeBaseEntries} riskCodes={riskCodes} />}
            {tab === "governance" && <AdminDetails bank={bank} notify={setToast}
              onThresholdChange={async (threshold) => {
                await updateInstitutionThreshold(bank.backendCode, threshold);
                await refreshData(false);
              }} />}
          </>}
      </main>
    </div>
    {selected && <Investigation transaction={selected} bank={bank} bankName={bankName} onClose={() => setSelectedKey(null)} />}
    <div className={`toast ${toast ? "show" : ""}`} role="status" aria-live="polite">{toast}</div>
  </>;
}
