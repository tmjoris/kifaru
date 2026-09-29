import type { Bank } from "./types";

export const initialBanks: Bank[] = [
      {
        id: "ncba",
        backendCode: "bank_a",
        name: "NCBA",
        region: "Kenya",
        users: "SOC-1 analysts",
        shortName: "NC",
        health: "healthy",
        threshold: 82,
        soc: "Connected to Sentinel, card processor, mobile banking, and dispute workflow.",
        connector: {
          endpoint: "soc.ncba.example/stream",
          systems: "Sentinel, card processor, mobile banking, dispute workflow",
          latency: "1.8s",
          lastSync: "11:47 AM"
        },
        inputSources: [
          { name: "SOC Sentinel connector", type: "SOC connector", method: "Streaming API", status: "Connected", cadence: "Realtime" },
          { name: "Card authorization feed", type: "Transaction feed", method: "REST API", status: "Connected", cadence: "Realtime" },
          { name: "Mobile banking telemetry", type: "Device signal", method: "Webhook", status: "Connected", cadence: "Near realtime" },
          { name: "Dispute workflow", type: "Case system", method: "Batch API", status: "Connected", cadence: "Every 15 min" }
        ]
      },
      {
        id: "kcb",
        backendCode: "bank_b",
        name: "KCB",
        region: "East Africa",
        users: "Fraud ops team",
        shortName: "KC",
        health: "warning",
        threshold: 76,
        soc: "Connected to Splunk and core banking. Mobile telemetry lag detected.",
        connector: {
          endpoint: "soc.kcb.example/stream",
          systems: "Splunk, core banking, mobile telemetry",
          latency: "4.6s",
          lastSync: "11:46 AM"
        },
        inputSources: [
          { name: "Splunk SOC connector", type: "SOC connector", method: "Streaming API", status: "Warning", cadence: "Realtime" },
          { name: "Core banking transfer feed", type: "Transaction feed", method: "SFTP batch", status: "Connected", cadence: "Every 5 min" },
          { name: "Agency banking terminal feed", type: "Channel signal", method: "REST API", status: "Connected", cadence: "Realtime" },
          { name: "Mobile telemetry", type: "Device signal", method: "Webhook", status: "Delayed", cadence: "Near realtime" }
        ]
      },
      {
        id: "equity",
        backendCode: "psp_c",
        name: "Equity",
        region: "Pan-African banking",
        users: "Managed SOC",
        shortName: "EQ",
        health: "healthy",
        threshold: 88,
        soc: "Connected to SIEM, ATM switch, sanctions screen, and case management.",
        connector: {
          endpoint: "soc.equity.example/stream",
          systems: "SIEM, ATM switch, sanctions screen, case management",
          latency: "2.1s",
          lastSync: "11:47 AM"
        },
        inputSources: [
          { name: "SIEM event connector", type: "SOC connector", method: "Streaming API", status: "Connected", cadence: "Realtime" },
          { name: "ATM switch feed", type: "Transaction feed", method: "Message queue", status: "Connected", cadence: "Realtime" },
          { name: "Sanctions screen", type: "Risk enrichment", method: "REST API", status: "Connected", cadence: "On demand" },
          { name: "Case management sync", type: "Case system", method: "Graph-style API", status: "Connected", cadence: "Every 10 min" }
        ]
      },
      {
        id: "im",
        backendCode: "sacco_d",
        name: "I&M",
        region: "Kenya and regional subsidiaries",
        users: "Digital risk team",
        shortName: "IM",
        health: "healthy",
        threshold: 84,
        soc: "Connected to card authorization, mobile banking, transaction monitoring, and case workflow.",
        connector: {
          endpoint: "soc.im.example/stream",
          systems: "Card authorization, mobile banking, transaction monitor, case workflow",
          latency: "2.4s",
          lastSync: "11:48 AM"
        },
        inputSources: [
          { name: "Card authorization stream", type: "Transaction feed", method: "Streaming API", status: "Connected", cadence: "Realtime" },
          { name: "Transaction monitor", type: "Rules engine", method: "REST API", status: "Connected", cadence: "Realtime" },
          { name: "Mobile banking events", type: "Device signal", method: "Webhook", status: "Connected", cadence: "Near realtime" },
          { name: "Case workflow", type: "Case system", method: "Batch API", status: "Connected", cadence: "Every 15 min" }
        ]
      }
    ];

export const riskCodeCatalog = [
      { code: "IP-401", label: "Changing IP or location", text: "Login geography, IP, or network path changed abnormally before the transaction." },
      { code: "VEL-429", label: "Velocity or volume spike", text: "Many transfers, deposits, attempts, or cash-outs happened in a short window." },
      { code: "DEV-403", label: "Device or auth anomaly", text: "New device, degraded reputation, SIM swap, credential stuffing, or password reset signal." },
      { code: "BEN-409", label: "Beneficiary mismatch", text: "New, disputed, high-risk, or recently changed beneficiary details." },
      { code: "AML-451", label: "Mule or AML pattern", text: "Mule proximity, structuring, cash-out behavior, or linked-account network risk." },
      { code: "DOC-422", label: "Invoice or vendor anomaly", text: "Business email compromise, invoice hash, vendor-bank change, or document mismatch." },
      { code: "CRY-418", label: "Crypto or forex risk", text: "Crypto gateway, forex dealer, remittance, or high-risk offshore rail." },
      { code: "GEN-400", label: "General fraud signal", text: "Fallback group for high-risk fraud evidence that does not match a specific category." }
    ];

export const knowledgeBase = {
      articles: [
        {
          title: "Cross-bank visibility rules",
          text: "A bank can only view fraud reports where it is the reporting bank or receiving bank. The reporting bank is always shown for audit context."
        },
        {
          title: "Fraud score interpretation",
          text: "Kifaru normalizes each bank's fraud definition into one centralized validation standard before alerting the receiving bank."
        },
        {
          title: "Incoming transaction handling",
          text: "Incoming flagged transfers should be checked for mule-account indicators, beneficiary age, unusual sender patterns, and linked case history."
        },
        {
          title: "Receiving-bank alerting",
          text: "When Kifaru validates fraud, the receiving bank gets an alert and can view the related validation history."
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
          title: "AI model evidence store",
          text: "Risk factors, score history, linked entities, velocity features, and analyst feedback from the selected tenant."
        },
        {
          title: "Policy and regulatory guidance",
          text: "Bank-specific thresholds, escalation SLAs, customer-contact rules, and evidence retention controls."
        }
      ]
    };
