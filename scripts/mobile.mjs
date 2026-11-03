// Mobile check for a running demo: opens each main page on an iPhone 13 and a
// Pixel 7 screen, fails if any page scrolls sideways, and saves a screenshot
// of each to docs/screenshots/mobile/. Uses the Chrome installed on this
// machine through playwright-core, so no browser download is needed.
//
//   npm run mobile                 check the banks, the director and Kifaru
//
// Set NCBA_URL, NETWORK_URL and KIFARU_WEB_URL to check other addresses, for
// example the hosted copies. KIFARU_DEMO_PASSWORD is the analyst password from
// the demo guide. Set CHROME_CHANNEL=chromium to use a downloaded Chromium.

import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, devices } from "playwright-core";

const env = (name, fallback) => (process.env[name] || fallback).replace(/\/$/, "");
const ncba = env("NCBA_URL", "http://127.0.0.1:9101");
const director = env("NETWORK_URL", "http://127.0.0.1:9100");
const kifaru = env("KIFARU_WEB_URL", "http://127.0.0.1:5173");
const password = process.env.KIFARU_DEMO_PASSWORD || "KifaruDemo2026!";
const outDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "docs", "screenshots", "mobile");

async function kifaruSignIn(page, url, email, institution) {
  await page.goto(url);
  await page.waitForSelector("#portal-email", { timeout: 60000 });
  await page.fill("#portal-email", email);
  await page.fill("#portal-password", password);
  if (institution) {
    const value = await page.$$eval("#portal-institution option",
      (options, wanted) => options.find((option) => option.textContent.includes(wanted))?.value, institution);
    await page.selectOption("#portal-institution", value);
  }
  await page.click(".portal-submit");
  await page.waitForSelector(".overview .trend-compare, .overview [role=alert]", { timeout: 60000 });
}

const pages = [
  ["bank-sign-in", async (page) => { await page.goto(`${ncba}/`); await page.waitForSelector("#login"); }],
  ["bank-customer", async (page) => {
    await page.goto(`${ncba}/`);
    await page.waitForSelector("#login");
    await page.selectOption('select[name="username"]', "peter");
    await page.fill('input[name="password"]', "bank1234");
    await page.click("#login button");
    await page.waitForSelector('input[name="account"]');
  }],
  ["bank-ops", async (page) => { await page.goto(`${ncba}/ops`); await page.waitForSelector(".topbar"); }],
  ["director", async (page) => { await page.goto(`${director}/`); await page.waitForSelector("[data-mode]"); }],
  ["kifaru-sign-in", async (page) => { await page.goto(`${kifaru}/`); await page.waitForSelector("#portal-email"); }],
  ["kifaru-overview", (page) => kifaruSignIn(page, `${kifaru}/`, "anthonyjordan@ncba.co.ke", "NCBA")],
  ["kifaru-staff-overview", (page) => kifaruSignIn(page, `${kifaru}/staff`, "anthonyjordan@kifaru.co.ke")],
];

await mkdir(outDir, { recursive: true });
const browser = await chromium.launch({ channel: process.env.CHROME_CHANNEL || "chrome", headless: true });
let failed = 0;
for (const device of ["iPhone 13", "Pixel 7"]) {
  console.log(device);
  for (const [name, open] of pages) {
    const context = await browser.newContext({ ...devices[device] });
    const page = await context.newPage();
    try {
      await open(page);
      await page.waitForLoadState("networkidle").catch(() => undefined);
      await page.evaluate(() => window.scrollTo(0, 0));
      const { scroll, width } = await page.evaluate(() => ({
        scroll: document.documentElement.scrollWidth, width: document.documentElement.clientWidth,
      }));
      const file = `${device.toLowerCase().replace(/\s+/g, "-")}-${name}.png`;
      await page.screenshot({ path: path.join(outDir, file) });
      if (scroll > width) throw new Error(`scrolls sideways by ${scroll - width}px`);
      console.log(`  ok    ${name}  (${width}px wide, ${file})`);
    } catch (error) {
      failed += 1;
      console.log(`  FAIL  ${name}: ${error.message.split("\n")[0]}`);
    } finally {
      await context.close();
    }
  }
}
await browser.close();
console.log(failed ? `\n${failed} check(s) failed` : "\nAll mobile checks passed");
process.exitCode = failed ? 1 : 0;
