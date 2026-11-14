// Signs in a handful of demo accounts and writes tokens.json for the load tools.
//
//   node load/make-tokens.mjs https://kifaru-api.onrender.com > tokens.json
//
// Every virtual user shares these few sessions, so only a few slow password checks
// are made. Demo accounts and password: see DEMO-GUIDE.md (override the password
// with KIFARU_DEMO_PASSWORD).
const base = (process.argv[2] || "http://127.0.0.1:8000").replace(/\/$/, "");
const password = process.env.KIFARU_DEMO_PASSWORD || "KifaruDemo2026!";
const accounts = [
  { email: "anthonyjordan@kifaru.co.ke", inst: "*", role: "staff" },
  { email: "anthonyjordan@ncba.co.ke", inst: "bank_a", role: "institution" },
  { email: "anthonyjordan@kcb.co.ke", inst: "bank_b", role: "institution" },
  { email: "anthonyjordan@equitybank.co.ke", inst: "psp_c", role: "institution" },
];
const tokens = [];
for (const account of accounts) {
  const body = { email: account.email, password, keep_signed_in: true };
  if (account.role === "institution") body.institution_code = account.inst;
  for (let attempt = 0; attempt < 5; attempt += 1) { // login is rate-limited on purpose; retry politely
    const response = await fetch(`${base}/v1/auth/login`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (response.status === 503) { await new Promise((r) => setTimeout(r, 4000)); continue; }
    if (!response.ok) { console.error(`${account.email}: HTTP ${response.status}`); break; }
    const payload = await response.json();
    tokens.push({ token: payload.access_token, csrf: payload.csrf_token, role: account.role, inst: account.inst });
    break;
  }
}
if (!tokens.length) { console.error("no accounts could sign in"); process.exit(1); }
console.error(`${tokens.length} sessions`);
console.log(JSON.stringify(tokens));
