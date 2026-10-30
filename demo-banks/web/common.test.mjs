import test from "node:test";
import assert from "node:assert/strict";

// common.js touches the DOM only inside functions, so it can be imported here.
const { escapeHtml, kes, clock, icon, logoMark, header } = await import("./common.js");

const attacks = [
  `<script>alert(1)</script>`,
  `"><img src=x onerror=alert(1)>`,
  `' onmouseover='alert(1)`,
  `</div><iframe src="javascript:alert(1)">`,
  `&lt;already escaped&gt;`,
];

test("escapeHtml leaves no markup or quote characters behind", () => {
  for (const attack of attacks) {
    const out = escapeHtml(attack);
    assert.doesNotMatch(out, /[<>"']/, `unescaped characters in ${out}`);
  }
  assert.equal(escapeHtml(`<b>"Tom" & 'Jerry'</b>`), "&lt;b&gt;&quot;Tom&quot; &amp; &#39;Jerry&#39;&lt;/b&gt;");
});

test("escapeHtml copes with missing and non-string values", () => {
  assert.equal(escapeHtml(null), "");
  assert.equal(escapeHtml(undefined), "");
  assert.equal(escapeHtml(0), "0");
  assert.equal(escapeHtml(12.5), "12.5");
});

test("header() escapes a hostile bank name, subtitle and id", () => {
  globalThis.document = { documentElement: { style: { setProperty() {} } }, title: "" };
  const html = header({ id: `x"><script>`, name: `<img src=x onerror=alert(1)>`, accent: "#000" }, `<script>alert(2)</script>`, "");
  assert.doesNotMatch(html, /<script>|<img src=x|onerror=alert\(1\)>/, html);
  assert.match(html, /&lt;img/);
  delete globalThis.document;
});

test("logoMark falls back to a known mark for an unknown bank", () => {
  assert.match(logoMark("not-a-bank"), /<svg/);
  assert.doesNotMatch(logoMark(`"><script>`), /<script>/);
});

test("icon and clock produce stable output", () => {
  assert.match(icon("lock"), /class="material-symbols-outlined /);
  assert.match(clock("2026-10-24T12:34:56Z"), /^\d{2}:\d{2}:\d{2}$/);
});

test("kes groups thousands and rounds to whole shillings", () => {
  assert.equal(kes(85000).replace(/ /g, " "), "KES 85,000");
  assert.equal(kes(412499.6).replace(/ /g, " "), "KES 412,500");
  assert.equal(kes(0), "KES 0");
});

test("every page script parses", async () => {
  const { readdirSync } = await import("node:fs");
  const { execFileSync } = await import("node:child_process");
  const { fileURLToPath } = await import("node:url");
  const { join } = await import("node:path");
  const here = fileURLToPath(new URL(".", import.meta.url));
  for (const file of readdirSync(here).filter((name) => name.endsWith(".js"))) {
    execFileSync(process.execPath, ["--check", join(here, file)]);
  }
});
