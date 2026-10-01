export type PortalScope = "institution" | "exchange";
export type Tab = "outgoing" | "incoming" | "history" | "reports" | "knowledge" | "governance";
export type Outcome = "corroborated" | "below_threshold" | "needs_review"
  | "quarantined" | "retracted" | "expired" | "cleared";
export type InstitutionKind = "bank" | "mortgage" | "sacco" | "psp";
export interface Session {
  scope: PortalScope;
  bankId: string | null;
  email: string;
  displayName: string;
  institutionCode: string;
  institutionName: string;
  expiresAt: string;
}
export interface AuthSession {
  email: string;
  displayName: string;
  role: "institution" | "staff";
  institutionCode: string;
  institutionName: string;
  expiresAt: string;
}
export type UserAccessRequestStatus = "pending" | "approved" | "rejected";
export interface UserAccessRequest {
  requestId: string;
  institutionCode: string;
  institutionName: string;
  alias: string;
  email: string;
  status: UserAccessRequestStatus;
  requestedByEmail: string;
  requestedAt: string;
  reviewedByEmail: string;
  reviewedAt: string;
  reviewNote: string;
  admittedUserId: string;
  approvedPasswordTip: string;
}
export interface Source {
  name: string;
  type: string;
  method: string;
  status: string;
  cadence: string;
}
export interface Bank {
  id: string;
  backendCode: string;
  name: string;
  region: string;
  users: string;
  shortName: string;
  health: string;
  threshold: number;
  soc: string;
  /** Institution category, used to group and label choices on the portal sign-in screen. */
  kind?: InstitutionKind;
  connector: { endpoint: string; systems: string; latency: string; lastSync: string };
  inputSources: Source[];
}
export interface Transaction {
  key: string;
  id: string;
  sourceBank: string;
  destinationBank: string;
  customerRef: string;
  merchant: string;
  country: string;
  amount: string;
  score: number;
  flagSource: string;
  validationStatus: Outcome;
  riskCode: { code: string; label: string };
  evidence: string[];
  /** Validator reason codes such as CODE:ATO-460 or CORRO:destination. */
  reasonCodes?: string[];
  /** Evidence fields the reporting institution attached to the report. */
  evidenceFields?: Record<string, unknown>;
  action: string;
  /** Protected destination fingerprint (hashed account or MSISDN). Never a raw identifier. */
  destinationHash: string;
  /** Other institutions whose own reports shared this same fingerprint. */
  corroboratingInstitutions: string[];
  corroborationCount: number;
  lifecycleState?: "active" | "quarantined" | "retracted" | "expired" | "cleared";
  expiresAt?: string;
  alertId?: string;
  alertState?: "sent" | "acknowledged" | "actioned" | "disputed" | "retracted";
  alertType?: "review" | "advisory";
  alertOutcome?: "held" | "released" | "recovered";
  alertOutcomeNote?: string;
}
export interface Validation {
  transaction_id: string;
  reporting_bank: string;
  receiving_bank: string;
  customer_ref: string;
  currency: string;
  amount: string;
  confidence: number;
  status: Outcome;
  validated_by: string;
  risk_codes: { code: string; label: string }[];
  key_signals: string[];
  short_explanation: string;
  recommended_action: string;
  destination_hash?: string;
  corroborating_institutions?: string[];
  corroboration_count?: number;
}
export interface UploadSummary {
  total_rows: number;
  corroborated: number;
  below_threshold: number;
  needs_review: number;
  top_risk_codes: { code: string; count: number }[];
}
export interface Notification {
  id: number;
  institutionCode: string;
  eventType: string;
  recordId: string;
  createdAt: string;
  payload: Record<string, unknown>;
}
export interface RiskCodeReference {
  code: string;
  label: string;
  text: string;
}
export interface KnowledgeBaseEntry {
  artefactHash: string;
  listName: string;
  label: string;
  addedBy: string;
  addedAt: string;
}
export interface DemoStreamEvent {
  eventOffset: number;
  topic: string;
  eventType: string;
  source: string;
  status: "pending" | "processed" | "failed";
  reportId: string;
  outcome: string;
  error: string;
  alertName: string;
  alertSeverity: string;
  createdAt: string;
}
export interface DemoStreamStatus {
  enabled: boolean;
  cadenceSeconds: number;
  nextOffset: number;
  emittedSinceReset: number;
  retainedEvents: number;
  maxEvents: number;
  lastEmittedAt: string;
  updatedAt: string;
  topic: string;
  datasetBasis: string;
  events: DemoStreamEvent[];
  metrics: {
    signals: number;
    corroborated: number;
    awaiting: number;
    belowPolicy: number;
    quarantined: number;
    alerts: number;
    acknowledged: number;
    actioned: number;
    disputed: number;
    retracted: number;
    actionedValue: number;
    p95LatencyMs: number;
  };
}
export interface GuidedDemoStatus {
  runId: string;
  step: number;
  status: "ready" | "running" | "completed" | "failed";
  firstReportId: string;
  secondReportId: string;
  alertId: string;
  error: string;
  updatedAt: string;
  reportingInstitutionA: string;
  reportingInstitutionAName: string;
  reportingInstitutionB: string;
  reportingInstitutionBName: string;
  receivingInstitution: string;
  receivingInstitutionName: string;
}
export interface DashboardData {
  banks: Bank[];
  transactions: Transaction[];
  riskCodes: RiskCodeReference[];
  knowledgeBaseEntries: KnowledgeBaseEntry[];
  notifications: Notification[];
}
