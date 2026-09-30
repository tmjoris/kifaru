import { useState } from "react";
import type { Bank, InstitutionKind, Stage } from "../types";
import { Icon, Logo } from "./Shared";

const KIND_LABEL: Record<InstitutionKind, string> = {
  bank: "Commercial bank", sacco: "SACCO", psp: "Mobile money / PSP",
};

function PortalBankPicker({ banks, stage, onPick, onAddInstitution }: {
  banks: Bank[]; stage: Stage; onPick: (bankId: string) => void;
  onAddInstitution: (name: string, region: string, kind: InstitutionKind, stage: Stage) => void;
}) {
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [region, setRegion] = useState("Kenya");
  const [kind, setKind] = useState<InstitutionKind>("bank");
  const filtered = banks.filter((item) => item.name.toLowerCase().includes(query.trim().toLowerCase()));

  return <div className="portal-picker">
    <div className="search-field portal-search">
      <Icon name="search" />
      <input className="input" placeholder="Search institution" value={query} onChange={(event) => setQuery(event.target.value)}
        aria-label={`Search ${stage === "receiving" ? "receiving" : "reporting"} institutions`} />
    </div>
    <div className="portal-bank-grid" role="list">
      {filtered.map((item) =>
        <button key={item.id} className="portal-bank-btn" role="listitem" onClick={() => onPick(item.id)}>
          <span className="portal-bank-name">{item.name}</span>
          <span className="portal-bank-region">{item.region}{item.pending ? " \u00b7 Not yet connected" : ""}</span>
        </button>,
      )}
      {!filtered.length && <p className="muted portal-empty">No institution matches &ldquo;{query}&rdquo;.</p>}
    </div>
    {!adding
      ? <button className="mini-btn portal-add-toggle" onClick={() => setAdding(true)}>
        <Icon name="add" className="btn-icon" />Institution not listed
      </button>
      : <div className="portal-add-form">
        <input className="input" placeholder="Institution name" value={name} onChange={(event) => setName(event.target.value)} aria-label="New institution name" />
        <input className="input" placeholder="Region / county" value={region} onChange={(event) => setRegion(event.target.value)} aria-label="New institution region" />
        <select className="select" value={kind} onChange={(event) => setKind(event.target.value as InstitutionKind)} aria-label="Institution type">
          {(Object.keys(KIND_LABEL) as InstitutionKind[]).map((value) => <option key={value} value={value}>{KIND_LABEL[value]}</option>)}
        </select>
        <div className="portal-add-actions">
          <button className="btn" onClick={() => { setAdding(false); setName(""); }}>Cancel</button>
          <button className="btn primary" disabled={!name.trim()} onClick={() => {
            onAddInstitution(name.trim(), region.trim() || "Kenya", kind, stage);
            setName(""); setAdding(false);
          }}>Add &amp; sign in</button>
        </div>
      </div>}
  </div>;
}

export function PortalLogin({ banks, onEnterBank, onEnterKifaru, onAddInstitution }: {
  banks: Bank[];
  onEnterBank: (stage: Stage, bankId: string) => void;
  onEnterKifaru: () => void;
  onAddInstitution: (name: string, region: string, kind: InstitutionKind, stage: Stage) => void;
}) {
  return <div className="portal-screen">
    <div className="portal-card">
      <div className="portal-header">
        <Logo />
        <h1>Kifaru</h1>
        <p>Sign in as staff to see only the workspace for your institution and role.</p>
      </div>
      <div className="portal-role-grid">
        <section className="portal-role">
          <div className="portal-role-head">
            <span className="portal-role-icon"><Icon name="flag" /></span>
            <div><h2>Reporting bank</h2><p>Detects fraud, publishes a protected fingerprint</p></div>
          </div>
          <PortalBankPicker banks={banks} stage="reporting" onPick={(bankId) => onEnterBank("reporting", bankId)} onAddInstitution={onAddInstitution} />
        </section>
        <section className="portal-role">
          <div className="portal-role-head">
            <span className="portal-role-icon"><Icon name="hub" /></span>
            <div><h2>Kifaru exchange</h2><p>Shares fingerprints across every institution</p></div>
          </div>
          <button className="btn primary portal-enter" onClick={onEnterKifaru}>
            <Icon name="login" className="btn-icon" />Enter as exchange staff
          </button>
        </section>
        <section className="portal-role">
          <div className="portal-role-head">
            <span className="portal-role-icon"><Icon name="shield" /></span>
            <div><h2>Receiving bank</h2><p>Matches fingerprints, holds the transfer</p></div>
          </div>
          <PortalBankPicker banks={banks} stage="receiving" onPick={(bankId) => onEnterBank("receiving", bankId)} onAddInstitution={onAddInstitution} />
        </section>
      </div>
    </div>
  </div>;
}
