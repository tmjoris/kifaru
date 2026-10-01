import type {
  AuthSession, Bank, DashboardData, DemoStreamEvent, DemoStreamStatus, KnowledgeBaseEntry, PortalScope, RiskCodeReference,
  Transaction, UploadSummary, Validation,
} from "./types";
import { riskCodeInfo } from "./explain";

const configuredApiUrl = (import.meta.env.VITE_API_URL || "").replace(/\/$/, "");
const API_BASE_URL = configuredApiUrl && !configuredApiUrl.startsWith("http")
  ? `https://${configuredApiUrl}`
  : configuredApiUrl;
const ACCESS_TOKEN_KEY = "kifaru-access-token";
let accessToken = window.sessionStorage.getItem(ACCESS_TOKEN_KEY)
  || window.localStorage.getItem(ACCESS_TOKEN_KEY) || "";
let csrfToken = "";

function apiUrl(path: string) {
  const resolvedPath = API_BASE_URL ? path.replace(/^\/api(?=\/|$)/, "") : path;
  return `${API_BASE_URL}${resolvedPath}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isValidation(value: unknown): value is Validation {
  if (!isRecord(value)) return false;
  const stringFields = ["transaction_id", "reporting_bank", "receiving_bank", "customer_ref",
    "currency", "amount", "validated_by", "short_explanation", "recommended_action"];
  return stringFields.every((field) => typeof value[field] === "string")
    && typeof value.confidence === "number" && Number.isFinite(value.confidence)
    && ["validated_fraud", "not_fraud", "needs_review"].includes(String(value.status))
    && Array.isArray(value.risk_codes)
    && value.risk_codes.every((code) => isRecord(code) && typeof code.code === "string" && typeof code.label === "string")
    && Array.isArray(value.key_signals) && value.key_signals.every((signal) => typeof signal === "string");
}

function isSummary(value: unknown): value is UploadSummary {
  return isRecord(value)
    && ["total_rows", "validated_fraud", "not_fraud", "needs_review"]
      .every((field) => typeof value[field] === "number" && Number.isInteger(value[field]) && Number(value[field]) >= 0)
    && Array.isArray(value.top_risk_codes)
    && value.top_risk_codes.every((item) => isRecord(item) && typeof item.code === "string" && typeof item.count === "number");
}

async function apiFetch(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  if (accessToken) headers.set("Authorization", `Bearer ${accessToken}`);
  if (csrfToken && ["POST", "PATCH", "DELETE"].includes((init.method || "GET").toUpperCase())) {
    headers.set("X-Kifaru-CSRF", csrfToken);
  }
  return fetch(apiUrl(path), { ...init, headers, credentials: "include" });
}

async function fetchJson(path: string, init?: RequestInit): Promise<unknown> {
  const response = await apiFetch(path, init);
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 && !path.includes("/v1/auth/")) {
      clearAuthState();
      window.dispatchEvent(new Event("kifaru-auth-expired"));
    }
    const detail = isRecord(payload) && typeof payload.detail === "string"
      ? payload.detail : `Backend request failed (HTTP ${response.status}).`;
    throw new Error(detail);
  }
  return payload;
}

function storeAccessToken(token: string, persistent: boolean) {
  window.sessionStorage.removeItem(ACCESS_TOKEN_KEY);
  window.localStorage.removeItem(ACCESS_TOKEN_KEY);
  accessToken = token;
  if (token) {
    (persistent ? window.localStorage : window.sessionStorage).setItem(ACCESS_TOKEN_KEY, token);
  }
}

function clearAuthState() {
  storeAccessToken("", false);
  csrfToken = "";
}

function parseAuthSession(payload: unknown): AuthSession {
  if (!isRecord(payload) || !["institution", "staff"].includes(String(payload.role))) {
    throw new Error("The authentication service returned an invalid session.");
  }
  const nextCSRFToken = requiredString(payload, "csrf_token");
  if (!nextCSRFToken) throw new Error("The authenticated session is missing its CSRF token.");
  csrfToken = nextCSRFToken;
  return {
    email: requiredString(payload, "email"),
    displayName: requiredString(payload, "display_name"),
    role: payload.role as AuthSession["role"],
    institutionCode: requiredString(payload, "institution_code"),
    institutionName: requiredString(payload, "institution_name"),
    expiresAt: requiredString(payload, "expires_at"),
  };
}

export async function login(
  email: string,
  password: string,
  institutionCode: string,
  keepSignedIn: boolean,
) {
  const payload = await fetchJson("/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      email,
      password,
      institution_code: institutionCode,
      keep_signed_in: keepSignedIn,
    }),
  });
  if (!isRecord(payload) || typeof payload.access_token !== "string" || !payload.access_token) {
    throw new Error("The authentication service did not issue a session token.");
  }
  storeAccessToken(payload.access_token, keepSignedIn);
  return parseAuthSession(payload);
}

export async function restoreSession(): Promise<AuthSession | null> {
  const response = await apiFetch("/api/v1/auth/session");
  if (response.status === 401) {
    clearAuthState();
    return null;
  }
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const detail = isRecord(payload) && typeof payload.detail === "string"
      ? payload.detail : `Session restore failed (HTTP ${response.status}).`;
    throw new Error(detail);
  }
  return parseAuthSession(payload);
}

export async function logout() {
  try {
    await fetchJson("/api/v1/auth/logout", { method: "POST" });
  } finally {
    clearAuthState();
  }
}

export function subscribeToAlerts(
  institution: string,
  handlers: { alert: () => void; demoEvent: () => void },
) {
  const controller = new AbortController();
  const decoder = new TextDecoder();

  async function connect() {
    while (!controller.signal.aborted) {
      try {
        const response = await apiFetch(`/api/v1/stream?institution=${encodeURIComponent(institution)}`, {
          signal: controller.signal,
          headers: { Accept: "text/event-stream" },
        });
        if (response.status === 401) {
          clearAuthState();
          window.dispatchEvent(new Event("kifaru-auth-expired"));
          return;
        }
        if (!response.ok || !response.body) {
          throw new Error(`Live stream failed (HTTP ${response.status}).`);
        }
        const reader = response.body.getReader();
        let buffer = "";
        while (!controller.signal.aborted) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true }).replaceAll("\r\n", "\n");
          let boundary = buffer.indexOf("\n\n");
          while (boundary >= 0) {
            const block = buffer.slice(0, boundary);
            buffer = buffer.slice(boundary + 2);
            const eventName = block.split("\n")
              .find((line) => line.startsWith("event:"))?.slice(6).trim();
            if (eventName === "alert") handlers.alert();
            if (eventName === "demo-event") handlers.demoEvent();
            boundary = buffer.indexOf("\n\n");
          }
        }
      } catch (error) {
        if (controller.signal.aborted) return;
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
    }
  }

  void connect();
  return () => controller.abort();
}

function parseStringArray(value: unknown): string[] {
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === "string");
  if (typeof value !== "string") return [];
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function parseEvidenceFields(value: unknown): Record<string, unknown> {
  if (typeof value !== "string") return {};
  try {
    const parsed: unknown = JSON.parse(value);
    return isRecord(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function parseEvidence(value: unknown): string[] {
  if (typeof value !== "string") return [];
  try {
    const parsed: unknown = JSON.parse(value);
    if (!isRecord(parsed)) return [];
    return Object.entries(parsed)
      .filter(([, item]) => item !== "" && item !== null && item !== undefined)
      .map(([key, item]) => `${key.replaceAll("_", " ")}: ${String(item)}`);
  } catch {
    return [];
  }
}

function backendStatus(value: unknown): Transaction["validationStatus"] {
  if (value === "VALIDATED_FRAUD") return "validated_fraud";
  if (value === "NOT_FRAUD") return "not_fraud";
  return "needs_review";
}

function requiredString(record: Record<string, unknown>, key: string) {
  const value = record[key];
  return typeof value === "string" ? value : "";
}

function requiredNumber(record: Record<string, unknown>, key: string) {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function parseRiskCodes(payload: unknown): RiskCodeReference[] {
  if (!isRecord(payload) || !isRecord(payload.codes)) {
    throw new Error("The backend returned an invalid fraud standard.");
  }
  return Object.entries(payload.codes).flatMap(([code, value]) => {
    if (!isRecord(value) || typeof value.name !== "string") return [];
    const info = riskCodeInfo(code, value.name);
    return [{ code, label: info.title, text: info.detail }];
  });
}

function parseKnowledgeBase(payload: unknown): KnowledgeBaseEntry[] {
  if (!Array.isArray(payload)) throw new Error("The backend returned an invalid knowledge base.");
  return payload.flatMap((value) => {
    if (!isRecord(value)) return [];
    const artefactHash = requiredString(value, "artefact_hash");
    const listName = requiredString(value, "list_name");
    if (!artefactHash || !listName) return [];
    return [{
      artefactHash,
      listName,
      label: requiredString(value, "label"),
      addedBy: requiredString(value, "added_by"),
      addedAt: requiredString(value, "added_at"),
    }];
  });
}

function transactionFromHistory(
  row: Record<string, unknown>,
  codeToBankId: Map<string, string>,
  riskCodeNames: Map<string, string>,
): Transaction {
  const riskCodes = parseStringArray(row.risk_codes);
  const reasonCodes = parseStringArray(row.reason_codes);
  const firstRiskCode = riskCodes[0] ?? reasonCodes[0] ?? "GEN-400";
  const reportingCode = requiredString(row, "reporting_institution");
  const destinationCode = requiredString(row, "destination_institution");
  const customerHash = requiredString(row, "subject_customer_hash");
  const currency = requiredString(row, "currency") || "KES";
  const amount = requiredNumber(row, "amount");
  const explanation = requiredString(row, "explanation");
  const status = backendStatus(row.status);
  const evidence = [...reasonCodes, ...parseEvidence(row.evidence)];
  const corroboratingInstitutions = parseStringArray(row.corroborating_institutions)
    .map((code) => codeToBankId.get(code) ?? code);

  return {
    key: requiredString(row, "report_id"),
    id: requiredString(row, "transaction_ref") || requiredString(row, "report_id"),
    sourceBank: codeToBankId.get(reportingCode) ?? `unknown:${reportingCode}`,
    destinationBank: codeToBankId.get(destinationCode) ?? "external",
    customerRef: customerHash ? `*${customerHash.slice(-4)}` : "Unavailable",
    merchant: requiredString(row, "reporting_system") || "Bank fraud system",
    country: requiredString(row, "channel") || currency,
    amount: `${currency} ${amount.toLocaleString(undefined, { maximumFractionDigits: 0 })}`,
    score: Math.round(requiredNumber(row, "validation_score") * 100),
    flagSource: requiredString(row, "agent_version") || "Kifaru agent",
    validationStatus: status,
    riskCode: {
      code: firstRiskCode,
      label: riskCodeNames.get(firstRiskCode) ?? "Central fraud signal",
    },
    evidence: evidence.length ? evidence : [explanation || "No additional evidence recorded."],
    reasonCodes,
    evidenceFields: parseEvidenceFields(row.evidence),
    action: explanation || (status === "validated_fraud"
      ? "Validated fraud alert routed to the receiving institution."
      : "Result retained in the shared validation history."),
    destinationHash: requiredString(row, "destination_account_hash") || requiredString(row, "destination_msisdn_hash"),
    corroboratingInstitutions,
    corroborationCount: requiredNumber(row, "corroboration_count") || corroboratingInstitutions.length,
    alertId: requiredString(row, "alert_id") || undefined,
    alertState: (["sent", "acknowledged", "actioned", "disputed"].includes(requiredString(row, "alert_state"))
      ? requiredString(row, "alert_state") : undefined) as Transaction["alertState"],
    alertType: (["hold", "advisory"].includes(requiredString(row, "alert_type"))
      ? requiredString(row, "alert_type") : undefined) as Transaction["alertType"],
  };
}

export async function loadDashboardData(
  bankTemplates: Bank[],
  access: { scope: PortalScope; institutionCode: string },
  signal?: AbortSignal,
): Promise<DashboardData> {
  const [institutionsPayload, standardPayload, knowledgePayload] = await Promise.all([
    fetchJson("/api/v1/institutions", { signal }),
    fetchJson("/api/v1/standard", { signal }),
    fetchJson("/api/v1/admin/kb", { signal }),
  ]);
  if (!Array.isArray(institutionsPayload)) {
    throw new Error("The backend returned an invalid institution list.");
  }

  const institutionByCode = new Map<string, Record<string, unknown>>();
  for (const value of institutionsPayload) {
    if (isRecord(value) && typeof value.code === "string") institutionByCode.set(value.code, value);
  }
  const banks = bankTemplates.map((bank) => {
    const institution = institutionByCode.get(bank.backendCode);
    return institution
      ? { ...bank, threshold: Math.round(requiredNumber(institution, "threshold") * 100) }
      : bank;
  });
  if (access.scope === "institution"
    && !banks.some((item) => item.backendCode === access.institutionCode)) {
    throw new Error("The authenticated institution is not available in this workspace.");
  }
  const historyInstitution = access.scope === "exchange" ? "*" : access.institutionCode;
  const historyPayload = await fetchJson(
    `/api/v1/history?institution=${encodeURIComponent(historyInstitution)}&limit=2000`,
    { signal },
  );
  const rows = new Map<string, Record<string, unknown>>();
  if (!isRecord(historyPayload) || !Array.isArray(historyPayload.history)) {
    throw new Error("The backend returned invalid institution history.");
  }
  for (const value of historyPayload.history) {
    if (!isRecord(value)) continue;
    const reportId = requiredString(value, "report_id");
    if (reportId) rows.set(reportId, value);
  }

  const riskCodes = parseRiskCodes(standardPayload);
  const riskCodeNames = new Map(riskCodes.map((item) => [item.code, item.label]));
  const codeToBankId = new Map(banks.map((bank) => [bank.backendCode, bank.id]));
  const transactions = [...rows.values()]
    .sort((a, b) => requiredString(b, "submitted_at").localeCompare(requiredString(a, "submitted_at")))
    .map((row) => transactionFromHistory(row, codeToBankId, riskCodeNames));

  return {
    banks,
    transactions,
    riskCodes,
    knowledgeBaseEntries: parseKnowledgeBase(knowledgePayload),
  };
}

export async function updateInstitutionThreshold(
  backendCode: string,
  thresholdPercent: number,
  signal?: AbortSignal,
) {
  await fetchJson("/api/v1/admin/config", {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ institution_thresholds: { [backendCode]: thresholdPercent / 100 } }),
    signal,
  });
}

export async function updateAlertState(
  alertId: string,
  state: "acknowledged" | "actioned" | "disputed",
  comment = "",
  signal?: AbortSignal,
) {
  await fetchJson(`/api/v1/alerts/${encodeURIComponent(alertId)}/state`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ state, comment }),
    signal,
  });
}

function parseDemoStream(payload: unknown): DemoStreamStatus {
  if (!isRecord(payload) || !Array.isArray(payload.events)) {
    throw new Error("The backend returned an invalid demo stream state.");
  }
  const events: DemoStreamEvent[] = payload.events.flatMap((value) => {
    if (!isRecord(value) || typeof value.event_offset !== "number") return [];
    const status = requiredString(value, "status");
    if (!["pending", "processed", "failed"].includes(status)) return [];
    return [{
      eventOffset: value.event_offset,
      topic: requiredString(value, "topic"),
      eventType: requiredString(value, "event_type"),
      source: requiredString(value, "source"),
      status: status as DemoStreamEvent["status"],
      reportId: requiredString(value, "report_id"),
      outcome: requiredString(value, "outcome"),
      error: requiredString(value, "error"),
      alertName: requiredString(value, "alert_name"),
      alertSeverity: requiredString(value, "alert_severity"),
      createdAt: requiredString(value, "created_at"),
    }];
  });
  return {
    enabled: payload.enabled === true,
    cadenceSeconds: requiredNumber(payload, "cadence_seconds"),
    nextOffset: requiredNumber(payload, "next_offset"),
    emittedSinceReset: requiredNumber(payload, "emitted_since_reset"),
    maxEvents: requiredNumber(payload, "max_events"),
    lastEmittedAt: requiredString(payload, "last_emitted_at"),
    updatedAt: requiredString(payload, "updated_at"),
    topic: requiredString(payload, "topic"),
    datasetBasis: requiredString(payload, "dataset_basis"),
    events,
  };
}

export async function loadDemoStream(signal?: AbortSignal) {
  return parseDemoStream(await fetchJson("/api/v1/admin/demo-stream", { signal }));
}

export async function setDemoStreamState(enabled: boolean, cadenceSeconds = 30, signal?: AbortSignal) {
  return parseDemoStream(await fetchJson("/api/v1/admin/demo-stream/state", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled, cadence_seconds: cadenceSeconds }),
    signal,
  }));
}

export async function emitDemoStreamEvent(signal?: AbortSignal) {
  await fetchJson("/api/v1/admin/demo-stream/emit", { method: "POST", signal });
}

export async function resetDemoStream(signal?: AbortSignal) {
  return parseDemoStream(await fetchJson("/api/v1/admin/demo-stream/reset", {
    method: "POST",
    signal,
  }));
}

export async function validateCsv(csv: string, signal?: AbortSignal) {
  const response = await apiFetch("/api/validate-csv", {
    method: "POST",
    headers: { "Content-Type": "text/csv" },
    body: csv,
    signal,
  });
  if (!response.headers.get("content-type")?.includes("application/json")) {
    throw new Error("The Go validation API is unavailable. Run npm run server.");
  }
  const payload: unknown = await response.json();
  if (!response.ok) {
    throw new Error(isRecord(payload) && typeof payload.error === "string"
      ? payload.error : `CSV validation failed (HTTP ${response.status}).`);
  }
  if (!isRecord(payload) || !Array.isArray(payload.validations)
    || !payload.validations.every(isValidation) || !isSummary(payload.summary)) {
    throw new Error("The validation API returned an unexpected response.");
  }
  return { validations: payload.validations, summary: payload.summary };
}
