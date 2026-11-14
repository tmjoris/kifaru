// Prints the server's own view of a load test: fetch /v1/admin/metrics before and after.
//
//   node load/metrics-diff.mjs https://kifaru-api.onrender.com snapshot > before.json
//   (run the load test)
//   node load/metrics-diff.mjs https://kifaru-api.onrender.com diff before.json
//
// Uses the staff session in load/tokens.json (made by load/make-tokens.mjs).
import { readFileSync } from "node:fs";
const [base, mode, beforeFile] = process.argv.slice(2);
const staff = JSON.parse(readFileSync(new URL("./tokens.json", import.meta.url))).find((t) => t.role === "staff");
const current = await (await fetch(`${base}/v1/admin/metrics`, { headers: { Authorization: `Bearer ${staff.token}` } })).json();
if (mode === "snapshot") { console.log(JSON.stringify(current)); process.exit(0); }
const before = JSON.parse(readFileSync(beforeFile, "utf8"));
const secs = current.uptime_seconds - before.uptime_seconds;
const delta = (group, key) => current[group][key] - before[group][key];
console.log(`window: ${secs}s  (server restarted: ${secs < 0})`);
console.log(`requests handled   ${delta("requests", "total")}   slow >1s ${delta("requests", "slow_over_1s")}   shed 503 ${delta("requests", "shed_503")}   in flight now ${current.requests.in_flight}`);
const throttledMs = Math.round(delta("cpu", "throttled_usec") / 1000), cpuMs = Math.round(delta("cpu", "used_usec") / 1000);
console.log(`cpu used           ${cpuMs} ms of ${Math.round(secs * 1000 * current.cpu.quota_cores)} ms available (${Math.round(100 * cpuMs / Math.max(1, secs * 1000 * current.cpu.quota_cores))}%)`);
console.log(`cpu THROTTLED      ${throttledMs} ms in ${delta("cpu", "throttled_periods")} of ${delta("cpu", "periods")} periods (${Math.round(100 * delta("cpu", "throttled_periods") / Math.max(1, delta("cpu", "periods")))}%)`);
console.log(`db pool            ${delta("database_pool", "acquires_that_waited")} of ${delta("database_pool", "acquires")} acquires waited; total wait ${delta("database_pool", "acquire_wait_seconds").toFixed(1)}s; ${current.database_pool.busy}/${current.database_pool.max} busy now`);
console.log(`memory             ${current.memory.container_used_mib} of ${current.memory.container_limit_mib} MiB; go heap ${current.memory.go_heap_mib} MiB; goroutines ${current.memory.goroutines}; gc pause ${delta("memory", "gc_pause_total_ms")} ms`);
console.log(`streams            ${current.streams.open} open; turned away ${delta("streams", "turned_away")}; cut loose ${delta("streams", "cut_loose")}`);
console.log(`caches             hits ${delta("caches", "hits")}  misses ${delta("caches", "misses")}`);
