import { api, deviceId, escapeHtml, header, icon, kes } from "/static/common.js";

const root = document.querySelector("#app");
const safe = escapeHtml;
let config;
let me;
let token = sessionStorage.getItem("simulated-bank-session");
let notice = null; // { text, kind: "ok" | "bad" }
let noticeFresh = false;
let noticeTimer;
let activeView = "overview";
let hideBalance = false;
let review = null; // the transfer waiting for the customer's confirmation
let receipt = null; // the sent transfer, with its reference
let sending = false;
let animate = true;
let draft = { bank: "", account: "0114417", name: "J. Kiprono", amount: "85000" };
const call = (path, options) => api(path, { ...options, token });

const PRODUCTS = { ncba: "NCBA NOW", kcb: "KCB Mobile", equity: "Equity Online" };
const productName = () => PRODUCTS[config.id] || config.name;

const COPY = {
  ncba: {
    title: "Money,<br>made calm.",
    lead: "Your accounts, payments and statements in one quiet, considered place.",
    points: [["schedule", "Available around the clock"], ["swap_horiz", "Send to other banks in moments"], ["shield_lock", "Every session protected"]],
  },
  kcb: {
    title: "Banking that<br>moves with you.",
    lead: "Check your balance, send money and keep track of every shilling from anywhere.",
    points: [["bolt", "Fast interbank transfers"], ["insights", "Clear view of your activity"], ["shield_lock", "Protected sign-in"]],
  },
  equity: {
    title: "Built for how<br>you earn.",
    lead: "Manage your account and move money with the confidence of a bank that knows your day.",
    points: [["account_balance", "Your account, always in reach"], ["payments", "Send money across banks"], ["shield_lock", "Security checks on every payment"]],
  },
};

/** Decorative scene on the sign-in panel; one per bank. */
function art(id) {
  const scenes = {
    ncba: `<defs><linearGradient id="sun" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#f0d596"/><stop offset="1" stop-color="#b9854a"/></linearGradient></defs>
      <circle cx="430" cy="270" r="112" fill="url(#sun)"/>
      <g fill="none" stroke="#f0d596" stroke-opacity=".22"><circle cx="430" cy="270" r="150"/><circle cx="430" cy="270" r="190"/><circle cx="430" cy="270" r="232"/><circle cx="430" cy="270" r="276"/></g>
      <path d="M0 410C120 350 230 392 340 360S530 330 640 372V520H0Z" fill="#3b2d25"/>
      <path d="M0 450C150 410 270 462 390 432S560 420 640 440V520H0Z" fill="#241a15"/>
      <g fill="#120d0a" transform="translate(372 -6)"><path d="M104 470c3-40 8-70 18-98l5 2c-8 28-12 56-14 96Z"/><ellipse cx="128" cy="368" rx="76" ry="12"/><ellipse cx="86" cy="380" rx="46" ry="8"/><ellipse cx="170" cy="380" rx="40" ry="7"/></g>`,
    kcb: `<defs><linearGradient id="lime" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#b6f08a"/><stop offset="1" stop-color="#3fbf6d"/></linearGradient></defs>
      <circle cx="450" cy="230" r="96" fill="url(#lime)" opacity=".95"/>
      <g fill="none" stroke="#fff" stroke-opacity=".16"><circle cx="450" cy="230" r="136"/><circle cx="450" cy="230" r="178"/><circle cx="450" cy="230" r="222"/></g>
      <path d="M0 330C110 290 210 372 320 332S520 292 640 336V520H0Z" fill="#0d7a49"/>
      <path d="M0 384C120 346 230 424 350 386S540 350 640 390V520H0Z" fill="#075c3a"/>
      <path d="M0 440C130 408 250 470 380 440S560 416 640 446V520H0Z" fill="#04402a"/>`,
    equity: `<polygon points="-40,520 330,170 700,520" fill="#c41230"/>
      <polygon points="60,520 330,262 600,520" fill="#8f0e25"/>
      <polygon points="170,520 330,364 490,520" fill="#2a2a2e"/>
      <g fill="none" stroke="#fff" stroke-opacity=".14"><polygon points="-120,520 330,90 780,520"/><polygon points="-200,520 330,10 860,520"/></g>
      <circle cx="500" cy="150" r="46" fill="#ff5a70" opacity=".9"/>`,
  };
  return `<svg class="login-scene" viewBox="0 0 640 520" preserveAspectRatio="xMidYMax slice" aria-hidden="true">${scenes[id] || ""}</svg>`;
}

function setNotice(text, kind) {
  notice = { text, kind };
  noticeFresh = true;
  clearTimeout(noticeTimer);
  noticeTimer = undefined;
}

function loginView() {
  const product = productName();
  const copy = COPY[config.id];
  root.innerHTML = `<div class="app bank-${safe(config.id)}">${header(config, product)}
    <main class="login-stage">
      <section class="login-art">${art(config.id)}
        <div class="login-copy"><span class="eyebrow">${safe(product)}</span><h1>${copy.title}</h1><p>${copy.lead}</p>
          <ul class="login-points">${copy.points.map(([name, text]) => `<li>${icon(name)}<span>${safe(text)}</span></li>`).join("")}</ul></div>
      </section>
      <form class="login-card" id="login">
        <span class="badge-icon">${icon("lock")}</span>
        <div><span class="eyebrow">Personal banking</span><h2>Sign in</h2><p class="sub">Choose your customer profile to continue.</p></div>
        <label class="field"><span>Customer</span><select name="username" required>${config.customers.map(c => `<option value="${safe(c.username)}">${safe(c.name)} · ${safe(c.account_masked)}</option>`).join("")}</select></label>
        <label class="field"><span>Password</span><input name="password" type="password" placeholder="Enter your password" autocomplete="current-password" required></label>
        <button class="btn btn-primary btn-block">Sign in ${icon("arrow_forward")}</button>
        ${notice ? `<div class="banner ${safe(notice.kind)}" role="alert">${icon("error")}<span>${safe(notice.text)}</span></div>` : ""}
        <p class="login-foot">${icon("verified_user")} Simulation environment · synthetic customers only</p>
      </form>
    </main></div>`;
  root.querySelector("#login").onsubmit = async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    event.currentTarget.querySelector("button").disabled = true;
    try {
      const result = await api("/api/login", { method: "POST", body: {
        username: form.get("username"), password: form.get("password"), device: deviceId(),
      } });
      token = result.token;
      sessionStorage.setItem("simulated-bank-session", token);
      notice = null;
      animate = true;
      await refresh();
    } catch (error) { setNotice(error.message, "bad"); loginView(); }
  };
}

async function refresh() {
  if (!token) return loginView();
  try { me = await call("/api/me"); dashboard(); }
  catch (error) {
    token = ""; sessionStorage.removeItem("simulated-bank-session"); receipt = null;
    // Say why the session ended, unless a notice already explains it.
    if (!notice && error.status === 401 && error.message !== "Please sign in again.") setNotice(error.message, "bad");
    loginView();
  }
}

const shortDate = (at) => new Date(at).toLocaleDateString("en-KE", { day: "numeric", month: "short" });

function activityRows(items, incoming, limit) {
  if (!items?.length) return `<div class="empty-activity">${icon("receipt_long")}<span>No ${incoming ? "incoming payments" : "transfers"} yet</span></div>`;
  return items.slice().reverse().slice(0, limit).map(item => {
    const title = incoming ? `${safe(item.from_bank_name)} transfer` : `To ${safe(item.to_name || item.to_bank_name)}`;
    const status = incoming ? item.state : item.status;
    const sub = `${safe(incoming ? item.from_bank_name : item.to_bank_name)} · ${shortDate(item.at)}${item.ref ? ` · ${safe(item.ref)}` : ""}`;
    const tone = status === "blocked" ? "bad" : status === "completed" ? "ok" : "";
    return `<div class="activity-row"><span class="activity-icon ${incoming ? "incoming" : "outgoing"}">${icon(incoming ? "south_west" : "north_east")}</span>
      <span class="activity-main"><strong>${title}</strong><small>${sub}</small></span>
      <strong class="activity-amount ${incoming ? "positive" : ""}">${incoming ? "+" : "−"}${kes(item.amount)}</strong><span class="tag ${tone}">${safe(status)}</span></div>`;
  }).join("");
}

const NAV = [
  { id: "overview", icon: "space_dashboard", label: "Overview" },
  { id: "transfers", icon: "sync_alt", label: "Transfers" },
  { id: "account", icon: "account_circle", label: "My account" },
];
const HEADINGS = {
  overview: (first) => [`Hello, ${first}`, "Your accounts at a glance."],
  transfers: () => ["Transfers", "Review your recent payments."],
  account: () => ["Account details", "Your personal account information."],
};

function balanceCard() {
  return `<article class="balance">
    <div class="balance-top"><span class="balance-label">Total available balance</span>
      <button class="eye" id="toggle-balance" aria-label="${hideBalance ? "Show" : "Hide"} balance" aria-pressed="${hideBalance}">${icon(hideBalance ? "visibility_off" : "visibility")}</button></div>
    <div class="balance-amount">${hideBalance ? "KES ••••••" : kes(me.balance)}</div>
    <div class="balance-bottom"><span class="balance-holder">${safe(me.name)}</span><span class="account-chip">${icon("account_balance_wallet")}${safe(me.account_masked)}</span></div>
  </article>`;
}

function statTiles() {
  const sent = (me.transfers || []).filter(t => t.status !== "blocked").reduce((sum, t) => sum + t.amount, 0);
  const received = (me.credits || []).reduce((sum, c) => sum + c.amount, 0);
  const blocked = (me.transfers || []).filter(t => t.status === "blocked").length;
  const tile = (name, label, value, tone = "") => `<div class="stat ${tone}"><span class="stat-icon">${icon(name)}</span><span class="stat-label">${label}</span><strong>${value}</strong></div>`;
  return `<div class="stats">${tile("north_east", "Money out", kes(sent))}${tile("south_west", "Money in", kes(received))}${tile("gpp_maybe", "Blocked payments", String(blocked), blocked ? "alert" : "")}</div>`;
}

function quickActions() {
  const action = (attr, name, title, sub) => `<button ${attr}><span class="quick-icon">${icon(name)}</span><strong>${title}</strong><small>${sub}</small></button>`;
  return `<div class="quick">${action("data-scroll-transfer", "send", "Send money", "To a bank account")}${action('data-view="transfers"', "receipt_long", "Transactions", "View account activity")}${action('data-view="account"', "manage_accounts", "Account details", "View your information")}</div>`;
}

function accountDetails() {
  const row = (label, value) => `<div><dt>${label}</dt><dd>${value}</dd></div>`;
  return `<article class="panel account-details"><div class="panel-head"><div><span class="eyebrow">Account profile</span><h2>Personal account</h2></div><span class="account-chip solid">Active</span></div>
    <dl>${row("Account holder", safe(me.name))}${row("Account number", safe(me.account_masked))}${row("Customer reference", safe(me.customer_ref))}${row("Account type", "Current account")}${row("Account status", `<i class="status-dot"></i> Active`)}</dl></article>`;
}

function activityPanel() {
  const all = activeView === "transfers";
  return `<article class="panel"><div class="panel-head"><div><span class="eyebrow">Account activity</span><h2>${all ? "Transfer history" : "Recent transactions"}</h2></div>
      ${all ? "" : `<button class="text-button" data-view="transfers">View all ${icon("arrow_forward")}</button>`}</div>
    <div class="activity-list">${activityRows(me.transfers, false, all ? 50 : 6)}</div></article>`;
}

function sendPanel() {
  const banks = config.banks.filter(b => b.id !== config.id);
  const selected = banks.some(b => b.id === draft.bank) ? draft.bank : banks[0]?.id;
  return `<article class="panel send" id="transfer-card"><span class="badge-icon">${icon("payments")}</span><span class="eyebrow">Move money</span><h2>Send money</h2><p class="sub">Transfer funds to an account at another bank.</p>
    <form class="form" id="transfer">
      <label class="field"><span>Beneficiary bank</span><select name="bank" required>${banks.map(b => `<option value="${safe(b.id)}" ${b.id === selected ? "selected" : ""}>${safe(b.name)}</option>`).join("")}</select></label>
      <label class="field"><span>Account number</span><input name="account" value="${safe(draft.account)}" inputmode="numeric" autocomplete="off" required></label>
      <label class="field"><span>Account name</span><input name="name" value="${safe(draft.name)}" autocomplete="off" required></label>
      <label class="field"><span>Amount to send</span><input name="amount" type="number" min="100" step="100" value="${safe(draft.amount)}" required><small class="hint">Available balance: ${kes(me.balance)}</small></label>
      <div class="note">${icon("info")}<span>Transfers are subject to account and security checks.</span></div>
      <button class="btn btn-primary btn-block">Continue ${icon("arrow_forward")}</button>
    </form></article>`;
}

function snapshotPanel() {
  const line = (label, value) => `<div class="summary-line"><span>${label}</span><strong>${value}</strong></div>`;
  return `<article class="panel snapshot"><span class="eyebrow">Account snapshot</span>${line("Account status", `<i class="status-dot"></i> Active`)}${line("Account number", safe(me.account_masked))}${line("Account type", "Current account")}${line("Currency", "KES · Kenyan shilling")}</article>`;
}

function reviewSheet() {
  if (!review) return "";
  const bank = config.banks.find(b => b.id === review.bank);
  const row = (label, value) => `<div><dt>${label}</dt><dd>${value}</dd></div>`;
  return `<div class="sheet-backdrop" id="sheet-backdrop"><div class="sheet" role="dialog" aria-modal="true" aria-labelledby="sheet-title">
    <span class="badge-icon">${icon("payments")}</span><span class="eyebrow">Review transfer</span><h2 id="sheet-title">Send ${kes(review.amount)}?</h2>
    <dl>${row("From", `${safe(me.name)} · ${safe(me.account_masked)}`)}${row("To", safe(review.name))}${row("Bank", safe(bank?.name || review.bank.toUpperCase()))}${row("Account", safe(review.account))}${row("Amount", `<strong>${kes(review.amount)}</strong>`)}</dl>
    <div class="sheet-actions"><button class="btn btn-secondary" data-cancel ${sending ? "disabled" : ""}>Cancel</button><button class="btn btn-primary" data-confirm ${sending ? "disabled" : ""}>${sending ? "Sending…" : "Confirm and send"}</button></div>
  </div></div>`;
}

function receiptSheet() {
  if (!receipt) return "";
  const bank = config.banks.find(b => b.id === receipt.bank);
  const row = (label, value) => `<div><dt>${label}</dt><dd>${value}</dd></div>`;
  const sentAt = new Date(receipt.at).toLocaleString("en-KE", { dateStyle: "medium", timeStyle: "short" });
  return `<div class="sheet-backdrop" id="receipt-backdrop"><div class="sheet" role="dialog" aria-modal="true" aria-labelledby="receipt-title">
    <span class="badge-icon">${icon("task_alt")}</span><span class="eyebrow">Transfer receipt</span><h2 id="receipt-title">${kes(receipt.amount)} sent</h2>
    <dl>${row("Reference", `<strong>${safe(receipt.ref)}</strong>`)}${row("Date", safe(sentAt))}${row("From", `${safe(me.name)} · ${safe(me.account_masked)}`)}${row("To", safe(receipt.name))}${row("Bank", safe(bank?.name || receipt.bank.toUpperCase()))}${row("Account", safe(receipt.account))}${row("Amount", `<strong>${kes(receipt.amount)}</strong>`)}</dl>
    <div class="sheet-actions single"><button class="btn btn-primary" data-done>Done</button></div>
  </div></div>`;
}

function toast() {
  if (!notice) return "";
  const fresh = noticeFresh ? "fresh" : "";
  noticeFresh = false;
  return `<div class="toast ${safe(notice.kind)} ${fresh}" role="status">${icon(notice.kind === "bad" ? "gpp_bad" : "check_circle")}<span>${safe(notice.text)}</span></div>`;
}

function dashboard() {
  const [title, subtitle] = HEADINGS[activeView](me.name.split(" ")[0]);
  const initials = me.name.split(" ").map(p => p[0]).slice(0, 2).join("");
  const today = new Date().toLocaleDateString("en-KE", { weekday: "long", day: "numeric", month: "long" });
  const enter = animate ? "enter" : "";
  animate = false;
  const scrollY = window.scrollY;
  root.innerHTML = `<div class="app bank-${safe(config.id)}">${header(config, productName(), `<span class="secure-chip">${icon("lock")} Secure session</span><button id="logout" class="btn btn-ghost">Sign out</button>`)}
    <div class="shell ${enter}">
      <aside class="side-nav"><div class="nav-profile"><span class="avatar">${safe(initials)}</span><span><strong>${safe(me.name)}</strong><small>Personal account</small></span></div>
        <span class="nav-label">Your banking</span>
        ${NAV.map(n => `<button class="nav-item ${activeView === n.id ? "active" : ""}" data-view="${n.id}">${icon(n.icon)}${n.label}</button>`).join("")}
        <div class="nav-help">${icon("support_agent")}<strong>Need help?</strong><small>Contact customer care.</small></div>
      </aside>
      <main class="main">
        <div class="page-head"><div><span class="eyebrow">${safe(config.name)} · ${safe(me.account_masked)}</span><h1>${safe(title)}</h1><p>${subtitle}</p></div><div class="page-date">${icon("calendar_today")}${today}</div></div>
        <div class="content ${activeView === "account" ? "account-view" : ""}">
          <section class="column">${balanceCard()}${statTiles()}${quickActions()}${activeView === "account" ? accountDetails() : activityPanel()}
            <article class="panel"><div class="panel-head"><div><span class="eyebrow">Money in</span><h2>Incoming payments</h2></div></div><div class="activity-list">${activityRows(me.credits, true, 6)}</div></article>
          </section>
          <aside class="column">${sendPanel()}${snapshotPanel()}</aside>
        </div>
        <footer class="page-foot">${safe(config.name)} · Online banking · Simulation with synthetic data</footer>
      </main>
    </div>${reviewSheet()}${receiptSheet()}${toast()}</div>`;
  window.scrollTo(0, scrollY);
  bindDashboard();
}

function bindDashboard() {
  root.querySelectorAll("[data-view]").forEach(button => button.addEventListener("click", () => { activeView = button.dataset.view; animate = true; dashboard(); }));
  root.querySelectorAll("[data-scroll-transfer]").forEach(button => button.addEventListener("click", () => root.querySelector("#transfer-card")?.scrollIntoView({ behavior: "smooth", block: "start" })));
  root.querySelector("#toggle-balance").onclick = () => { hideBalance = !hideBalance; dashboard(); };
  root.querySelector("#logout").onclick = async () => {
    try { await call("/api/logout", { method: "POST" }); } catch {}
    token = ""; sessionStorage.removeItem("simulated-bank-session"); notice = null; review = null; receipt = null; loginView();
  };
  if (receipt) {
    const close = () => { receipt = null; dashboard(); };
    root.querySelector("[data-done]").onclick = close;
    root.querySelector("#receipt-backdrop").addEventListener("click", (event) => { if (event.target.id === "receipt-backdrop") close(); });
  }

  const form = root.querySelector("#transfer");
  form.addEventListener("input", () => { draft = Object.fromEntries(new FormData(form)); });
  form.onsubmit = (event) => {
    event.preventDefault();
    const data = new FormData(form);
    draft = Object.fromEntries(data);
    review = { bank: data.get("bank"), account: data.get("account"), name: data.get("name"), amount: Number(data.get("amount")) };
    dashboard();
    root.querySelector("[data-confirm]")?.focus();
  };

  if (!review) {
    if (notice && !noticeTimer) {
      noticeTimer = setTimeout(() => { notice = null; noticeTimer = undefined; root.querySelector(".toast")?.remove(); }, 7000);
    }
    return;
  }
  root.querySelector("[data-cancel]").onclick = () => { review = null; dashboard(); };
  root.querySelector("#sheet-backdrop").addEventListener("click", (event) => { if (event.target.id === "sheet-backdrop" && !sending) { review = null; dashboard(); } });
  root.querySelector("[data-confirm]").onclick = confirmTransfer;
}

async function confirmTransfer() {
  const sent = review;
  sending = true;
  dashboard();
  try {
    const result = await call("/api/transfers", { method: "POST", body: { bank: sent.bank, account: sent.account, name: sent.name, amount: sent.amount } });
    if (result.status === "blocked") setNotice(`Transfer blocked by ${config.name}'s own fraud checks. You have been signed out. Reference ${result.ref}.`, "bad");
    else receipt = { ...sent, ref: result.ref, at: result.at };
  } catch (error) { setNotice(error.message, "bad"); }
  sending = false;
  review = null;
  await refresh();
  root.querySelector("[data-done]")?.focus();
}

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && review && !sending) { review = null; dashboard(); }
  else if (event.key === "Escape" && receipt) { receipt = null; dashboard(); }
});

api("/api/config").then(value => { config = value; return refresh(); }).catch(error => {
  root.innerHTML = `<main class="page"><div class="banner bad">${icon("error")}<span>${safe(error.message)}. Start this simulated bank service and reload.</span></div></main>`;
});
setInterval(() => {
  const focused = document.activeElement;
  if (token && me && !review && !receipt && !sending && !focused?.matches("input, select, textarea")) refresh();
}, 2500);
