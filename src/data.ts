import type { Bank, InstitutionKind, Source } from "./types";
import directory from "../backend/data/kenyan_banks.json" with { type: "json" };

interface DirectoryEntry {
  code: string;
  id: string;
  name: string;
  legal_name: string;
  ref: string;
  type: string;
  threshold: number;
}

/** Illustrative connector set-ups, rotated across banks. None describes a real bank's systems. */
const connectorProfiles: { users: string; systems: string; latency: string; inputSources: Source[] }[] = [
  {
    users: "SOC analysts",
    systems: "SIEM, card processor, mobile banking, dispute workflow",
    latency: "1.8s",
    inputSources: [
      { name: "SOC SIEM connector", type: "SOC connector", method: "Streaming API", status: "Synthetic", cadence: "Realtime" },
      { name: "Card authorization feed", type: "Transaction feed", method: "REST API", status: "Synthetic", cadence: "Realtime" },
      { name: "Mobile banking telemetry", type: "Device signal", method: "Webhook", status: "Synthetic", cadence: "Near realtime" },
    ],
  },
  {
    users: "Fraud operations",
    systems: "Core banking, agency banking, mobile telemetry",
    latency: "2.6s",
    inputSources: [
      { name: "Core banking transfer feed", type: "Transaction feed", method: "SFTP batch", status: "Synthetic", cadence: "Every 5 min" },
      { name: "Agency banking terminal feed", type: "Channel signal", method: "REST API", status: "Synthetic", cadence: "Realtime" },
      { name: "Mobile telemetry", type: "Device signal", method: "Webhook", status: "Synthetic", cadence: "Near realtime" },
    ],
  },
  {
    users: "Managed SOC",
    systems: "SIEM, ATM switch, sanctions screen, case management",
    latency: "2.1s",
    inputSources: [
      { name: "SIEM event connector", type: "SOC connector", method: "Streaming API", status: "Synthetic", cadence: "Realtime" },
      { name: "ATM switch feed", type: "Transaction feed", method: "Message queue", status: "Synthetic", cadence: "Realtime" },
      { name: "Case management sync", type: "Case system", method: "Batch API", status: "Synthetic", cadence: "Every 10 min" },
    ],
  },
  {
    users: "Digital risk team",
    systems: "Card authorization, internet banking, transaction monitor",
    latency: "2.4s",
    inputSources: [
      { name: "Card authorization stream", type: "Transaction feed", method: "Streaming API", status: "Synthetic", cadence: "Realtime" },
      { name: "Transaction monitor", type: "Rules engine", method: "REST API", status: "Synthetic", cadence: "Realtime" },
      { name: "Internet banking events", type: "Device signal", method: "Webhook", status: "Synthetic", cadence: "Near realtime" },
    ],
  },
];

export const kenyanBanks: DirectoryEntry[] = directory.institutions;

/** Illustrative mobile money set-up. It does not describe any provider's real systems. */
const mobileMoneyProfile = {
  users: "Mobile money risk desk",
  systems: "Wallet ledger, agent cash-out network, SIM swap register",
  latency: "1.2s",
  inputSources: [
    { name: "Wallet transaction stream", type: "Transaction feed", method: "Streaming API", status: "Synthetic", cadence: "Realtime" },
    { name: "Agent cash-out feed", type: "Channel signal", method: "Message queue", status: "Synthetic", cadence: "Realtime" },
    { name: "SIM swap register", type: "Device signal", method: "REST API", status: "Synthetic", cadence: "On demand" },
  ],
};

const regions: Record<InstitutionKind, string> = {
  bank: "Licensed commercial bank",
  mortgage: "Mortgage finance institution",
  psp: "Mobile money provider",
  sacco: "Deposit-taking SACCO",
};

/**
 * Every licensed bank in Kenya (37 commercial banks and HFC, the mortgage finance
 * institution) and the two largest mobile money providers, M-Pesa and Airtel Money,
 * from backend/data/kenyan_banks.json. The names are real; all reports, alerts and
 * connector details shown for them are synthetic.
 */
export const initialBanks: Bank[] = kenyanBanks.map((entry, index) => {
  const kind: InstitutionKind = entry.type === "mortgage" || entry.type === "psp" ? entry.type : "bank";
  const profile = kind === "psp" ? mobileMoneyProfile : connectorProfiles[index % connectorProfiles.length];
  return {
    id: entry.id,
    backendCode: entry.code,
    name: entry.name,
    region: regions[kind],
    users: profile.users,
    shortName: entry.ref,
    health: "healthy",
    kind,
    threshold: Math.round(entry.threshold * 100),
    soc: `Synthetic demo feed. ${entry.legal_name} has not supplied or reviewed any data shown here.`,
    connector: {
      endpoint: `soc.${entry.id}.example/stream`,
      systems: profile.systems,
      latency: profile.latency,
      lastSync: "Synthetic",
    },
    inputSources: profile.inputSources,
  };
});

export const riskCodeCatalog = [
  { code: "IP-401", label: "Shared device across unrelated customers", text: "A protected device appeared across unrelated customer activity." },
  { code: "IP-402", label: "Recent SIM swap", text: "A SIM change occurred shortly before the reported activity." },
  { code: "IP-403", label: "Emulator or rooted device", text: "The reporting institution observed an emulator or tampered device." },
  { code: "IP-404", label: "Foreign or anonymising network", text: "The session used an unusual country, VPN, proxy, or Tor route." },
  { code: "VEL-429", label: "Transaction burst", text: "Many transfers or attempts occurred within one hour." },
  { code: "VEL-430", label: "New beneficiary used quickly", text: "A beneficiary was added and used within minutes." },
  { code: "VEL-431", label: "Structuring below a threshold", text: "Payment amounts repeatedly stayed just below an institution limit." },
  { code: "MUL-440", label: "New account with high fan-in", text: "A new account received funds from many unrelated senders." },
  { code: "MUL-441", label: "Near-total flow-through", text: "Most incoming value moved out again." },
  { code: "MUL-442", label: "Short dwell time", text: "Funds left the receiving account within ten minutes." },
  { code: "MUL-443", label: "Reused cash-out number", text: "A protected cash-out MSISDN appeared across institutions." },
  { code: "BEN-450", label: "High-value first beneficiary payment", text: "A new beneficiary received an unusually large first transfer." },
  { code: "BEN-451", label: "Many senders to one destination", text: "Unrelated senders converged on one protected destination." },
  { code: "ATO-460", label: "New device and beneficiary change", text: "A new device was followed by a new beneficiary." },
  { code: "ATO-461", label: "Credential change in an unusual session", text: "Credentials changed during an off-hours or otherwise unusual session." },
];

export const knowledgeBase = {
      articles: [
        {
          title: "Cross-bank visibility rules",
          text: "A bank can only view fraud reports where it is the reporting bank or receiving bank. The reporting bank is always shown for audit context."
        },
        {
          title: "Shared policy score",
          text: "Kifaru applies one deterministic signal policy to structured evidence before routing an alert to the receiving institution."
        },
        {
          title: "Incoming transaction handling",
          text: "Incoming flagged transfers should be checked for mule-account indicators, beneficiary age, unusual sender patterns, and linked case history."
        },
        {
          title: "Receiving-bank alerting",
          text: "When a signal meets policy and has an eligible independent match, the receiving institution gets an alert and chooses its response."
        }
      ],
      playbooks: [
        {
          title: "Account takeover",
          text: "Lock digital channel, challenge customer identity, review device fingerprint, and check recent beneficiary changes."
        },
        {
          title: "Mule account risk",
          text: "Inspect inbound velocity, cash-out behavior, related accounts, and previous fraud-network proximity."
        },
        {
          title: "Card compromise",
          text: "Freeze card, compare merchant cluster, validate card-present signals, and notify card operations."
        },
        {
          title: "False-positive review",
          text: "Confirm customer context, release held transaction when safe, and submit feedback to model governance."
        }
      ],
      sources: [
        {
          title: "Bank SOC connector",
          text: "SIEM alerts, transaction monitor events, auth logs, device telemetry, and case-management outcomes."
        },
        {
          title: "Institution evidence store",
          text: "Risk factors, score history, linked entities, velocity features, and analyst feedback from the selected tenant."
        },
        {
          title: "Policy and regulatory guidance",
          text: "Bank-specific thresholds, escalation SLAs, customer-contact rules, and evidence retention controls."
        }
      ]
    };
