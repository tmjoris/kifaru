import type { DemoStreamStatus } from "../types";
import { Card, Icon } from "./Shared";

function readable(value: string) {
  return value
    .toLowerCase()
    .replaceAll("_", " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function DemoStream({ stream, busy, onToggle, onEmit, onReset }: {
  stream: DemoStreamStatus | null;
  busy: boolean;
  onToggle: (enabled: boolean) => Promise<void>;
  onEmit: () => Promise<void>;
  onReset: () => Promise<void>;
}) {
  const events = stream?.events ?? [];
  return <Card title="Synthetic SOC event stream"
    subtitle="Microsoft Sentinel-shaped reports exercise the same protected-signal pipeline as uploads and API submissions."
    className="stream-console"
    actions={<span className={`stream-state ${stream?.enabled ? "live" : ""}`}>
      <i />{stream?.enabled ? "Producing" : "Paused"}
    </span>}>
    <div className="stream-toolbar">
      <dl className="stream-metrics">
        <div><dt>Topic</dt><dd className="mono">{stream?.topic || "sentinel.security-alert"}</dd></div>
        <div><dt>Next offset</dt><dd>{stream?.nextOffset ?? 1}</dd></div>
        <div><dt>Cadence</dt><dd>{stream?.cadenceSeconds ?? 30}s</dd></div>
        <div><dt>Retained</dt><dd>{stream?.retainedEvents ?? 0} / {stream?.maxEvents ?? 500}</dd></div>
      </dl>
      <div className="stream-actions">
        <button className={`btn ${stream?.enabled ? "" : "primary"}`} disabled={busy}
          onClick={() => void onToggle(!stream?.enabled)}>
          <Icon name={stream?.enabled ? "pause" : "play_arrow"} className="btn-icon" />
          {stream?.enabled ? "Pause stream" : "Start stream"}
        </button>
        <button className="btn" disabled={busy} onClick={() => void onEmit()}>
          <Icon name="skip_next" className="btn-icon" />Emit one
        </button>
        <button className="btn" disabled={busy || !events.length} onClick={() => {
          if (window.confirm("Reset the synthetic stream and remove its generated reports?")) {
            void onReset();
          }
        }}>
          <Icon name="restart_alt" className="btn-icon" />Reset
        </button>
      </div>
    </div>
    <div className="stream-outcomes" aria-label="Current demonstration outcomes">
      <div><span>Signals</span><strong>{stream?.metrics.signals ?? 0}</strong></div>
      <div><span>Corroborated</span><strong>{stream?.metrics.corroborated ?? 0}</strong></div>
      <div><span>Awaiting match</span><strong>{stream?.metrics.awaiting ?? 0}</strong></div>
      <div><span>Alerts delivered</span><strong>{stream?.metrics.alerts ?? 0}</strong></div>
      <div><span>Receiver actions</span><strong>{stream?.metrics.actioned ?? 0}</strong></div>
      <div><span>Disputed or retracted</span>
        <strong>{(stream?.metrics.disputed ?? 0) + (stream?.metrics.retracted ?? 0)}</strong></div>
      <div><span>Actioned synthetic value</span>
        <strong>KES {(stream?.metrics.actionedValue ?? 0).toLocaleString()}</strong></div>
      <div><span>Policy p95</span><strong>{Math.round(stream?.metrics.p95LatencyMs ?? 0)} ms</strong></div>
    </div>
    <div className="stream-log" aria-live="polite">
      <div className="stream-log-head">
        <span>Recent broker events</span>
        <small>{stream?.datasetBasis || "Public SOC schemas and synthetic banking transactions"}</small>
      </div>
      {events.length ? events.slice(0, 8).map((event) =>
        <div className="stream-event" key={event.eventOffset}>
          <span className="stream-offset mono">#{event.eventOffset}</span>
          <span><strong>{event.alertName || readable(event.eventType)}</strong>
            <small>{readable(event.eventType)} · {event.alertSeverity || "Unrated"}</small></span>
          <span className={`stream-result ${event.status}`}>{event.outcome ? readable(event.outcome) : readable(event.status)}</span>
        </div>,
      ) : <p className="stream-empty">No synthetic events have been produced. Start the stream or emit one event.</p>}
    </div>
  </Card>;
}
