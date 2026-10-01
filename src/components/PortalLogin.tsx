import { useState } from "react";
import type { Bank } from "../types";
import { Icon, Logo } from "./Shared";

export function PortalLogin({ banks, staff, onEnterBank, onEnterKifaru }: {
  banks: Bank[];
  staff: boolean;
  onEnterBank: (email: string, password: string, bankId: string, keepSignedIn: boolean) => Promise<void>;
  onEnterKifaru: (email: string, password: string, keepSignedIn: boolean) => Promise<void>;
}) {
  const [bankId, setBankId] = useState(banks[0]?.id ?? "");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [keepSignedIn, setKeepSignedIn] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  const [signingIn, setSigningIn] = useState(false);
  const [error, setError] = useState("");
  const [theme, setTheme] = useState(() =>
    document.documentElement.dataset.theme === "dark" ? "dark" : "light");

  function toggleTheme() {
    const next = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.dataset.theme = next;
    window.localStorage.setItem("kifaru-theme", next);
  }

  async function signIn() {
    setSigningIn(true);
    setError("");
    try {
      if (staff) await onEnterKifaru(email.trim(), password, keepSignedIn);
      else if (bankId) await onEnterBank(email.trim(), password, bankId, keepSignedIn);
    } catch (signInError) {
      setError(signInError instanceof Error ? signInError.message : "Sign-in failed.");
    } finally {
      setSigningIn(false);
    }
  }

  return <main className="portal-screen">
    <aside className="portal-identity" aria-label="Kifaru">
      <Logo />
      <span>Kifaru</span>
      <small>Shared fraud signal exchange</small>
    </aside>

    <header className="portal-masthead">
      <div className="portal-mobile-brand"><Logo /><span>Kifaru</span></div>
      <p><span className="portal-status-dot" /> Exchange online</p>
      <button className="portal-theme-toggle" type="button" onClick={toggleTheme}
        aria-label={`Use ${theme === "dark" ? "light" : "dark"} theme`}>
        <Icon name={theme === "dark" ? "light_mode" : "dark_mode"} />
      </button>
    </header>

    <section className="portal-access" aria-labelledby="portal-title">
      <div className="portal-access-inner">
        <header className="portal-header">
          <p>{staff ? "Kifaru operations" : "Institution access"}</p>
          <h1 id="portal-title">{staff ? "Enter staff operations" : "Enter the exchange"}</h1>
          <span>{staff ? "Use your Kifaru staff account." : "Use the account issued by your institution."}</span>
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

          {!staff && <div className="portal-field">
            <label htmlFor="portal-institution">Institution</label>
            <select id="portal-institution" className="portal-control"
              value={bankId} onChange={(event) => setBankId(event.target.value)}>
              {banks.map((bank) =>
                <option value={bank.id} key={bank.id}>{bank.name}</option>,
              )}
            </select>
          </div>}

          <div className="portal-form-meta">
            <label><input type="checkbox" checked={keepSignedIn}
              onChange={(event) => setKeepSignedIn(event.target.checked)} /> Keep me signed in</label>
            <span>Demo accounts only</span>
          </div>

          {error && <p className="portal-auth-error" role="alert">{error}</p>}
          <button type="submit" className="portal-submit"
            disabled={signingIn || !email.trim() || !password || (!staff && !bankId)}>
            {signingIn ? "Signing in..." : "Open workspace"}
          </button>
        </form>

        <p className="portal-help"><Icon name="lock" /> Access is logged and monitored.</p>
        <a className="portal-route-link" href={staff ? "/" : "/staff"}>
          {staff ? "Sign in through an institution" : "Kifaru staff sign in"}
        </a>
      </div>
    </section>

    <section className="portal-signal" aria-labelledby="signal-title">
      <div className="signal-receipt">
        <header>
          <div>
            <p>Protected signal</p>
            <h2 id="signal-title">One fingerprint. Two institutions.</h2>
          </div>
          <span className="signal-match"><Icon name="check_circle" /> Match found</span>
        </header>

        <div className="signal-route" aria-label="A fraud signal matched across two banks">
          <div className="signal-node">
            <span>Reporting institution</span>
            <strong>Bank A</strong>
            <small>Flagged 08:42:16</small>
          </div>
          <div className="signal-path">
            <i />
            <div><Logo /><span>Kifaru</span></div>
            <i />
          </div>
          <div className="signal-node">
            <span>Receiving institution</span>
            <strong>Bank B</strong>
            <small>Matched 08:42:18</small>
          </div>
        </div>

        <dl className="signal-details">
          <div><dt>Signal ID</dt><dd>KF-6F2A-91D7</dd></div>
          <div><dt>Match type</dt><dd>Device fingerprint</dd></div>
          <div><dt>Response time</dt><dd>2.1 seconds</dd></div>
        </dl>

        <footer>
          <div><Icon name="visibility_off" /><span><strong>Not shared</strong>Name, balance, full account number</span></div>
          <div><Icon name="encrypted" /><span><strong>Shared safely</strong>Protected identifier, event time, risk signal</span></div>
        </footer>
      </div>
      <p className="portal-signal-note">Kifaru helps institutions recognise the same fraud pattern
        before the next transfer is paid out.</p>
    </section>
  </main>;
}
