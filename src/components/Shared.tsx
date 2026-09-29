import { Fragment, type ReactNode } from "react";
import { statusClass, validationLabel } from "../domain";
import type { ChainNode, PipelineStep } from "../domain";

export function Logo() {
  return <div className="brand-mark">
    <svg viewBox="0 0 64 64" role="img" aria-label="Kifaru rhinoceros shield logo">
      <path d="M32 5 54 14v16c0 14.4-8.8 24.5-22 29-13.2-4.5-22-14.6-22-29V14l22-9Z" fill="currentColor" opacity=".18" />
      <path d="M19 35c1.2-6.5 6.1-11.1 14.2-11.1h8.1c4.8 0 8.5 3.4 9.1 8.1l.5 3.8h-7.3l-3.8-5.1h-8.2l-5.3 7.6h-8.1l.8-3.3Z" fill="currentColor" />
      <path d="M42.2 23.7 54 16.8l-5.3 14.6c-1-3.5-3.2-6.2-6.5-7.7Z" fill="currentColor" />
      <path d="M24.8 39.4h7.4l-2.1 6.9h-7.3l2-6.9Zm15.2 0h7.4l-2 6.9h-7.3l1.9-6.9Z" fill="currentColor" />
      <path d="M18.7 35.2 11 32.1l8.9-3.1-.8 3.1-.4 3.1Z" fill="currentColor" />
      <circle cx="39.6" cy="28.4" r="1.4" fill="var(--cp-accent-fg)" />
    </svg>
  </div>;
}

/** A small four-colour accent, echoing the reference product's brand marks. Used sparingly. */
export function BrandDots() {
  return <span className="brand-dots" aria-hidden="true"><i /><i /><i /><i /></span>;
}

/** Renders a Google Material Symbols glyph by ligature name, e.g. <Icon name="search" />. */
export function Icon({ name, className = "" }: { name: string; className?: string }) {
  return <span className={`material-symbols-outlined ${className}`} aria-hidden="true">{name}</span>;
}

export function Card({ title, subtitle, children, actions, className = "" }: {
  title: string; subtitle?: string; children: ReactNode; actions?: ReactNode; className?: string;
}) {
  return <article className={`card ${className}`}>
    <div className="card-header">
      <div><h3 className="card-title">{title}</h3>{subtitle && <p className="card-subtitle">{subtitle}</p>}</div>
      {actions}
    </div>
    {children}
  </article>;
}

export function Status({ status }: { status: string }) {
  return <span className={`pill ${statusClass(status)}`}>{validationLabel(status)}</span>;
}

export function KnowledgeItems({ items }: { items: { title: string; text: string }[] }) {
  return <div className="kb-list">{items.map((item) =>
    <div className="kb-item" key={item.title}><strong>{item.title}</strong><p>{item.text}</p></div>,
  )}</div>;
}

/** The KIFARU pipeline: Detect -> Fingerprint -> Share -> Match -> Act. */
export function Pipeline({ steps, compact }: { steps: PipelineStep[]; compact?: boolean }) {
  return <ol className={`pipeline ${compact ? "pipeline-compact" : ""}`} aria-label="Kifaru validation pipeline">
    {steps.map((step, index) => <li key={step.id} className={`pipeline-step state-${step.state}`}>
      <span className="pipeline-node" aria-hidden="true">{index + 1}</span>
      <span className="pipeline-copy"><strong>{step.label}</strong>{!compact && <span>{step.detail}</span>}</span>
    </li>)}
  </ol>;
}

/** How money and intelligence moved: reporting bank -> shared fingerprint -> receiving bank -> outcome. */
export function CampaignChain({ nodes }: { nodes: ChainNode[] }) {
  return <div className="chain" role="list" aria-label="Cross-institution fraud chain">
    {nodes.map((node, index) => <Fragment key={node.label + index}>
      <div className={`chain-node kind-${node.kind}`} role="listitem">
        <strong>{node.label}</strong><span>{node.detail}</span>
      </div>
      {index < nodes.length - 1 && <span className="chain-arrow" aria-hidden="true">&rarr;</span>}
    </Fragment>)}
  </div>;
}
