import type { GuidedDemoStatus, Notification } from "../types";
import { Card, Icon } from "./Shared";

const stepCopy = [
  {
    title: "First institution publishes",
    detail: "A protected beneficiary signal enters the exchange and waits for an independent match.",
  },
  {
    title: "Second institution corroborates",
    detail: "A separate report matches the same destination within the receiving institution.",
  },
  {
    title: "Receiving institution is alerted",
    detail: "Kifaru routes the corroborated signal without deciding what happens to the payment.",
  },
  {
    title: "Institution records its response",
    detail: "The receiver acknowledges the alert and records a synthetic review hold.",
  },
];

function nextAction(step: number) {
  if (step === 0) return "Publish first signal";
  if (step === 1) return "Publish matching signal";
  if (step >= 3) return "Scenario complete";
  return "Record receiver action";
}

export function GuidedCampaign({ campaign, busy, onAdvance, onReset }: {
  campaign: GuidedDemoStatus | null;
  busy: boolean;
  onAdvance: () => Promise<void>;
  onReset: () => Promise<void>;
}) {
  const step = campaign?.step ?? 0;
  const completed = step >= 3;
  return <Card title="Guided cross-bank scenario"
    subtitle="Run one deterministic story from first report to receiving-institution response."
    className="guided-campaign"
    actions={<span className={`guided-state state-${campaign?.status ?? "ready"}`}>
      <i />{campaign?.status === "failed" ? "Needs reset" : completed ? "Complete" : step ? "In progress" : "Ready"}
    </span>}>
    <div className="guided-grid">
      <div className="guided-track" aria-label="Guided demonstration progress">
        {stepCopy.map((item, index) => {
          const done = completed || index < step;
          const current = !done && index === step;
          return <div key={item.title} className={`guided-stage ${done ? "done" : ""} ${current ? "current" : ""}`}>
            <span className="guided-index">{done ? <Icon name="check" /> : index + 1}</span>
            <span><strong>{item.title}</strong><small>{item.detail}</small></span>
          </div>;
        })}
      </div>
      <div className="guided-scene">
        <div className="guided-route" aria-label="Scenario institutions">
          <div className="route-node">
            <span>First reporter</span>
            <strong>{campaign?.reportingInstitutionAName || "NCBA"}</strong>
            <small>{step > 0 ? "Protected signal published" : "Ready to publish"}</small>
          </div>
          <div className="route-thread" />
          <div className="route-node signal">
            <span>Protected beneficiary</span>
            <strong>{step > 1 ? "Qualified match" : "Awaiting match"}</strong>
            <small>{step > 1 ? "Two institutions, one scoped token" : "No receiver alert yet"}</small>
          </div>
          <div className="route-thread" />
          <div className="route-node">
            <span>Second reporter</span>
            <strong>{campaign?.reportingInstitutionBName || "KCB"}</strong>
            <small>{step > 1 ? "Independent report matched" : "Ready to corroborate"}</small>
          </div>
          <div className="route-thread" />
          <div className="route-node outcome">
            <span>Receiving institution</span>
            <strong>{campaign?.receivingInstitutionName || "Equity Bank"}</strong>
            <small>{step > 2 ? "Review hold recorded" : step > 1 ? "Review alert delivered" : "Decision retained"}</small>
          </div>
        </div>
        {campaign?.error && <p className="guided-error" role="alert">{campaign.error}</p>}
        <div className="guided-actions">
          <button className="btn primary" disabled={busy || completed || campaign?.status === "failed"}
            onClick={() => void onAdvance()}>
            <Icon name={step === 0 ? "play_arrow" : step === 1 ? "join_inner" : "fact_check"} />
            {busy ? "Processing..." : nextAction(step)}
          </button>
          <button className="btn" disabled={busy || (!campaign?.runId && step === 0)}
            onClick={() => void onReset()}>
            <Icon name="restart_alt" />Reset scenario
          </button>
        </div>
      </div>
    </div>
  </Card>;
}

function notificationTitle(eventType: string) {
  if (eventType.includes("disputed")) return "Receiving institution disputed an alert";
  if (eventType.includes("released")) return "Receiving institution released the activity";
  if (eventType.includes("recovered")) return "Receiving institution recorded recovery";
  if (eventType.includes("held")) return "Receiving institution recorded a review hold";
  if (eventType.includes("retracted")) return "A linked alert was retracted";
  return "Shared signal update";
}

export function SignalNotifications({ notifications }: { notifications: Notification[] }) {
  if (!notifications.length) return null;
  return <section className="signal-notifications" aria-label="Recent signal updates">
    <span><Icon name="notifications_active" /> Recent signal updates</span>
    <div className="signal-notification-list">{notifications.slice(0, 3).map((notification) =>
      <article key={notification.id}>
        <strong>{notificationTitle(notification.eventType)}</strong>
        <time>{notification.createdAt || "just now"}</time>
        <p>{notification.recordId}</p>
      </article>,
    )}</div>
  </section>;
}
