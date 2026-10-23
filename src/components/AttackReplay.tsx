import { useEffect, useRef } from "react";
import type { AttackScenarioStatus, AttackSignal } from "../types";
import { explainReasons } from "../explain";
import { validationLabel } from "../domain";
import { Icon } from "./Shared";

type Mode = AttackScenarioStatus["mode"];

/** The banks' own systems are scripted; only the Kifaru lane comes from the backend. */
const LAST_STEP = 5;
const BLOCKED_ATTEMPT = 120000;
const MULE = "••4417";
const CLOCK = ["", "10:02", "10:09", "10:11", "10:24", "10:31"];

const kes = (amount: number) => `KES ${amount.toLocaleString("en-KE")}`;

interface Names { a: string; b: string; r: string }

function stepTitle(step: number, { a, b }: Names) {
  return ["Before the attack", `Account takeover at ${a}`, `${a} blocks a second takeover`,
    "The receiving bank", `The attacker moves to ${b}`, "Cash-out"][step];
}

function narration(step: number, mode: Mode, names: Names, scenario: AttackScenarioStatus) {
  const { a, b, r } = names;
  const first = scenario.signals[0];
  const tier = first ? validationLabel(first.status).toLowerCase() : "single-source review";
  switch (step) {
    case 0:
      return `An attacker has phished online banking logins for customers at ${a} and ${b}, and controls a mule account at ${r} opened three days ago. `
        + (mode === "with"
          ? `This time both sending banks publish protected signals to Kifaru.`
          : "Each bank works alone. There is no shared channel between them.");
    case 1:
      return `The attacker signs in to Grace Achieng's ${a} account from a new phone, adds ${r} ${MULE} as a beneficiary and sends ${kes(scenario.firstAmount)}. ${a}'s fraud engine scores it 0.58, below its 0.80 blocking threshold, so the transfer completes.`;
    case 2:
      return `Minutes later the same phone signs in to Peter Mwangi's account and tries to send ${kes(BLOCKED_ATTEMPT)} to the same beneficiary. ${a} blocks it and calls Grace, who confirms she never made the first transfer. The attacker sees the decline and knows ${a} is onto them. `
        + (mode === "with"
          ? `${a} publishes a protected signal, and Kifaru sends ${r} a ${tier}: the victim confirmed the transfer, and receivers upheld ${scenario.standingUpheld} of ${a}'s ${scenario.standingUpheld + scenario.standingAdverse} recent alerts.`
          : `${a} opens an internal case and emails ${r}'s fraud mailbox. Nothing reaches ${r}'s systems.`);
    case 3:
      return mode === "with"
        ? `${r}'s fraud desk reviews the alert and places a post-no-debit hold on ${MULE}. Money can still arrive, but nothing can leave. The ${kes(scenario.firstAmount)} is frozen.`
        : `At ${r}, ${MULE} looks like any other account. The ${kes(scenario.firstAmount)} is free to withdraw, and ${a}'s email sits unread in a shared mailbox.`;
    case 4:
      return `The attacker switches to a new phone and signs in to Faith Njeri's ${b} account. ${b} has never seen ${MULE}, and its engine scores the ${kes(scenario.secondAmount)} transfer 0.62, below its 0.75 threshold. The transfer completes. `
        + (mode === "with"
          ? `${b} still publishes its flag. Kifaru matches it to ${a}'s signal despite the new phone, escalates the alert to corroborated, and the money lands on a frozen account.`
          : `${b} has no way to know that ${a} blocked the same beneficiary fifteen minutes earlier.`);
    default:
      return mode === "with"
        ? `The attacker's withdrawals are declined. ${r} returns ${kes(scenario.secondAmount)} to ${b} and ${kes(scenario.firstAmount)} to ${a}. The mule account is burned and no customer loses money.`
        : `The attacker moves ${kes(scenario.firstAmount + scenario.secondAmount)} out of ${MULE} through mobile wallets and agent withdrawals. When ${r} reads ${a}'s email the next morning, the account is empty. Two customers at two banks have lost their money.`;
  }
}

type LogLine = { clock: string; text: string; tone?: "ok" | "bad" | "warn" };

function attackerLog(step: number, mode: Mode, { a, b, r }: Names, scenario: AttackScenarioStatus): LogLine[] {
  const total = scenario.firstAmount + scenario.secondAmount;
  const lines: LogLine[][] = [
    [{ clock: "09:40", text: `phished logins: 2 × ${a}, 1 × ${b}. Mule account ready at ${r}.` }],
    [
      { clock: CLOCK[1], text: `login ${a} · grace.achieng (new phone, OTP via SIM swap)` },
      { clock: CLOCK[1], text: `add beneficiary ${r} ${MULE}` },
      { clock: CLOCK[1], text: `transfer ${kes(scenario.firstAmount)} … completed`, tone: "ok" },
    ],
    [
      { clock: CLOCK[2], text: `login ${a} · peter.mwangi (same phone)` },
      { clock: CLOCK[2], text: `transfer ${kes(BLOCKED_ATTEMPT)} … declined`, tone: "bad" },
      { clock: CLOCK[2], text: `${a} has flagged this phone. Switching to ${b}.`, tone: "warn" },
    ],
    [{ clock: CLOCK[3], text: `check mule balance: ${kes(scenario.firstAmount)}` }],
    [
      { clock: CLOCK[4], text: `new phone · login ${b} · faith.njeri` },
      { clock: CLOCK[4], text: `add beneficiary ${r} ${MULE}` },
      { clock: CLOCK[4], text: `transfer ${kes(scenario.secondAmount)} … completed`, tone: "ok" },
    ],
    mode === "with"
      ? [
        { clock: CLOCK[5], text: `withdraw ${kes(total)} … declined: account restricted`, tone: "bad" },
        { clock: CLOCK[5], text: "mule account frozen. Campaign burned.", tone: "warn" },
      ]
      : [
        { clock: CLOCK[5], text: `withdraw ${kes(total)} to wallets and agents … completed`, tone: "ok" },
        { clock: CLOCK[5], text: `campaign complete. ${kes(total)} out.`, tone: "ok" },
      ],
  ];
  return lines.slice(0, step + 1).flat();
}

interface Transfer {
  clock: string;
  customer: string;
  device: string;
  amount: number;
  score: number;
  threshold: number;
  blocked: boolean;
  reasons: string[];
  tag?: string;
}

function SenderPlatform({ bank, theme, transfers, footer, receiver }: {
  bank: string;
  theme: string;
  transfers: Transfer[];
  footer: { icon: string; text: string; tone: "neutral" | "kifaru" | "alone" } | null;
  receiver: string;
}) {
  return <section className={`bank-platform ${theme}`} aria-label={`${bank} online banking`}>
    <header className="platform-bar">
      <span className="platform-logo"><Icon name="account_balance" /></span>
      <span><strong>{bank}</strong><small>Online banking · simulated</small></span>
    </header>
    <div className="platform-body">
      {transfers.length === 0 && <p className="platform-idle"><Icon name="verified_user" />No unusual activity</p>}
      {transfers.map((transfer) => <article key={transfer.customer} className={`platform-transfer ${transfer.blocked ? "blocked" : "completed"}`}>
        <div className="transfer-head">
          <span><strong>{transfer.customer}</strong><small>{transfer.device} · {transfer.clock}</small></span>
          <span className={`transfer-result ${transfer.blocked ? "blocked" : "completed"}`}>
            <Icon name={transfer.blocked ? "block" : "check_circle"} />{transfer.blocked ? "Blocked" : "Completed"}
          </span>
        </div>
        <p className="transfer-line">To {receiver} {MULE} <strong>{kes(transfer.amount)}</strong></p>
        <div className="engine" aria-label={`Fraud engine score ${transfer.score} against a blocking threshold of ${transfer.threshold}`}>
          <span className="engine-label">Fraud engine</span>
          <span className="engine-track">
            <i className="engine-fill" style={{ width: `${transfer.score * 100}%` }} />
            <i className="engine-threshold" style={{ left: `${transfer.threshold * 100}%` }} />
          </span>
          <span className="engine-value">{transfer.score.toFixed(2)}<small> / block at {transfer.threshold.toFixed(2)}</small></span>
        </div>
        <ul className="transfer-reasons">{transfer.reasons.map((reason) => <li key={reason}>{reason}</li>)}
          {transfer.tag && <li className="transfer-tag"><Icon name="report" />{transfer.tag}</li>}</ul>
      </article>)}
    </div>
    {footer && <footer className={`platform-footer ${footer.tone}`}><Icon name={footer.icon} />{footer.text}</footer>}
  </section>;
}

function tierLabel(signal: AttackSignal) {
  if (signal.alertTier === "corroborated") return "Corroborated alert";
  if (signal.alertTier === "single_source_review") return "Single-source review";
  if (signal.alertTier === "single_source_notice") return "Single-source notice";
  return validationLabel(signal.status);
}

function receiverDecision(signal: AttackSignal) {
  if (signal.alertOutcome) return `${signal.alertState} · ${signal.alertOutcome}`;
  return signal.alertState || "no alert";
}

function KifaruLane({ mode, step, names, signals }: {
  mode: Mode; step: number; names: Names; signals: AttackSignal[];
}) {
  if (mode === "without") {
    return <section className="kifaru-lane alone" aria-label="Shared channel">
      <span className="lane-mark"><Icon name="link_off" /></span>
      <div>
        <strong>No shared channel</strong>
        <p>{step >= 2
          ? `${names.a}'s block stays inside ${names.a}. An email to ${names.r}'s fraud mailbox is the only link, and nobody has read it.`
          : "Each bank sees only its own customers and its own transfers."}</p>
      </div>
    </section>;
  }
  return <section className="kifaru-lane connected" aria-label="Kifaru exchange">
    <span className="lane-mark"><Icon name="hub" /></span>
    <div className="lane-content">
      <strong>Kifaru exchange <small>live backend records</small></strong>
      {signals.length === 0 && <p>{step >= 1
        ? `Listening. ${names.a} has no reason to report until its fraud engine or a customer tells it something is wrong.`
        : "Both sending banks are connected. Nothing has been published yet."}</p>}
      <div className="lane-signals">{signals.map((signal) => <article key={signal.reportId} className="lane-signal">
        <div className="lane-route">
          <span>{signal.reportingInstitutionName}</span><Icon name="arrow_forward" />
          <span className="lane-token">protected beneficiary token</span><Icon name="arrow_forward" />
          <span>{names.r}</span>
        </div>
        <div className="lane-verdict">
          <span className={`lane-tier tier-${signal.alertTier || "none"}`}>{tierLabel(signal)}</span>
          <span>score {signal.score.toFixed(2)}</span>
          {signal.corroborationCount > 0 && <span>{signal.corroborationCount} matching institution</span>}
        </div>
        <ul className="lane-reasons">{explainReasons(signal.reasonCodes)
          .filter((reason) => reason.code.includes(":") && !reason.code.startsWith("BANK:"))
          .map((reason) => <li key={reason.code}>{reason.title}</li>)}</ul>
        <p className="lane-ids">{signal.reportId} · {signal.alertId || "no alert"} · {names.r}: {receiverDecision(signal)}</p>
      </article>)}</div>
    </div>
  </section>;
}

function ReceiverPlatform({ mode, step, names, scenario }: {
  mode: Mode; step: number; names: Names; scenario: AttackScenarioStatus;
}) {
  const { a, b, r } = names;
  const total = scenario.firstAmount + scenario.secondAmount;
  const held = mode === "with" && step >= 3;
  const balance = step >= 5 ? 0 : step >= 4 ? total : step >= 1 ? scenario.firstAmount : 0;
  const accountState = mode === "with"
    ? step >= 5 ? "Frozen · funds returned" : held ? "Post-no-debit hold" : "Active"
    : step >= 5 ? "Emptied" : "Active";
  const creditState = (frozenFrom: number, returnedTo: string) => {
    if (mode === "with") {
      if (step >= 5) return { state: `Returned to ${returnedTo}`, tone: "safe" };
      if (step >= frozenFrom) return { state: frozenFrom === 4 ? "Frozen on arrival" : "Frozen", tone: "safe" };
      return { state: "Available", tone: "risk" };
    }
    return step >= 5 ? { state: "Withdrawn", tone: "lost" } : { state: "Available", tone: "risk" };
  };
  const ledger = [
    step >= 1 && { id: "c1", clock: CLOCK[1], text: `+${kes(scenario.firstAmount)} from ${a} · Grace Achieng`, ...creditState(3, a) },
    step >= 4 && { id: "c2", clock: CLOCK[4], text: `+${kes(scenario.secondAmount)} from ${b} · Faith Njeri`, ...creditState(4, b) },
    step >= 5 && (mode === "with"
      ? { id: "d1", clock: CLOCK[5], text: `−${kes(total)} withdrawal attempt`, tone: "safe", state: "Declined" }
      : { id: "d1", clock: CLOCK[5], text: `−${kes(total)} to wallets and agents`, tone: "lost", state: "Paid out" }),
  ].filter(Boolean) as { id: string; clock: string; text: string; tone: string; state: string }[];

  return <section className="bank-platform platform-receiver" aria-label={`${r} receiving bank`}>
    <header className="platform-bar">
      <span className="platform-logo"><Icon name="account_balance" /></span>
      <span><strong>{r}</strong><small>Core banking and fraud desk · simulated</small></span>
    </header>
    <div className="receiver-grid">
      <div className="mule-account">
        <span className="eyebrow">Beneficiary account {MULE}</span>
        <p className="mule-holder">J. Kiprono · opened 3 days ago</p>
        <p className={`mule-state ${held ? "held" : accountState === "Emptied" ? "emptied" : ""}`}>
          <Icon name={held ? "lock" : accountState === "Emptied" ? "money_off" : "lock_open"} />{accountState}
        </p>
        <p className="mule-balance">{kes(balance)}<small>{held && step < 5 ? " frozen" : " balance"}</small></p>
        <ul className="mule-ledger">
          {ledger.length === 0 && <li className="muted">No activity yet</li>}
          {ledger.map((entry) => <li key={entry.id} className={`tone-${entry.tone}`}>
            <span>{entry.clock}</span><span>{entry.text}</span><strong>{entry.state}</strong>
          </li>)}
        </ul>
      </div>
      <div className="fraud-inbox">
        <span className="eyebrow">Fraud desk</span>
        {mode === "without" && (step >= 2
          ? <article className="inbox-item email">
            <Icon name="mail" />
            <span><strong>Email from {a} fraud team</strong>
              <small>{step >= 5 ? "Read next morning. The account is already empty." : "Unread · shared mailbox, no response time agreed"}</small></span>
          </article>
          : <p className="muted">Nothing to review.</p>)}
        {mode === "with" && (scenario.signals.length === 0
          ? <p className="muted">Nothing to review.</p>
          : scenario.signals.map((signal) => <article key={signal.reportId} className={`inbox-item alert tier-${signal.alertTier}`}>
            <Icon name="notifications_active" />
            <span>
              <strong>{tierLabel(signal)} from Kifaru</strong>
              <small>Reported by {signal.reportingInstitutionName} · {signal.alertId}</small>
              <small className="inbox-decision">Decision: {receiverDecision(signal)}</small>
            </span>
          </article>))}
      </div>
    </div>
  </section>;
}

function Outcome({ mode, step, names, scenario }: {
  mode: Mode; step: number; names: Names; scenario: AttackScenarioStatus;
}) {
  const total = scenario.firstAmount + scenario.secondAmount;
  const atRisk = mode === "with" && step >= 3 ? 0 : step >= 5 ? 0 : step >= 4 ? total : step >= 1 ? scenario.firstAmount : 0;
  const protectedAmount = mode === "with" ? (step >= 4 ? total : step >= 3 ? scenario.firstAmount : 0) : 0;
  const lost = mode === "without" && step >= 5 ? total : 0;
  const knew = mode === "with"
    ? step >= 2 ? `At ${names.a}'s block (${CLOCK[2]})` : "Not yet"
    : step >= 5 ? "Next morning, by email" : "Not yet";
  return <section className="replay-outcome" aria-label="Running outcome">
    <div><span>Customer money lost</span><strong className={lost ? "lost" : ""}>{kes(lost)}</strong></div>
    <div><span>Withdrawable by the attacker</span><strong className={atRisk ? "risk" : ""}>{kes(atRisk)}</strong></div>
    <div><span>Frozen or returned</span><strong className={protectedAmount ? "safe" : ""}>{kes(protectedAmount)}</strong></div>
    <div><span>{names.r} learned of it</span><strong>{knew}</strong></div>
  </section>;
}

function Comparison({ mode, names, scenario }: { mode: Mode; names: Names; scenario: AttackScenarioStatus }) {
  const total = kes(scenario.firstAmount + scenario.secondAmount);
  return <section className="replay-compare" aria-label="Comparison">
    <div className={`compare-column alone ${mode === "without" ? "current" : ""}`}>
      <span className="eyebrow">Without Kifaru</span>
      <ul>
        <li><strong>{total}</strong> lost by two customers at two banks</li>
        <li>{names.r} learned of it the next morning, by email</li>
        <li>{names.b} let the transfer through with no warning</li>
      </ul>
    </div>
    <div className={`compare-column connected ${mode === "with" ? "current" : ""}`}>
      <span className="eyebrow">With Kifaru</span>
      <ul>
        <li><strong>KES 0</strong> lost. {total} frozen and returned</li>
        <li>{names.r} was alerted when {names.a} blocked the second takeover</li>
        <li>{names.b}'s weaker signal was matched and the alert escalated</li>
      </ul>
    </div>
  </section>;
}

export function AttackReplay({ scenario, busy, onAdvance, onReset }: {
  scenario: AttackScenarioStatus | null;
  busy: boolean;
  onAdvance: () => Promise<void>;
  onReset: (mode: Mode) => Promise<void>;
}) {
  const step = scenario?.step ?? 0;
  const mode = scenario?.mode ?? "without";
  const complete = step >= LAST_STEP;
  const canAdvance = !!scenario && !busy && !complete && scenario.status !== "failed";
  const root = useRef<HTMLElement>(null);

  // Bring the receiving side and the final comparison into view as they change.
  useEffect(() => {
    const target = step === 3 ? ".replay-receiving" : step === LAST_STEP ? ".replay-compare" : "";
    if (target) root.current?.querySelector(target)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }, [step]);

  // Presentation clickers send arrow, page and space keys.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)
        || (event.key === " " && target.tagName === "BUTTON"))) return;
      if (["ArrowRight", "PageDown", " "].includes(event.key) && canAdvance) {
        event.preventDefault();
        void onAdvance();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [canAdvance, onAdvance]);

  if (!scenario) return <section className="attack-replay"><p className="muted">Loading the attack replay...</p></section>;
  const names = { a: scenario.firstSenderName || "NCBA", b: scenario.secondSenderName || "KCB", r: scenario.receiverName || "Equity Bank" };

  const firstBank: Transfer[] = [];
  if (step >= 1) firstBank.push({
    clock: CLOCK[1], customer: "Grace Achieng", device: "New Android phone", amount: scenario.firstAmount,
    score: 0.58, threshold: 0.8, blocked: false, reasons: ["New device", "New beneficiary"],
    tag: step >= 2 ? "Customer confirmed fraud" : undefined,
  });
  if (step >= 2) firstBank.push({
    clock: CLOCK[2], customer: "Peter Mwangi", device: "Same phone as Grace's session", amount: BLOCKED_ATTEMPT,
    score: 0.93, threshold: 0.8, blocked: true,
    reasons: ["Phone used on another customer 7 min ago", "Beneficiary just paid by another customer"],
  });
  const secondBank: Transfer[] = step >= 4 ? [{
    clock: CLOCK[4], customer: "Faith Njeri", device: "New iPhone", amount: scenario.secondAmount,
    score: 0.62, threshold: 0.75, blocked: false, reasons: ["New device", "New beneficiary"],
  }] : [];
  const firstSignal = scenario.signals[0];
  const secondSignal = scenario.signals[1];

  return <section ref={root} className={`attack-replay mode-${mode}`}>
    <header className="replay-header">
      <div>
        <p className="eyebrow">Attack replay · synthetic customers and accounts</p>
        <h2>One mule account, two sending banks</h2>
      </div>
      <div className="replay-mode" role="group" aria-label="Replay mode">
        {(["without", "with"] as Mode[]).map((option) => <button key={option}
          className={`mode-option ${mode === option ? "active" : ""}`} aria-pressed={mode === option}
          disabled={busy} onClick={() => void onReset(option)}>
          <Icon name={option === "with" ? "hub" : "link_off"} />{option === "with" ? "With Kifaru" : "Without Kifaru"}
        </button>)}
      </div>
    </header>

    <div className="replay-stage">
      <div className="replay-steps" aria-label="Replay progress">
        {Array.from({ length: LAST_STEP }, (_, index) => <span key={index}
          className={`replay-dot ${index < step ? "done" : ""} ${index === step - 1 ? "current" : ""}`}>{index + 1}</span>)}
      </div>
      <div className="replay-narration" aria-live="polite">
        <span className="eyebrow">{step === 0 ? "Setup" : `Step ${step} of ${LAST_STEP} · ${CLOCK[step]}`}</span>
        <h3>{stepTitle(step, names)}</h3>
        <p>{narration(step, mode, names, scenario)}</p>
        {scenario.error && <p className="guided-error" role="alert">{scenario.error}</p>}
      </div>
      <div className="replay-controls">
        {complete && mode === "without"
          ? <button className="btn primary" disabled={busy} onClick={() => void onReset("with")}>
            <Icon name="hub" />Replay with Kifaru
          </button>
          : <button className="btn primary" disabled={!canAdvance} onClick={() => void onAdvance()}>
            <Icon name={complete ? "check" : "play_arrow"} />
            {busy ? "Working..." : complete ? "Replay complete" : step === 0 ? "Start the attack" : "Next step"}
          </button>}
        <button className="btn" disabled={busy || (step === 0 && scenario.status === "ready")}
          onClick={() => void onReset(mode)}>
          <Icon name="restart_alt" />Reset
        </button>
        <small className="muted">→ or Space advances</small>
      </div>
    </div>

    <div className="replay-senders">
      <section className="attacker-console" aria-label="Attacker console">
        <header><Icon name="terminal" />Attacker</header>
        <ol>{attackerLog(step, mode, names, scenario).map((line, index) =>
          <li key={index} className={line.tone ? `tone-${line.tone}` : ""}><time>{line.clock}</time>{line.text}</li>)}</ol>
      </section>
      <SenderPlatform bank={names.a} theme="platform-first" receiver={names.r} transfers={firstBank}
        footer={step < 2 ? null : mode === "with"
          ? { icon: "hub", tone: "kifaru", text: `Protected signal published to Kifaru${firstSignal ? ` · ${firstSignal.reportId}` : ""}` }
          : { icon: "mail", tone: "alone", text: `Case opened. Email sent to ${names.r}'s fraud mailbox.` }} />
      <SenderPlatform bank={names.b} theme="platform-second" receiver={names.r} transfers={secondBank}
        footer={step < 4 ? null : mode === "with"
          ? { icon: "hub", tone: "kifaru", text: `Flag published to Kifaru${secondSignal ? ` · ${secondSignal.reportId}` : ""}` }
          : { icon: "visibility_off", tone: "alone", text: "Below the blocking threshold. Nothing else to go on." }} />
    </div>

    <div className="replay-receiving">
      <KifaruLane mode={mode} step={step} names={names} signals={scenario.signals} />
      <ReceiverPlatform mode={mode} step={step} names={names} scenario={scenario} />
    </div>
    <Outcome mode={mode} step={step} names={names} scenario={scenario} />
    {complete && <Comparison mode={mode} names={names} scenario={scenario} />}
  </section>;
}
