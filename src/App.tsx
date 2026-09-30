import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  alertStreamUrl, emitDemoStreamEvent, loadDashboardData, loadDemoStream, resetDemoStream,
  setDemoStreamState, updateAlertState, updateInstitutionThreshold, validateCsv,
} from "./api";
import { initialBanks, riskCodeCatalog } from "./data";
import { protectIdentifiers } from "./identifiers";
import {
  counterpartyBankId, displayTransactionId, isVisible, moneyDirection, reportingBankId,
  resolveSession, transactionDirection,
} from "./domain";
import type {
  Bank, DemoStreamStatus, KnowledgeBaseEntry, PortalScope, RiskCodeReference, Session, Tab,
  Transaction, UploadSummary,
} from "./types";
import { BrandDots, Icon, Logo } from "./components/Shared";
import { Investigation, TransactionTable } from "./components/Transactions";
import { Reports } from "./components/Reports";
import { Exchange } from "./components/Exchange";
import { AdminDetails, KnowledgeBase } from "./components/ReferencePanels";
import { PortalLogin } from "./components/PortalLogin";
import { DemoStream } from "./components/DemoStream";

const SESSION_KEY = "kifaru-session";

function readJson<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? JSON.parse(raw) as T : null;
  } catch {
    return null;
  }
}

function readSession(): Session | null {
  return resolveSession(readJson<Session & { stage?: string }>(SESSION_KEY), initialBanks.map((bank) => bank.id));
}

export default function App() {
  const [session, setSession] = useState<Session | null>(readSession);
  const bankTemplates = useMemo<Bank[]>(() => initialBanks, []);
  const [banks, setBanks] = useState<Bank[]>(bankTemplates);
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
  const [demoStream, setDemoStream] = useState<DemoStreamStatus | null>(null);
  const [demoBusy, setDemoBusy] = useState(false);
  const uploadController = useRef<AbortController | null>(null);
  const dataController = useRef<AbortController | null>(null);

  useEffect(() => {
    if (session) window.localStorage.setItem(SESSION_KEY, JSON.stringify(session));
    else window.localStorage.removeItem(SESSION_KEY);
  }, [session]);

  const staffRoute = window.location.pathname === "/staff";
  const activeSession = session
    && ((staffRoute && session.scope === "exchange") || (!staffRoute && session.scope === "institution"))
    ? session
    : null;
  const scope = activeSession?.scope ?? (staffRoute ? "exchange" : "institution");
  const isExchange = scope === "exchange";
  const bankId = activeSession?.bankId ?? "";
  const bank = banks.find((item) => item.id === bankId) ?? banks[0];
  const bankName = (id: string) => id === "external" ? "External network" : banks.find((item) => item.id === id)?.name ?? id;
  const records = transactions.filter((item) => isVisible(item, bankId));
  const selected = transactions.find((item) => item.key === selectedKey);
  const submitted = records.filter((item) => reportingBankId(item) === bankId).length;
  const received = records.filter((item) => counterpartyBankId(item) === bankId && item.validationStatus === "validated_fraud").length;
  const visible = records.filter((item) => filter === "all" || item.validationStatus === filter).filter((item) => {
    const text = [item.customerRef, item.merchant, item.country, item.id, displayTransactionId(item, bank.shortName),
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
      const data = await loadDashboardData(bankTemplates, controller.signal);
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
  }, [bankTemplates]);

  const refreshDemoStream = useCallback(async () => {
    const state = await loadDemoStream();
    setDemoStream(state);
    return state;
  }, []);

  useEffect(() => {
    void refreshData().catch(() => undefined);
    return () => {
      uploadController.current?.abort();
      dataController.current?.abort();
    };
  }, [refreshData]);

  useEffect(() => {
    if (!activeSession || !isExchange) return;
    void refreshDemoStream().catch(() => undefined);
  }, [activeSession, isExchange, refreshDemoStream]);

  useEffect(() => {
    if (!activeSession) return;
    const institution = isExchange ? "*" : bank.backendCode;
    const stream = new EventSource(alertStreamUrl(institution));
    stream.addEventListener("alert", () => {
      void refreshData(false).catch(() => undefined);
    });
    stream.addEventListener("demo-event", () => {
      void Promise.all([refreshData(false), refreshDemoStream()]).catch(() => undefined);
    });
    return () => stream.close();
  }, [activeSession, bank.backendCode, isExchange, refreshData, refreshDemoStream]);

  function enterPortal(next: PortalScope, nextBankId: string | null) {
    setSession({ scope: next, bankId: nextBankId });
    setSelectedKey(null);
    setQuery("");
    setFilter("all");
    setReportView("all");
    setTab("outgoing");
  }

  function switchPortal() {
    setSession(null);
  }

  async function upload(file: File) {
    const controller = new AbortController();
    uploadController.current?.abort();
    uploadController.current = controller;
    setUploading(true);
    setUploadError("");
    setUploadSummary(null);
    try {
      const { csv, hashed } = await protectIdentifiers(await file.text());
      const payload = await validateCsv(csv, controller.signal);
      await refreshData(false);
      setUploadSummary(payload.summary);
      setToast(`${payload.summary.total_rows} rows validated. ${hashed} identifiers hashed in this browser before upload.`);
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

  async function actOnAlert(
    alertId: string,
    state: "acknowledged" | "actioned" | "disputed",
    comment = "",
  ) {
    try {
      await updateAlertState(alertId, state, comment);
      await refreshData(false);
      setToast(`Alert ${state}.`);
    } catch (error) {
      setToast(error instanceof Error ? error.message : "Alert update failed.");
    }
  }

  async function controlDemoStream(enabled: boolean) {
    setDemoBusy(true);
    try {
      setDemoStream(await setDemoStreamState(enabled, 30));
      setToast(enabled ? "Synthetic SOC stream started." : "Synthetic SOC stream paused.");
    } catch (error) {
      setToast(error instanceof Error ? error.message : "Unable to update the synthetic stream.");
    } finally {
      setDemoBusy(false);
    }
  }

  async function emitDemoEvent() {
    setDemoBusy(true);
    try {
      await emitDemoStreamEvent();
      await Promise.all([refreshDemoStream(), refreshData(false)]);
      setToast("Synthetic Sentinel event processed.");
    } catch (error) {
      setToast(error instanceof Error ? error.message : "Unable to emit a synthetic event.");
    } finally {
      setDemoBusy(false);
    }
  }

  async function clearDemoStream() {
    setDemoBusy(true);
    try {
      setDemoStream(await resetDemoStream());
      await refreshData(false);
      setToast("Synthetic stream reset.");
    } catch (error) {
      setToast(error instanceof Error ? error.message : "Unable to reset the synthetic stream.");
    } finally {
      setDemoBusy(false);
    }
  }

  const tabs: { id: Tab; label: string }[] = [
    { id: "outgoing", label: "Flags submitted" },
    { id: "incoming", label: "Alerts received" }, { id: "history", label: "Related history" },
    { id: "reports", label: "Reports" }, { id: "knowledge", label: "Knowledge base" },
    { id: "governance", label: "Governance" },
  ];
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
  const workspaceMeta = isExchange
    ? { label: "Kifaru exchange", blurb: "Cross-institution fraud signals and ecosystem matches.", icon: "hub", railLabel: "Exchange" }
    : { label: bank.name, blurb: "Report, receive, and investigate shared fraud signals.", icon: "account_balance", railLabel: "Workspace" };

  if (!activeSession) {
    return <PortalLogin banks={banks} staff={staffRoute}
      onEnterKifaru={() => enterPortal("exchange", null)}
      onEnterBank={(nextBankId) => enterPortal("institution", nextBankId)} />;
  }

  return <>
    <div className={`app ${collapsed ? "sidebar-collapsed" : ""}`} id="appShell">
      <nav className="nav-rail" aria-label="Signed-in portal">
        <div className="nav-rail-brand"><Logo /></div>
        <div className="rail-item active" aria-current="page">
          <span className="rail-icon"><Icon name={workspaceMeta.icon} /></span>
          <span className="rail-text">{workspaceMeta.railLabel}</span>
        </div>
        <button className="rail-item rail-signout" aria-label="Switch portal" title="Switch portal" onClick={switchPortal}>
          <span className="rail-icon"><Icon name="logout" /></span>
          <span className="rail-text">Switch</span>
        </button>
        <button className="rail-item rail-toggle" aria-label={collapsed ? "Expand panel" : "Collapse panel"} aria-expanded={!collapsed} onClick={() => setCollapsed(!collapsed)}>
          <span className="rail-icon"><Icon name={collapsed ? "chevron_right" : "chevron_left"} /></span>
        </button>
      </nav>
      <aside className="sidebar">
        <div className="sidebar-top">
          <div className="brand"><h1>{workspaceMeta.label}</h1><p>{workspaceMeta.blurb}</p></div>
        </div>
        {!isExchange && <>
          <p className="sidebar-label">Institution</p>
          <div className="bank-list">
            <div className="bank-card active signed-in-card" aria-current="true">
              <span><span className="bank-name">{bank.name}</span><span className="bank-meta">{bank.region}</span></span>
              <span className={`dot ${bank.health === "warning" ? "warning" : bank.health === "pending" ? "pending" : ""}`} aria-hidden="true" />
            </div>
          </div>
        </>}
        {!isExchange && <div className="side-panel">
          <label className="pill" htmlFor="csvUpload">CSV log upload</label>
          <p className="muted">Upload a fraud log to test detection. Identifier columns are hashed in this browser first.</p>
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
        {isExchange && <div className="side-panel">
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
          <p className="eyebrow">{isExchange ? "Ecosystem operations" : "Institution workspace"} · Synthetic demo data</p>
          <h2>{workspaceMeta.label} <BrandDots /></h2>
          <p className="muted">{isExchange
            ? "Every institution's shared fingerprints, matched across the ecosystem."
            : "Submit suspicious activity, receive matched alerts, and investigate related records."}</p>
          {dataError && <p className="muted" role="alert">Backend unavailable: {dataError}</p>}
        </div><div className="actions">
          <button className="btn" onClick={switchPortal}><Icon name="logout" className="btn-icon" />Switch portal</button>
          <button className="btn" onClick={() => {
            const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
            document.documentElement.dataset.theme = next;
            window.localStorage.setItem("kifaru-theme", next);
          }}><Icon name="contrast" className="btn-icon" />Toggle theme</button>
          <button className="btn primary" disabled={loadingData} onClick={() => void refreshData().then(() => {
            setToast("Data refreshed.");
          }).catch(() => undefined)}><Icon name="refresh" className="btn-icon" />{loadingData ? "Loading..." : "Refresh data"}</button>
        </div></section>
        {isExchange
          ? <div className="stack">
            <DemoStream stream={demoStream} busy={demoBusy}
              onToggle={controlDemoStream} onEmit={emitDemoEvent} onReset={clearDemoStream} />
            <Exchange transactions={transactions} bankName={bankName} onOpen={setSelectedKey} />
          </div>
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
    {selected && <Investigation transaction={selected} bank={bank} bankName={bankName}
      onClose={() => setSelectedKey(null)} onAlertAction={isExchange ? undefined : actOnAlert} />}
    <div className={`toast ${toast ? "show" : ""}`} role="status" aria-live="polite">{toast}</div>
  </>;
}
