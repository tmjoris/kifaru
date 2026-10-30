import { api, clock, escapeHtml, icon } from "./common.js";

const root = document.querySelector("#app");
const safe = escapeHtml;
let state = null;
let problem = "";
let framesBuilt = "";
let shellBuilt = false;
const painted = { stage: "", live: "" };

const KIFARU_MARK = `<span class="logo logo-kifaru" aria-hidden="true"><svg viewBox="0 0 32 32"><path d="M9 7v18M9 17l11-10M9 17l11 8" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/></svg></span>`;

function shell() {
  shellBuilt = true;
  painted.stage = painted.live = "";
  root.innerHTML = `<div class="app dark kifaru">
    <header class="topbar"><div class="brand">${KIFARU_MARK}<span class="brand-text"><strong>Kifaru class demo</strong><small>Three independent simulated bank platforms · local classroom environment</small></span></div>
      <div class="topbar-right"><a class="btn btn-ghost" id="staff-link" target="_blank" rel="noopener">Open Kifaru staff ${icon("open_in_new")}</a></div></header>
    <main class="director">
      <div class="sim-banner">${icon("science")}<span><strong>Simulation only</strong> Synthetic customers, logins, accounts and transfers. The interbank switch is local and no real money moves.</span></div>
      <section class="stage" id="stage"></section>
      <section class="live" id="live"></section>
      <section class="frames" id="frames"></section>
      <p class="fine">Customer pages accept only synthetic demo identities and passcodes and are visibly marked as simulations. Keep real credentials out of this exercise.</p>
    </main></div>`;
}

function stage() {
  const steps = state.steps || [];
  const done = Math.min(state.step, steps.length);
  const finished = state.step >= steps.length;
  const percent = steps.length ? Math.round((done / steps.length) * 100) : 0;
  return `<div class="stage-main panel"><span class="eyebrow">Scenario</span><h1>Account takeover across three banks</h1>
      <p class="lede">Each bank keeps its own customer session, ledger and fraud threshold. Bank connectors submit protected indicators to Kifaru's API.</p>
      <div class="mode" role="group" aria-label="Defence mode"><button data-mode="without" class="${state.mode === "without" ? "active" : ""}">${icon("lock_open")} Without Kifaru</button><button data-mode="with" class="with ${state.mode === "with" ? "active" : ""}">${icon("shield")} With Kifaru</button></div>
      <div class="actions"><button id="prepare" class="btn btn-secondary">${icon("history")} Prepare reporter history</button>
        <button id="next" class="btn btn-primary" ${state.busy || finished ? "disabled" : ""}>${state.busy ? "Working…" : finished ? "Replay complete" : `Next attacker step ${icon("arrow_forward")}`}</button></div>
      <small class="hint">Manual: open each bank's customer and operations links. Autopilot: advance one real transaction per click.</small>
      ${problem ? `<p class="banner bad">${icon("error")}<span>${safe(problem)}</span></p>` : ""}</div>
    <div class="stage-steps panel"><div class="panel-head"><div><span class="eyebrow">Progress</span><h2>${done} of ${steps.length} steps</h2></div><span class="percent">${percent}%</span></div>
      <div class="progress"><i style="width:${percent}%"></i></div>
      <ol class="steps">${steps.map((s, i) => `<li class="${i < state.step ? "done" : i === state.step ? "next" : ""}"><span class="step-dot">${i < state.step ? icon("check") : i + 1}</span><span class="step-actor">${safe(s.actor)}</span><span class="step-title">${safe(s.title)}</span></li>`).join("")}</ol>
      <p class="switch-note"><strong>Interbank switch</strong> · ${(state.switch || []).length} settled or reversed messages · <a href="${safe(state.kifaru_web)}/staff" target="_blank" rel="noopener">Kifaru API-backed outcomes</a></p></div>`;
}

function live() {
  const attacker = (state.attacker || []).map(x => `<li class="${safe(x.tone)}"><time>${clock(x.at)}</time><span>${safe(x.text)}</span></li>`).join("") || "<li>Waiting for first step.</li>";
  const wire = (state.switch || []).map(x => `<li><time>${clock(x.at)}</time><span>${safe(x.text)} · ${safe(x.status)}</span></li>`).join("") || "<li>Switch is idle.</li>";
  const mail = (state.mail || []).map(m => `<article class="mail"><div><strong>${safe(m.subject)}</strong></div><p>${safe(m.body)}</p></article>`).join("") || `<p class="empty">${icon("inbox")}<span>No messages.</span></p>`;
  return `<article class="panel terminal-panel"><div class="panel-head"><div><span class="eyebrow">Red team</span><h2>Attacker activity</h2></div><span class="rec"><i></i>Live</span></div><ol class="events console" id="console">${attacker}</ol></article>
    <article class="panel"><div class="panel-head"><div><span class="eyebrow">Settlement</span><h2>Interbank switch and fraud mailbox</h2></div></div><ul class="events">${wire}</ul><h3>Unread-style shared mailbox</h3>${mail}</article>`;
}

function frames() {
  return state.banks.map(bank => `<section class="frame" style="--frame:${safe(bank.accent)}">
    <header><span class="dots" aria-hidden="true"><i></i><i></i><i></i></span><strong>${safe(bank.name)} <small>independent platform</small></strong>
      <nav><a href="${safe(bank.url)}/" target="_blank" rel="noopener">Customer ${icon("north_east")}</a><a href="${safe(bank.url)}/ops" target="_blank" rel="noopener">Operations ${icon("north_east")}</a></nav></header>
    <iframe title="${safe(bank.name)} simulated operations" src="${safe(bank.url)}/ops"></iframe></section>`).join("");
}

function paint(id, html) {
  if (painted[id] === html) return false;
  painted[id] = html;
  root.querySelector(`#${id}`).innerHTML = html;
  return true;
}

function render() {
  if (!state) return;
  if (!shellBuilt) shell();
  root.querySelector("#staff-link").href = `${state.kifaru_web}/staff`;
  paint("stage", stage());
  const before = root.querySelector("#console");
  const pinned = !before || before.scrollTop + before.clientHeight >= before.scrollHeight - 24;
  const top = before?.scrollTop ?? 0;
  if (paint("live", live())) {
    const consoleEl = root.querySelector("#console");
    consoleEl.scrollTop = pinned ? consoleEl.scrollHeight : top;
  }
  // The bank iframes are built once, so they are not reloaded on every poll.
  const signature = JSON.stringify(state.banks);
  if (signature !== framesBuilt) { framesBuilt = signature; root.querySelector("#frames").innerHTML = frames(); }
}

async function mutate(path, body) {
  problem = "";
  try { state = await api(path, { method: "POST", body }); render(); }
  catch (e) { problem = e.message; await refresh(); }
}

async function refresh() {
  try { state = await api("api/state"); render(); }
  catch (e) {
    root.innerHTML = `<main class="page"><div class="banner bad">${icon("error")}<span>${safe(e.message)}. Start all three banks and the interbank switch.</span></div></main>`;
    framesBuilt = "";
    shellBuilt = false;
  }
}

root.addEventListener("click", (event) => {
  const mode = event.target.closest("[data-mode]");
  if (mode) return mutate("api/mode", { mode: mode.dataset.mode });
  if (event.target.closest("#next")) return mutate("api/autopilot/next", {});
  if (event.target.closest("#prepare")) return mutate("api/prepare", {});
});

refresh();
setInterval(refresh, 1200);
