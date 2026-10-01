/**
 * Identifier columns in an uploaded risk log. Their values are replaced with
 * keyed hashes in the browser, so the API never receives raw customer data.
 */
export const IDENTIFIER_COLUMNS = [
  "customer_ref", "customer", "customer_name", "account_number",
  "destination_account", "destination_msisdn", "msisdn", "phone_number",
  "device_profile", "evidence_device_profile",
];

/**
 * Shared demo key so uploads from different browsers can still match. It is
 * public in this prototype; a real institution would hold its own key.
 */
export const DEMO_UPLOAD_KEY = import.meta.env?.VITE_UPLOAD_HASH_KEY || "kifaru-demo-upload-key";

/**
 * Strict RFC 4180 parser matching the API's Go CSV reader: a quote may only open
 * a field, a quoted field must be closed, and every row needs the header's width.
 * Anything else is refused before upload, so no row can hide inside another.
 */
export function parseCsv(text: string): string[][] {
  const input = text.startsWith("\uFEFF") ? text.slice(1) : text;
  const records: string[][] = [];
  let row: string[] = [];
  let field = "";
  let state: "start" | "unquoted" | "quoted" | "closed" = "start";
  let line = 1;
  const endField = () => {
    row.push(field);
    field = "";
    state = "start";
  };
  const endRow = () => {
    endField();
    records.push(row);
    row = [];
  };
  for (let index = 0; index < input.length; index += 1) {
    const char = input[index];
    if (state === "quoted") {
      if (char === '"' && input[index + 1] === '"') {
        field += '"';
        index += 1;
      } else if (char === '"') {
        state = "closed";
      } else {
        if (char === "\n") line += 1;
        field += char;
      }
      continue;
    }
    if (char === ",") {
      endField();
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && input[index + 1] === "\n") index += 1;
      endRow();
      line += 1;
    } else if (state === "closed") {
      throw new Error(`CSV line ${line}: unexpected text after a closing quote.`);
    } else if (char === '"') {
      if (state !== "start") throw new Error(`CSV line ${line}: a quote appears inside an unquoted field.`);
      state = "quoted";
    } else {
      field += char;
      state = "unquoted";
    }
  }
  if (state === "quoted") throw new Error(`CSV line ${line}: a quoted field is never closed.`);
  if (state !== "start" || row.length > 0) endRow();
  const rows = records.filter((record) => !(record.length === 1 && record[0] === ""));
  const width = rows[0]?.length ?? 0;
  rows.forEach((record, index) => {
    if (record.length !== width) {
      throw new Error(`CSV row ${index + 1} has ${record.length} fields; the header has ${width}.`);
    }
  });
  return rows;
}

export function toCsv(rows: string[][]): string {
  return rows.map((row) => row.map((value) => /[",\r\n]/.test(value)
    ? `"${value.replaceAll('"', '""')}"`
    : value).join(",")).join("\n") + "\n";
}

async function hmacHex(key: CryptoKey, value: string) {
  const signature = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(value));
  return [...new Uint8Array(signature)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** Replaces raw identifiers with `sha256:` HMAC digests and reports how many were hashed. */
export async function protectIdentifiers(csv: string, secret = DEMO_UPLOAD_KEY) {
  const rows = parseCsv(csv);
  if (rows.length < 2) return { csv, hashed: 0 };
  const targets = rows[0]
    .map((header, index) => IDENTIFIER_COLUMNS.includes(header.trim().toLowerCase()) ? index : -1)
    .filter((index) => index >= 0);
  if (!targets.length) return { csv, hashed: 0 };
  const key = await crypto.subtle.importKey(
    "raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"],
  );
  let hashed = 0;
  for (const row of rows.slice(1)) {
    for (const index of targets) {
      const value = (row[index] ?? "").trim();
      if (!value || value.startsWith("sha256:")) continue;
      row[index] = `sha256:${await hmacHex(key, value.replace(/\s+/g, "").toLowerCase())}`;
      hashed += 1;
    }
  }
  return { csv: toCsv(rows), hashed };
}
