import { useState } from "react";
import type { Bank, InstitutionKind, Stage } from "../types";
import { Icon, Logo } from "./Shared";

const ROLE_OPTIONS: { id: Stage; label: string; icon: string }[] = [
  { id: "reporting", label: "Reporting bank", icon: "flag" },
  { id: "kifaru", label: "Kifaru exchange", icon: "hub" },
  { id: "receiving", label: "Receiving bank", icon: "shield" },
];

const PIPELINE = [
  ["Detect", "A bank spots fraud"],
  ["Share", "Kifaru shares a protected signal"],
  ["Match", "Another bank finds the same pattern"],
  ["Act", "Staff review or hold the transfer"],
];

export function PortalLogin({ banks, onEnterBank, onEnterKifaru }: {
  banks: Bank[];
  onEnterBank: (stage: Stage, bankId: string) => void;
  onEnterKifaru: () => void;
  onAddInstitution: (name: string, region: string, kind: InstitutionKind, stage: Stage) => void;
}) {
  const availableBanks = banks.filter((bank) => !bank.pending);
  const [role, setRole] = useState<Stage>("reporting");
  const [bankId, setBankId] = useState(availableBanks[0]?.id ?? banks[0]?.id ?? "");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [theme, setTheme] = useState(() =>
    document.documentElement.dataset.theme === "dark" ? "dark" : "light");

  function toggleTheme() {
    const next = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.dataset.theme = next;
    window.localStorage.setItem("kifaru-theme", next);
  }

  function signIn() {
    if (role === "kifaru") onEnterKifaru();
    else if (bankId) onEnterBank(role, bankId);
  }

  return <main className="portal-screen">
    <section className="portal-story" aria-label="What Kifaru does">
      <img src="/kifaru-network.svg"
        alt="A protected fraud signal moving between financial institutions through Kifaru" />
      <div className="portal-story-shade" />
      <div className="portal-story-brand"><Logo /><span>Kifaru</span></div>
      <div className="portal-story-copy">
        <h1>Stop fraud before the money moves again.</h1>
        <p>Kifaru lets banks share protected fraud signals. If one bank flags a device,
          account or phone number, another bank can catch the same pattern before paying out.</p>
        <ol className="portal-pipeline">
          {PIPELINE.map(([label, detail], index) =>
            <li key={label}>
              <span>{index + 1}</span>
              <div><strong>{label}</strong><small>{detail}</small></div>
            </li>,
          )}
        </ol>
      </div>
    </section>

    <section className="portal-access" aria-labelledby="portal-title">
      <button className="portal-theme-toggle" type="button" onClick={toggleTheme}
        aria-label={`Use ${theme === "dark" ? "light" : "dark"} theme`}>
        <Icon name={theme === "dark" ? "light_mode" : "dark_mode"} />
      </button>

      <div className="portal-access-inner">
        <header className="portal-header">
          <div className="portal-mobile-brand"><Logo /><span>Kifaru</span></div>
          <h2 id="portal-title">Sign in</h2>
          <p>Use your staff account to open your workspace.</p>
        </header>

        <form className="portal-form" onSubmit={(event) => { event.preventDefault(); signIn(); }}>
          <div className="portal-field">
            <label htmlFor="portal-email">Work email</label>
            <input id="portal-email" className="portal-control" type="email" autoComplete="email"
              placeholder="name@institution.co.ke" value={email}
              onChange={(event) => setEmail(event.target.value)} />
          </div>

          <div className="portal-field">
            <label htmlFor="portal-password">Password</label>
            <div className="portal-password">
              <input id="portal-password" className="portal-control"
                type={showPassword ? "text" : "password"} autoComplete="current-password"
                placeholder="Enter your password" value={password}
                onChange={(event) => setPassword(event.target.value)} />
              <button type="button" onClick={() => setShowPassword(!showPassword)}
                aria-label={showPassword ? "Hide password" : "Show password"}>
                <Icon name={showPassword ? "visibility_off" : "visibility"} />
              </button>
            </div>
          </div>

          <fieldset className="portal-fieldset">
            <legend>Workspace</legend>
            <div className="portal-role-options">
              {ROLE_OPTIONS.map((option) =>
                <button type="button" key={option.id}
                  className={`portal-role-option ${role === option.id ? "active" : ""}`}
                  aria-pressed={role === option.id} onClick={() => setRole(option.id)}>
                  <Icon name={option.icon} /><span>{option.label}</span>
                </button>,
              )}
            </div>
          </fieldset>

          {role !== "kifaru" && <div className="portal-field">
            <label htmlFor="portal-institution">Institution</label>
            <select id="portal-institution" className="portal-control"
              value={bankId} onChange={(event) => setBankId(event.target.value)}>
              {availableBanks.map((bank) =>
                <option value={bank.id} key={bank.id}>{bank.name}</option>,
              )}
            </select>
          </div>}

          <div className="portal-form-meta">
            <label><input type="checkbox" /> Keep me signed in</label>
            <button type="button">Forgot password?</button>
          </div>

          <button type="submit" className="btn primary portal-submit"
            disabled={!email.trim() || !password || (role !== "kifaru" && !bankId)}>
            Sign in
          </button>
        </form>

        <p className="portal-help"><Icon name="lock" /> Protected staff access</p>
      </div>
    </section>
  </main>;
}
