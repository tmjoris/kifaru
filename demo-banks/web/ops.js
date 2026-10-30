import { api, clock, escapeHtml, header, icon, kes } from "./common.js";

const root = document.querySelector("#app");
const safe = (value) => escapeHtml(value || "—");
const ACCOUNT = "0114417";
let config;
let state;
let lastSignature = "";

async function refresh() {
  state = await api("api/ops/state");
  const signature = JSON.stringify(state);
  if (signature === lastSignature) return;
  lastSignature = signature;
  render();
}

const empty = (text) => `<p class="empty">${icon("inbox")}<span>${text}</span></p>`;
const tierLabel = (tier) => String(tier || "alert").replaceAll("_", " ");

function kpi(name, label, value, tone = "") {
  return `<div class="kpi ${tone}"><span class="kpi-icon">${icon(name)}</span><span class="kpi-label">${label}</span><strong>${value}</strong></div>`;
}

function transferCard(t) {
  const blocked = t.status === "blocked";
  const reasons = (t.reasons || []).map(reason => `<span class="${/kifaru/i.test(reason) ? "kifaru" : ""}">${safe(reason)}</span>`).join("");
  const kifaru = t.kifaru
    ? `<div class="kifaru-result">${icon("shield")}<span><strong>Kifaru report: ${safe(t.kifaru.result?.status)}</strong><small>${safe(t.kifaru.error || t.kifaru.result?.report_id || "Submitted")}</small></span></div>`
    : "";
  return `<div class="txn ${blocked ? "blocked" : ""}">
    <div class="txn-head"><div><strong>${safe(t.customer_name)}</strong><small>${safe(t.to_name)} · ${kes(t.amount)} · ${safe(t.to_bank_name)}</small></div><span class="tag ${blocked ? "bad" : "ok"}">${safe(t.status)}</span></div>
    <div class="gauge"><span>Bank fraud score</span>
      <div class="track"><i style="width:${Math.min(100, t.score * 100)}%"></i><b style="left:${Math.min(100, t.threshold * 100)}%" title="Block threshold"></b></div>
      <strong>${t.score.toFixed(2)} <em>/ ${t.threshold.toFixed(2)}</em></strong></div>
    ${reasons ? `<div class="chips">${reasons}</div>` : ""}${kifaru}</div>`;
}

function alertCard(a) {
  return `<article class="alert ${safe(a.alert_tier)}"><div class="alert-head">${icon("notification_important")}<strong>${tierLabel(a.alert_tier)}</strong><small>${safe(a.reporter_name)}</small></div>
    <p>${safe(a.explanation)}</p><small>${safe(a.state)} · ${safe(a.outcome)}</small></article>`;
}

function render() {
  const transfers = state.transfers || [];
  const alerts = state.alerts || [];
  const credits = state.credits || [];
  const blocked = transfers.filter(t => t.status === "blocked").length;
  const connector = `<label class="switch"><input id="connector" type="checkbox" ${state.connector ? "checked" : ""}><span class="switch-track"></span><span>Kifaru connector <b>${state.connector ? "on" : "off"}</b></span></label>`;
  const events = (state.events || []).slice().reverse().map(e => `<li class="${safe(e.tone)}"><time>${clock(e.at)}</time><span>${safe(e.text)}</span></li>`).join("");
  const mail = (state.mail || []).map(m => `<article class="mail"><div><strong>${safe(m.subject)}</strong><small>${safe(m.from_bank)} · ${clock(m.at)}</small></div><p>${safe(m.body)}</p></article>`).join("");

  const scrollY = window.scrollY;
  root.innerHTML = `<div class="app dark bank-${safe(config.id)}">${header(config, "Operations console", connector)}
    <main class="ops">
      <div class="sim-banner">${icon("science")}<span><strong>Simulated operations</strong> Independent bank ledger and bank-specific fraud rules. Kifaru is an optional connector. Demo run key for Kifaru's risk check: <code>${safe(state.run_key || "")}</code></span></div>
      <div class="kpis">${kpi("account_tree", "Transfers scored", transfers.length)}${kpi("block", "Blocked", blocked, blocked ? "alert" : "")}${kpi("notifications_active", "Kifaru alerts", alerts.length, alerts.length ? "info" : "")}${kpi("south_west", "Credits in", credits.length)}</div>
      <div class="ops-grid">
        <div class="stack">
          <section class="panel"><div class="panel-head"><div><span class="eyebrow">Real-time scoring</span><h2>Bank fraud engine</h2></div><span class="threshold">Block at ${state.block_threshold.toFixed(2)}</span></div>
            ${transfers.map(transferCard).join("") || empty("Waiting for simulated transfers.")}</section>
          <section class="panel"><div class="panel-head"><div><span class="eyebrow">Live</span><h2>Local operations feed</h2></div></div><ul class="events">${events || "<li>Waiting for activity.</li>"}</ul></section>
        </div>
        <div class="stack">
          <section class="panel"><div class="panel-head"><div><span class="eyebrow">From the network</span><h2>Incoming Kifaru alerts</h2></div></div>
            ${alerts.map(alertCard).join("") || empty("No incoming Kifaru alerts.")}
            <div class="actions"><button id="hold" class="btn btn-secondary">${icon("lock")} Place account hold</button><button id="return" class="btn btn-secondary">${icon("undo")} Return available funds</button></div></section>
          <section class="panel"><div class="panel-head"><div><span class="eyebrow">Settlement</span><h2>Incoming interbank credits</h2></div></div>
            ${credits.map(c => `<div class="credit"><span class="activity-icon incoming">${icon("south_west")}</span><span><strong>${safe(c.from_bank_name)}</strong><small>${safe(c.state)} · ${clock(c.at)}</small></span><strong class="credit-amount">${kes(c.amount)}</strong></div>`).join("") || empty("No incoming credits.")}</section>
          <section class="panel"><div class="panel-head"><div><span class="eyebrow">Shared</span><h2>Fraud-team mailbox</h2></div></div>${mail || empty("Mailbox is empty.")}</section>
        </div>
      </div>
    </main></div>`;
  window.scrollTo(0, scrollY);

  root.querySelector("#connector").onchange = async (e) => { await api("api/ops/connector", { method: "POST", body: { enabled: e.target.checked } }); await refresh(); };
  root.querySelector("#hold").onclick = async () => { await api("api/ops/hold", { method: "POST", body: { account: ACCOUNT } }); await refresh(); };
  root.querySelector("#return").onclick = async () => { await api("api/ops/return", { method: "POST", body: { account: ACCOUNT } }); await refresh(); };
}

api("api/config").then(v => { config = v; return refresh(); }).catch(e => {
  root.innerHTML = `<main class="page"><div class="banner bad">${icon("error")}<span>${escapeHtml(e.message)}</span></div></main>`;
});
setInterval(() => refresh().catch(() => {}), 1500);
