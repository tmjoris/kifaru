// Smoke test for a running demo: is every service up and does the main
// journey work? Runs on Windows, macOS and Linux with Node 22 or later.
//
//   npm run smoke                  check the banks and the director
//   npm run smoke -- --kifaru      also check the Kifaru API and web app
//
// It signs in once at each bank from a device called "smoke-check" and makes no
// payments, so it is safe to run in the middle of a rehearsal. Set NCBA_URL,
// KCB_URL, EQUITY_URL, NETWORK_URL, KIFARU_API_URL or KIFARU_WEB_URL if your
// services are not on the default ports.

const withKifaru = process.argv.includes("--kifaru");
const env = (name, fallback) => (process.env[name] || fallback).replace(/\/$/, "");
const banks = [
  { id: "ncba", url: env("NCBA_URL", "http://127.0.0.1:9101"), user: "grace", name: "Grace Achieng" },
  { id: "kcb", url: env("KCB_URL", "http://127.0.0.1:9102"), user: "faith", name: "Faith Njeri" },
  { id: "equity", url: env("EQUITY_URL", "http://127.0.0.1:9103"), user: "kiprono", name: "J. Kiprono" },
];
const director = env("NETWORK_URL", "http://127.0.0.1:9100");
const kifaruApi = env("KIFARU_API_URL", "http://127.0.0.1:8000");
const kifaruWeb = env("KIFARU_WEB_URL", "http://127.0.0.1:5173");

let failed = 0;
const check = async (name, run) => {
  try {
    const detail = await run();
    console.log(`  ok    ${name}${typeof detail === "string" && detail ? `  (${detail})` : ""}`);
  } catch (error) {
    failed += 1;
    const reason = error.message === "fetch failed" ? "could not connect, is the service running?" : error.message;
    console.log(`  FAIL  ${name}: ${reason}`);
  }
};
const get = async (url, init) => {
  const response = await fetch(url, { signal: AbortSignal.timeout(8000), ...init });
  if (!response.ok) throw new Error(`${url} answered ${response.status}`);
  return response;
};

console.log("Demo banks");
for (const bank of banks) {
  await check(`${bank.id} customer page`, async () => {
    const page = await (await get(`${bank.url}/`)).text();
    if (!page.includes('id="app"')) throw new Error("page has no #app root");
  });
  await check(`${bank.id} operations page`, () => get(`${bank.url}/ops`));
  await check(`${bank.id} stylesheet and script`, async () => {
    await get(`${bank.url}/static/style.css`);
    await get(`${bank.url}/static/customer.js`);
  });
  await check(`${bank.id} sign-in and account`, async () => {
    const login = await (await get(`${bank.url}/api/login`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: bank.user, password: "bank1234", device: "smoke-check" }),
    })).json();
    const me = await (await get(`${bank.url}/api/me`, { headers: { Authorization: `Bearer ${login.token}` } })).json();
    if (me.name !== bank.name) throw new Error(`signed in as ${me.name}, expected ${bank.name}`);
    return `balance KES ${Math.round(me.balance).toLocaleString("en-KE")}`;
  });
  await check(`${bank.id} operations state`, async () => {
    const state = await (await get(`${bank.url}/api/ops/state`)).json();
    if (JSON.stringify(state).includes("bank1234")) throw new Error("ops state leaks a customer password");
    return `connector ${state.connector ? "on" : "off"}`;
  });
}

console.log("Director");
await check("director page", () => get(`${director}/`));
await check("director state", async () => {
  const state = await (await get(`${director}/api/state`)).json();
  if (state.banks?.length !== 3 || state.steps?.length !== 6) throw new Error("expected 3 banks and 6 steps");
  return `mode ${state.mode}, step ${state.step} of ${state.steps.length}`;
});

if (withKifaru) {
  console.log("Kifaru");
  await check("API health", async () => {
    const health = await (await get(`${kifaruApi}/health`)).json();
    return JSON.stringify(health).slice(0, 60);
  });
  await check("web app staff page", () => get(`${kifaruWeb}/staff`));
}

console.log(failed ? `\n${failed} check(s) failed` : "\nAll checks passed");
process.exit(failed ? 1 : 0);
