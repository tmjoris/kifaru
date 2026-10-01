import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  createUserAccessRequest, decideUserAccessRequest, emitDemoStreamEvent,
  loadDashboardData, loadDemoStream, loadUserAccessRequests, login,
  logout as logoutSession, resetDemoStream, restoreSession, setDemoStreamState,
  subscribeToAlerts, updateAlertState, updateInstitutionThreshold, validateCsv,
} from "./api";
import { initialBanks, riskCodeCatalog } from "./data";
import { protectIdentifiers } from "./identifiers";
import { riskCodeInfo } from "./explain";
import {
  counterpartyBankId, displayTransactionId, isVisible, moneyDirection, reportingBankId,
  transactionDirection,
} from "./domain";
import type {
  AuthSession, Bank, DashboardData, DemoStreamStatus, KnowledgeBaseEntry, RiskCodeReference,
  Session, Tab,
  Transaction, UploadSummary, UserAccessRequest,
} from "./types";
import { BrandDots, Icon, Logo } from "./components/Shared";
import { Investigation, TransactionTable } from "./components/Transactions";
import { Reports } from "./components/Reports";
import { Exchange } from "./components/Exchange";
import { AdminDetails, KnowledgeBase } from "./components/ReferencePanels";
import { PortalLogin } from "./components/PortalLogin";
import { DemoStream } from "./components/DemoStream";
import { StaffUserAdmissions } from "./components/UserAccess";

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [sessionReady, setSessionReady] = useState(false);
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
  const [userAccessRequests, setUserAccessRequests] = useState<UserAccessRequest[]>([]);
  const [userRequestDomain, setUserRequestDomain] = useState("");
  const [userRequestsLoading, setUserRequestsLoading] = useState(false);
  const [userRequestsError, setUserRequestsError] = useState("");
  const uploadController = useRef<AbortController | null>(null);
  const dataController = useRef<AbortController | null>(null);
  const userRequestController = useRef<AbortController | null>(null);
  const pointerOnList = useRef(false);
  const heldUpdate = useRef<DashboardData | null>(null);
  const [updateWaiting, setUpdateWaiting] = useState(false);

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
  const bankRef = (id: string) => id === "external" ? "EXT" : banks.find((item) => item.id === id)?.shortName ?? id;
  const records = transactions.filter((item) => isVisible(item, bankId));
  const selected = transactions.find((item) => item.key === selectedKey);
  const submitted = records.filter((item) => reportingBankId(item) === bankId).length;
  const received = records.filter((item) => counterpartyBankId(item) === bankId && item.validationStatus === "validated_fraud").length;
  const visible = records.filter((item) => filter === "all" || item.validationStatus === filter).filter((item) => {
    const text = [item.customerRef, item.merchant, item.country, item.id, displayTransactionId(item, bankRef(reportingBankId(item))),
      transactionDirection(item, bankId), moneyDirection(item, bankId), bankName(reportingBankId(item)),
      riskCodeInfo(item.riskCode.code, item.riskCode.label).title, item.riskCode.code].join(" ").toLowerCase();
    return text.includes(query.trim().toLowerCase());
  });

  const sessionFromAuth = useCallback((auth: AuthSession): Session | null => {
    if (auth.role === "staff") {
      if (!staffRoute) return null;
      return {
        scope: "exchange", bankId: null, email: auth.email, displayName: auth.displayName,
        institutionCode: "", institutionName: "", expiresAt: auth.expiresAt,
      };
    }
    if (staffRoute) return null;
    const assignedBank = bankTemplates.find((item) => item.backendCode === auth.institutionCode);
    if (!assignedBank) return null;
    return {
      scope: "institution", bankId: assignedBank.id, email: auth.email,
      displayName: auth.displayName, institutionCode: auth.institutionCode,
      institutionName: auth.institutionName, expiresAt: auth.expiresAt,
    };
  }, [bankTemplates, staffRoute]);

  const clearProtectedData = useCallback(() => {
    uploadController.current?.abort();
    dataController.current?.abort();
    setBanks(bankTemplates);
    setTransactions([]);
    setSelectedKey(null);
    setQuery("");
    setFilter("all");
    setReportView("all");
    setTab("outgoing");
    setUploadError("");
    setUploadSummary(null);
    setRiskCodes(riskCodeCatalog);
    setKnowledgeBaseEntries([]);
    setDemoStream(null);
    userRequestController.current?.abort();
    setUserAccessRequests([]);
    setUserRequestDomain("");
    setUserRequestsLoading(false);
    setUserRequestsError("");
    pointerOnList.current = false;
    heldUpdate.current = null;
    setUpdateWaiting(false);
    setDataError("");
    setLoadingData(false);
  }, [bankTemplates]);

  useEffect(() => {
    let active = true;
    void restoreSession()
      .then(async (auth) => {
        if (!active || !auth) return;
        const restored = sessionFromAuth(auth);
        if (restored) {
          setSession(restored);
        } else {
          await logoutSession();
        }
      })
      .catch((error) => {
        if (active) setDataError(error instanceof Error ? error.message : "Unable to restore the secure session.");
      })
      .finally(() => {
        if (active) setSessionReady(true);
      });
    return () => {
      active = false;
    };
  }, [sessionFromAuth]);

  useEffect(() => {
    const expire = () => {
      setSession(null);
      clearProtectedData();
      setToast("Your session expired. Sign in again.");
    };
    window.addEventListener("kifaru-auth-expired", expire);
    return () => window.removeEventListener("kifaru-auth-expired", expire);
  }, [clearProtectedData]);

  useEffect(() => {
    if (!toast) return;
    const timeout = window.setTimeout(() => setToast(""), 3000);
    return () => window.clearTimeout(timeout);
  }, [toast]);

  const applyData = useCallback((data: DashboardData) => {
    setBanks(data.banks);
    setTransactions(data.transactions);
    setRiskCodes(data.riskCodes);
    setKnowledgeBaseEntries(data.knowledgeBaseEntries);
  }, []);

  // Live updates wait while the pointer is over a list, so rows do not move under the cursor.
  const pointerOverList = useCallback((over: boolean) => {
    pointerOnList.current = over;
    if (!over && heldUpdate.current) {
      applyData(heldUpdate.current);
      heldUpdate.current = null;
      setUpdateWaiting(false);
    }
  }, [applyData]);

  const refreshData = useCallback(async (showLoading = true, live = false) => {
    if (!activeSession) return;
    const controller = new AbortController();
    dataController.current?.abort();
    dataController.current = controller;
    if (showLoading) setLoadingData(true);
    setDataError("");
    try {
      const data = await loadDashboardData(bankTemplates, {
        scope: activeSession.scope,
        institutionCode: activeSession.institutionCode,
      }, controller.signal);
      if (live && pointerOnList.current) {
        heldUpdate.current = data;
        setUpdateWaiting(true);
      } else {
        heldUpdate.current = null;
        setUpdateWaiting(false);
        applyData(data);
      }
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
  }, [activeSession, applyData, bankTemplates]);

  const refreshDemoStream = useCallback(async () => {
    const state = await loadDemoStream();
    setDemoStream(state);
    return state;
  }, []);

  const refreshUserAccessRequests = useCallback(async (showLoading = true) => {
    if (!activeSession) return;
    const controller = new AbortController();
    userRequestController.current?.abort();
    userRequestController.current = controller;
    if (showLoading) setUserRequestsLoading(true);
    setUserRequestsError("");
    try {
      const payload = await loadUserAccessRequests(controller.signal);
      setUserAccessRequests(payload.requests);
      setUserRequestDomain(payload.emailDomain);
    } catch (error) {
      if (!controller.signal.aborted) {
        setUserRequestsError(error instanceof Error ? error.message : "Unable to load user requests.");
      }
      throw error;
    } finally {
      if (userRequestController.current === controller) {
        setUserRequestsLoading(false);
        userRequestController.current = null;
      }
    }
  }, [activeSession]);

  useEffect(() => {
    if (!activeSession) {
      setLoadingData(false);
      return;
    }
    void refreshData().catch(() => undefined);
    return () => {
      uploadController.current?.abort();
      dataController.current?.abort();
      userRequestController.current?.abort();
    };
  }, [activeSession, refreshData]);

  useEffect(() => {
    if (!activeSession) return;
    void refreshUserAccessRequests().catch(() => undefined);
  }, [activeSession, refreshUserAccessRequests]);

  useEffect(() => {
    if (!activeSession || !isExchange) return;
    void refreshDemoStream().catch(() => undefined);
  }, [activeSession, isExchange, refreshDemoStream]);

  useEffect(() => {
    if (!activeSession) return;
    const institution = isExchange ? "*" : bank.backendCode;
    return subscribeToAlerts(institution, {
      alert: () => {
        void refreshData(false, true).catch(() => undefined);
      },
      demoEvent: () => {
        void Promise.all([refreshData(false, true), refreshDemoStream()]).catch(() => undefined);
      },
    });
  }, [activeSession, bank.backendCode, isExchange, refreshData, refreshDemoStream]);

  async function authenticate(email: string, password: string, nextBankId: string | null, keepSignedIn: boolean) {
    const selectedBank = nextBankId ? bankTemplates.find((item) => item.id === nextBankId) : null;
    const auth = await login(email, password, selectedBank?.backendCode ?? "", keepSignedIn);
    const nextSession = sessionFromAuth(auth);
    if (!nextSession) {
      await logoutSession();
      throw new Error(staffRoute
        ? "Use a Kifaru staff account on this sign-in page."
        : "Use an institution account on this sign-in page.");
    }
    clearProtectedData();
    setSession(nextSession);
  }

  async function switchPortal() {
    try {
      await logoutSession();
    } catch (error) {
      setToast(error instanceof Error ? error.message : "Sign-out failed.");
    } finally {
      setSession(null);
      clearProtectedData();
    }
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

  async function requestInstitutionUser(alias: string) {
    await createUserAccessRequest(alias);
    await refreshUserAccessRequests(false).catch(() => undefined);
  }

  async function reviewInstitutionUser(
    requestId: string,
    decision: "approved" | "rejected",
  ) {
    await decideUserAccessRequest(requestId, decision);
    await refreshUserAccessRequests(false).catch(() => undefined);
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

  if (!sessionReady) {
    return <main className="portal-auth-loading"><Logo /><p>Checking secure session...</p></main>;
  }

  if (!activeSession) {
    return <PortalLogin banks={banks} staff={staffRoute}
      onEnterKifaru={(email, password, keepSignedIn) =>
        authenticate(email, password, null, keepSignedIn)}
      onEnterBank={(email, password, nextBankId, keepSignedIn) =>
        authenticate(email, password, nextBankId, keepSignedIn)} />;
  }

  return <>
    <div className={`app ${collapsed ? "sidebar-collapsed" : ""}`} id="appShell">
      <nav className="nav-rail" aria-label="Signed-in portal">
        <div className="nav-rail-brand"><Logo /></div>
        <div className="rail-item active" aria-current="page">
          <span className="rail-icon"><Icon name={workspaceMeta.icon} /></span>
          <span className="rail-text">{workspaceMeta.railLabel}</span>
        </div>
        <button className="rail-item rail-signout" aria-label="Sign out" title="Sign out"
          onClick={() => void switchPortal()}>
          <span className="rail-icon"><Icon name="logout" /></span>
          <span className="rail-text">Sign out</span>
        </button>
        <button className="rail-item rail-toggle" aria-label={collapsed ? "Expand panel" : "Collapse panel"} aria-expanded={!collapsed} onClick={() => setCollapsed(!collapsed)}>
          <span className="rail-icon"><Icon name={collapsed ? "chevron_right" : "chevron_left"} /></span>
        </button>
      </nav>
      <aside className="sidebar">
        <div className="sidebar-top">
          <div className="brand"><h1>{workspaceMeta.label}</h1><p>{workspaceMeta.blurb}</p></div>
        </div>
        <p className="signed-in-user"><strong>{activeSession.displayName}</strong><span>{activeSession.email}</span></p>
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
          <button className="btn" onClick={() => void switchPortal()}><Icon name="logout" className="btn-icon" />Sign out</button>
          <button className="btn" onClick={() => {
            const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
            document.documentElement.dataset.theme = next;
            window.localStorage.setItem("kifaru-theme", next);
          }}><Icon name="contrast" className="btn-icon" />Toggle theme</button>
          <button className="btn primary" disabled={loadingData || userRequestsLoading} onClick={() => void Promise.all([
            refreshData(), refreshUserAccessRequests(false),
          ]).then(() => {
            setToast("Data refreshed.");
          }).catch(() => undefined)}><Icon name="refresh" className="btn-icon" />{loadingData || userRequestsLoading ? "Loading..." : "Refresh data"}</button>
        </div></section>
        {isExchange
          ? <div className="stack">
            <StaffUserAdmissions requests={userAccessRequests} loading={userRequestsLoading}
              error={userRequestsError} onDecision={reviewInstitutionUser} notify={setToast} />
            <DemoStream stream={demoStream} busy={demoBusy}
              onToggle={controlDemoStream} onEmit={emitDemoEvent} onReset={clearDemoStream} />
            {updateWaiting && <p className="update-waiting" role="status">New activity has arrived. The cards update when you move the pointer off them.</p>}
            <div onPointerEnter={() => pointerOverList(true)} onPointerLeave={() => pointerOverList(false)}>
              <Exchange transactions={transactions} bankName={bankName} onOpen={setSelectedKey} />
            </div>
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
                    <input className="input" aria-label="Search transactions" placeholder="Search by institution, risk or report ID" value={query} onChange={(event) => setQuery(event.target.value)} />
                  </div>
                  <select className="select" aria-label="Filter by risk" value={filter} onChange={(event) => setFilter(event.target.value)}>
                    <option value="all">All outcomes</option><option value="validated_fraud">Validated fraud</option>
                    <option value="not_fraud">Marked not fraud</option><option value="needs_review">Under review</option>
                  </select>
                </div>
              </div>
              {updateWaiting && <p className="update-waiting" role="status">New activity has arrived. The list updates when you move the pointer off it.</p>}
              <div onPointerEnter={() => pointerOverList(true)} onPointerLeave={() => pointerOverList(false)}>
                <TransactionTable {...transactionTabs[tab]} records={visible.filter((item) => tab === "history"
                  || (tab === "outgoing" ? reportingBankId(item) === bankId : counterpartyBankId(item) === bankId && item.validationStatus === "validated_fraud"))}
                  bank={bank} bankName={bankName} bankRef={bankRef} history={tab === "history"} onOpen={setSelectedKey} />
              </div>
            </>}
            {tab === "reports" && <Reports records={records} bank={bank} view={reportView} onView={setReportView} />}
            {tab === "knowledge" && <KnowledgeBase bank={bank} entries={knowledgeBaseEntries} riskCodes={riskCodes} />}
            {tab === "governance" && <AdminDetails bank={bank} notify={setToast}
              userRequests={userAccessRequests} userRequestDomain={userRequestDomain}
              userRequestsLoading={userRequestsLoading} userRequestsError={userRequestsError}
              onUserRequest={requestInstitutionUser}
              onThresholdChange={async (threshold) => {
                await updateInstitutionThreshold(bank.backendCode, threshold);
                await refreshData(false);
              }} />}
          </>}
      </main>
    </div>
    {selected && <Investigation transaction={selected} viewerBankId={isExchange ? null : bank.id}
      bankName={bankName} bankRef={bankRef}
      onClose={() => setSelectedKey(null)} onAlertAction={isExchange ? undefined : actOnAlert} />}
    <div className={`toast ${toast ? "show" : ""}`} role="status" aria-live="polite">{toast}</div>
  </>;
}
