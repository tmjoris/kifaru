// Breaks the demo banks on purpose, so you can show the tests catching it.
//
//   node scripts/demo-break.mjs threshold   NCBA stops blocking at 0.80 and blocks at 0.50
//   node scripts/demo-break.mjs password    the ops page leaks customer passwords again
//   node scripts/demo-break.mjs credit      the interbank credit accepts negative amounts
//   node scripts/demo-break.mjs restore     undo whatever was applied (git checkout of the two files)
//
// Use it on a throwaway branch: commit, push and open a pull request to watch
// the checks go red, then run `restore` and push to watch them go green.
import { readFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const root = fileURLToPath(new URL("../demo-banks/", import.meta.url));
const config = join(root, "config.go");
const bank = join(root, "bank.go");

const swap = (file, from, to) => {
  const text = readFileSync(file, "utf8");
  if (!text.includes(from)) throw new Error(`${file} no longer contains the text this break replaces; it may already be applied`);
  writeFileSync(file, text.replace(from, to));
};

const breaks = {
  threshold: () => {
    const text = readFileSync(config, "utf8");
    const start = text.indexOf('ID: "ncba"');
    const at = text.indexOf("BlockThreshold: 0.80", start);
    if (start < 0 || at < 0) throw new Error("could not find NCBA's block threshold");
    writeFileSync(config, text.slice(0, at) + "BlockThreshold: 0.50" + text.slice(at + "BlockThreshold: 0.80".length));
    return "NCBA now blocks at 0.50 instead of 0.80";
  },
  password: () => {
    swap(config, 'Password      string `json:"-"`', "Password      string");
    return "customer passwords are serialised again";
  },
  credit: () => {
    swap(bank, `	if request.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	account := b.accountByNumber`, `	b.mu.Lock()
	defer b.mu.Unlock()
	account := b.accountByNumber`);
    return "interbank credits accept zero and negative amounts";
  },
};

const action = process.argv[2];
if (action === "restore") {
  execFileSync("git", ["checkout", "--", "demo-banks/config.go", "demo-banks/bank.go"], { cwd: join(root, ".."), stdio: "inherit" });
  console.log("restored demo-banks/config.go and demo-banks/bank.go");
} else if (breaks[action]) {
  console.log(`Broken on purpose: ${breaks[action]()}`);
} else {
  console.error("usage: node scripts/demo-break.mjs threshold | password | credit | restore");
  process.exit(2);
}
