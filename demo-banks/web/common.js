export const kes = (amount) => `KES ${Math.round(amount).toLocaleString("en-KE")}`;

export const clock = (value) => new Date(value).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function escapeHtml(value) {
  return String(value ?? "").replace(/[&"']/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[char]);
}

export async function api(path, { method = "GET", body, token } = {}) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw Object.assign(new Error(payload.error || `HTTP ${response.status}`), { payload, status: response.status });
  return payload;
}

/**
 * A per-browser device ID, kept in a cookie. Cookies ignore the port, so all
 * three banks on 127.0.0.1 see the same browser, as Kifaru's device check needs.
 */
export function deviceId() {
  const match = document.cookie.match(/(?:^|;\s*)demo-device-id=([^;]+)/);
  let id = match ? decodeURIComponent(match[1]) : "";
  if (!id) {
    try { id = localStorage.getItem("demo-device-id") || ""; } catch { id = ""; }
  }
  if (!id) id = `browser-${crypto.getRandomValues(new Uint32Array(1))[0].toString(16)}`;
  document.cookie = `demo-device-id=${encodeURIComponent(id)}; path=/; max-age=31536000; SameSite=Lax`;
  return id;
}

/** A Material Symbols glyph. The name is always a literal from our own code. */
export const icon = (name, extra = "") => `<span class="material-symbols-outlined ${extra}" aria-hidden="true">${name}</span>`;

/** Original abstract marks for the three simulated banks, not the real logos. */
const MARKS = {
  ncba: `<path d="M9 24V8l14 16V8" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>`,
  kcb: `<circle cx="16" cy="16" r="10" fill="none" stroke="currentColor" stroke-width="2.4"/><path d="M13 11v10M13 16l6-5M13 16l6 5" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>`,
  equity: `<path d="M5 17 16 7l11 10" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/><path d="M10 17.5V25h12v-7.5" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>`,
};

export function logoMark(id) {
  return `<span class="logo logo-${escapeHtml(id)}" aria-hidden="true"><svg viewBox="0 0 32 32">${MARKS[id] || MARKS.ncba}</svg></span>`;
}

export function header(config, subtitle, right = "") {
  document.documentElement.style.setProperty("--accent", config.accent);
  document.title = `${config.name} · ${subtitle}`;
  return `<header class="topbar"><div class="brand">${logoMark(config.id)}
    <span class="brand-text"><strong>${escapeHtml(config.name)}</strong><small>${escapeHtml(subtitle)}</small></span><span class="demo-chip" title="Simulation with synthetic data">Demo</span></div>
    <div class="topbar-right">${right}</div></header>`;
}
