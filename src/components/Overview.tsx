import { useEffect, useId, useRef, useState } from "react";
import { loadTrends, resetTrendHistory, seedTrendHistory } from "../api";
import type { TrendCounts, TrendDay, TrendSummary } from "../types";
import { Card, Icon } from "./Shared";

type Row = { key: keyof TrendCounts; label: string; detail?: string };

const SERIES: { key: keyof Omit<TrendDay, "day">; label: string; className: string }[] = [
  { key: "reports", label: "Signals", className: "series-1" },
  { key: "alerts", label: "Alerts", className: "series-2" },
  { key: "blocked", label: "Blocked or held", className: "series-3" },
  { key: "released", label: "Released", className: "series-4" },
];

// The three alert tiers say why they fired; the other rows name plain counts.
function rows(staff: boolean): Row[] {
  return [
    { key: "reports", label: staff ? "Signals reported" : "Reports submitted" },
    { key: "alerts", label: staff ? "Alerts issued" : "Alerts received" },
    { key: "corroborated", label: "Corroborated", detail: "Two institutions reported the same account" },
    { key: "single_source_review", label: "Single-source review", detail: "One report, victim confirmed, trusted reporter" },
    { key: "single_source_notice", label: "Single-source notice", detail: "One report on a known-risk account" },
    { key: "held", label: "Held" },
    { key: "recovered", label: "Recovered" },
    { key: "released", label: "Released" },
    { key: "blocked", label: "Blocked", detail: "Held, recovered, or stopped by the bank" },
    { key: "allowed", label: "Allowed", detail: "Released, or paid out before a report" },
    { key: "incidents", label: "Incidents", detail: "Accounts with a corroborated alert" },
  ];
}

function Change({ now, before }: { now: number; before: number }) {
  const delta = now - before;
  const word = delta > 0 ? "up" : delta < 0 ? "down" : "no change";
  return <span className="trend-change" title={`${word}${delta ? ` ${Math.abs(delta)}` : ""}`}>
    {delta !== 0 && <Icon name={delta > 0 ? "arrow_upward" : "arrow_downward"} />}
    <span aria-hidden="true">{delta > 0 ? `+${delta}` : delta < 0 ? `${delta}` : "0"}</span>
    <span className="sr-only">{word}{delta ? ` ${Math.abs(delta)}` : ""}</span>
  </span>;
}

function Compared({ now, before }: { now: number; before: number }) {
  return <span className="trend-compared"><strong>{now}</strong>
    {now === before ? <span className="trend-from">no change</span>
      : <><Change now={now} before={before} /><span className="trend-from">from {before}</span></>}</span>;
}

const shortDay =(day: string) => new Date(`${day}T12:00:00`).toLocaleDateString("en-KE", { day: "numeric", month: "short" });

function TrendChart({ series, title }: { series: TrendDay[]; title: string }) {
  const [hover, setHover] = useState<number | null>(null);
  const [width, setWidth] = useState(560);
  const plot = useRef<HTMLDivElement>(null);
  const id = useId();
  // The chart is drawn at its real width so its text stays at true pixel sizes.
  useEffect(() => {
    const element = plot.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setWidth(Math.max(260, Math.round(entry.contentRect.width))));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const wide = width >= 440;
  const height = wide ? 230 : 200, left = 30, right = wide ? 112 : 8, top = 12, bottom = 28;
  const plotWidth = width - left - right, plotHeight = height - top - bottom;
  const peak = Math.max(4, ...series.flatMap((day) => SERIES.map((line) => day[line.key])));
  const step = Math.max(1, Math.ceil(peak / 4));
  const yMax = step * 4;
  const x = (index: number) => left + (series.length < 2 ? plotWidth / 2 : (index / (series.length - 1)) * plotWidth);
  const y = (value: number) => top + plotHeight - (value / yMax) * plotHeight;
  const totals = SERIES.map((line) => series.reduce((sum, day) => sum + day[line.key], 0));
  const summary = `${title}, ${series.length} days from ${series[0] ? shortDay(series[0].day) : ""} to ${series.length ? shortDay(series[series.length - 1].day) : ""}. `
    + SERIES.map((line, index) => `${line.label}: ${totals[index]} in total, ${series.length ? series[series.length - 1][line.key] : 0} today`).join(". ") + ".";
  // Direct labels sit at the end of each line; nudge them apart when lines end close together.
  const ends = SERIES.map((line) => ({ line, y: y(series.length ? series[series.length - 1][line.key] : 0) }))
    .sort((a, b) => a.y - b.y);
  for (let index = 1; index < ends.length; index++) ends[index].y = Math.max(ends[index].y, ends[index - 1].y + 16);
  const active = hover === null ? null : series[hover];

  return <figure className="trend-figure">
    <ul className="trend-legend" aria-hidden="true">{SERIES.map((line) =>
      <li key={line.key}><i className={line.className} />{line.label}</li>)}</ul>
    <div className="trend-plot" ref={plot}>
      <svg viewBox={`0 0 ${width} ${height}`} width={width} height={height} role="img" aria-labelledby={`${id}-summary`}
        onPointerLeave={() => setHover(null)}
        onPointerMove={(event) => {
          const box = event.currentTarget.getBoundingClientRect();
          const position = ((event.clientX - box.left) / box.width) * width;
          const index = Math.round(((position - left) / plotWidth) * (series.length - 1));
          setHover(Math.min(series.length - 1, Math.max(0, index)));
        }}>
        {[0, 1, 2, 3, 4].map((tick) => <g key={tick} className="trend-grid">
          <line x1={left} x2={left + plotWidth} y1={y(tick * step)} y2={y(tick * step)} />
          <text x={left - 6} y={y(tick * step) + 4} textAnchor="end">{tick * step}</text>
        </g>)}
        {series.map((day, index) => (index % (wide ? 7 : 10) === (series.length - 1) % (wide ? 7 : 10))
          && <text key={day.day} className="trend-axis" x={x(index)} y={height - 6}
            textAnchor={index === series.length - 1 && !wide ? "end" : "middle"}>{shortDay(day.day)}</text>)}
        {SERIES.map((line) => <polyline key={line.key} className={`trend-line ${line.className}`}
          points={series.map((day, index) => `${x(index)},${y(day[line.key])}`).join(" ")} />)}
        {wide && ends.map(({ line, y: labelY }) =>
          <text key={line.key} className="trend-end" x={left + plotWidth + 8} y={labelY + 4}>{line.label}</text>)}
        {hover !== null && <g className="trend-hover">
          <line x1={x(hover)} x2={x(hover)} y1={top} y2={top + plotHeight} />
          {SERIES.map((line) => <circle key={line.key} className={line.className} cx={x(hover)} cy={y(series[hover][line.key])} r={4} />)}
        </g>}
      </svg>
      {active && <div className="trend-tooltip" style={{ left: `${(x(hover!) / width) * 100}%` }} role="status">
        <strong>{shortDay(active.day)}</strong>
        {SERIES.map((line) => <span key={line.key}><i className={line.className} />{line.label} <b>{active[line.key]}</b></span>)}
      </div>}
    </div>
    <figcaption id={`${id}-summary`} className="sr-only">{summary}</figcaption>
    <details className="trend-table">
      <summary>Show the daily numbers as a table</summary>
      <div className="table-wrap"><table>
        <thead><tr><th scope="col">Day</th>{SERIES.map((line) => <th scope="col" key={line.key}>{line.label}</th>)}</tr></thead>
        <tbody>{[...series].reverse().map((day) => <tr key={day.day}><td>{shortDay(day.day)}</td>
          {SERIES.map((line) => <td key={line.key}>{day[line.key]}</td>)}</tr>)}</tbody>
      </table></div>
    </details>
  </figure>;
}

export function Overview({ institution, title, staff, version, notify }: {
  institution: string; title: string; staff: boolean; version: number; notify: (message: string) => void;
}) {
  const [trends, setTrends] = useState<TrendSummary | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    loadTrends(institution, 30, controller.signal)
      .then((value) => { setTrends(value); setError(""); })
      .catch((reason) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "Unable to load trends."); });
    return () => controller.abort();
  }, [institution, version, reload]);

  async function changeHistory(seed: boolean) {
    setBusy(true);
    try {
      const changed = seed ? await seedTrendHistory() : await resetTrendHistory();
      notify(seed ? `Seeded ${changed} synthetic reports across the last 60 days.` : `Removed ${changed} seeded reports.`);
      setReload((value) => value + 1);
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : "Unable to change the seeded history.");
    } finally {
      setBusy(false);
    }
  }

  const c = trends?.comparison;
  const tiles = c ? [
    { label: staff ? "Signals reported" : "Reports submitted", key: "reports" as const },
    { label: staff ? "Alerts issued" : "Alerts received", key: "alerts" as const },
    { label: "Blocked", key: "blocked" as const, extra: `${c.this_month.allowed} allowed this month` },
    { label: "Incidents", key: "incidents" as const, extra: "Distinct corroborated destinations" },
  ] : [];

  return <div className="overview">
    <Card title={title} subtitle={`Synthetic demo data in Nairobi time${trends ? ` · today is ${shortDay(trends.today)}` : ""}${trends?.seededReports ? ` · includes ${trends.seededReports} seeded history reports` : ""}.`}
      actions={staff ? <div className="control-row">
        <button className="btn" disabled={busy} onClick={() => void changeHistory(true)}><Icon name="history" className="btn-icon" />{busy ? "Working..." : "Seed 60 days of history"}</button>
        <button className="btn" disabled={busy || !trends?.seededReports} onClick={() => void changeHistory(false)}><Icon name="delete_sweep" className="btn-icon" />Remove seeded history</button>
      </div> : undefined}>
      {error && <p className="muted overview-message" role="alert">Trends unavailable: {error}</p>}
      {!c && !error && <p className="muted overview-message">Loading trends...</p>}
      {c && <section className="grid metrics overview-tiles">{tiles.map((tile) =>
        <div className="metric" key={tile.key}>
          <div className="metric-label"><span>{tile.label}</span><span>This month</span></div>
          <div className="metric-value">{c.this_month[tile.key]} <Change now={c.this_month[tile.key]} before={c.last_month[tile.key]} /></div>
          <div className="metric-detail">Today {c.today[tile.key]}, yesterday {c.yesterday[tile.key]}{tile.extra ? `. ${tile.extra}` : ""}</div>
        </div>)}
      </section>}
    </Card>
    {c && trends && <div className="overview-grid">
      <div className="overview-column">
        <Card title="Last 30 days" subtitle="Daily signals, alerts and receiver outcomes. Synthetic data.">
          <TrendChart series={trends.series} title={`${title}: last 30 days`} />
        </Card>
  {staff && <Card title="Institutions by activity" subtitle="Reports sent and alerts received in the last 30 days.">
        <div className="table-wrap"><table className="trend-rank">
          <thead><tr><th scope="col">Institution</th><th scope="col">Reports</th><th scope="col">Alerts</th></tr></thead>
          <tbody>{trends.ranking.length ? trends.ranking.slice(0, 6).map((row) => <tr key={row.institution}>
            <td>{row.name}</td><td>{row.reports}</td><td>{row.alerts}</td></tr>)
            : <tr><td colSpan={3} className="muted">No activity in the last 30 days.</td></tr>}</tbody>
        </table></div>
      </Card>}
      </div>
      <Card title="Today and this month" subtitle={`Today against yesterday, and this month so far against the same days last month (${trends.periods.last_month}).`}>
        <div className="table-wrap"><table className="trend-compare">
          <thead><tr><th scope="col">Figure</th><th scope="col">Today</th><th scope="col">This month</th></tr></thead>
          <tbody>{rows(staff).map((row) => <tr key={row.key}>
            <th scope="row"><strong>{row.label}</strong>{row.detail && <span className="trend-why">{row.detail}</span>}</th>
            <td><Compared now={c.today[row.key]} before={c.yesterday[row.key]} /></td>
            <td><Compared now={c.this_month[row.key]} before={c.last_month[row.key]} /></td>
          </tr>)}</tbody>
        </table></div>
      </Card>
    </div>}
  </div>;
}
