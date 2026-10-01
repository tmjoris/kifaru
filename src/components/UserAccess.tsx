import { type FormEvent, useMemo, useState } from "react";
import type { UserAccessRequest, UserAccessRequestStatus } from "../types";
import { Card, Icon } from "./Shared";

function statusLabel(status: UserAccessRequestStatus) {
  if (status === "approved") return "Admitted";
  if (status === "rejected") return "Rejected";
  return "Awaiting review";
}

function statusClass(status: UserAccessRequestStatus) {
  if (status === "approved") return "clear";
  if (status === "rejected") return "fraud";
  return "review";
}

function formatDate(value: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function InstitutionUserAccess({
  institutionName,
  emailDomain,
  requests,
  loading,
  error,
  onRequest,
  notify,
}: {
  institutionName: string;
  emailDomain: string;
  requests: UserAccessRequest[];
  loading: boolean;
  error: string;
  onRequest: (alias: string) => Promise<void>;
  notify: (message: string) => void;
}) {
  const [alias, setAlias] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const normalizedAlias = alias.trim().toLowerCase();
  const preview = `${normalizedAlias || "newuser"}@${emailDomain || "institution-domain"}`;
  const pendingCount = requests.filter((request) => request.status === "pending").length;

  async function submitRequest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    try {
      await onRequest(normalizedAlias);
      setAlias("");
      notify(`User addition request sent for ${preview}.`);
    } catch (requestError) {
      notify(requestError instanceof Error ? requestError.message : "User addition request failed.");
    } finally {
      setSubmitting(false);
    }
  }

  return <Card className="wide-card access-gateway" title="Request a teammate account"
    subtitle={`Ask Kifaru staff to admit another ${institutionName} user. You supply only the alias.`}
    actions={<span className={`pill ${pendingCount ? "review" : "clear"}`}>
      {pendingCount ? `${pendingCount} awaiting review` : "Queue clear"}
    </span>}>
    <div className="access-gateway-grid">
      <form className="alias-request-form" onSubmit={(event) => void submitRequest(event)}>
        <label htmlFor="userAlias">Account alias</label>
        <div className="alias-builder">
          <input id="userAlias" className="input" value={alias}
            onChange={(event) => setAlias(event.target.value.toLowerCase())}
            placeholder="janekamau" autoComplete="off" minLength={3} maxLength={32}
            pattern="[a-z][a-z0-9]{2,31}" required />
          <span className="domain-lock"><Icon name="lock" />@{emailDomain || "loading..."}</span>
        </div>
        <p className="field-help">Use 3-32 letters or numbers, starting with a letter. Kifaru locks the domain to this institution.</p>
        <div className="admission-preview">
          <span>Requested sign-in</span>
          <strong>{preview}</strong>
        </div>
        <button className="btn primary" type="submit"
          disabled={submitting || loading || !emailDomain || !normalizedAlias}>
          <Icon name="person_add" className="btn-icon" />
          {submitting ? "Sending request..." : "Request account"}
        </button>
      </form>

      <section className="request-ledger" aria-labelledby="institution-request-history">
        <div className="request-ledger-heading">
          <div>
            <h4 id="institution-request-history">Admission ledger</h4>
            <p>Kifaru staff must approve an account before it can sign in.</p>
          </div>
          <span>{requests.length} total</span>
        </div>
        {error && <p className="request-state error" role="alert">{error}</p>}
        {!error && loading && <p className="request-state">Loading account requests...</p>}
        {!error && !loading && !requests.length &&
          <p className="request-state">No account requests yet. The first request will appear here.</p>}
        {!!requests.length && <div className="request-list" role="list">{requests.map((request) =>
          <article className="request-row" role="listitem" key={request.requestId}>
            <div>
              <strong>{request.email}</strong>
              <p>Requested {formatDate(request.requestedAt)} by {request.requestedByEmail}</p>
              {request.reviewedAt && <p>Reviewed {formatDate(request.reviewedAt)} by {request.reviewedByEmail}</p>}
              {request.approvedPasswordTip && <p>{request.approvedPasswordTip}</p>}
            </div>
            <span className={`pill ${statusClass(request.status)}`}>{statusLabel(request.status)}</span>
          </article>,
        )}</div>}
      </section>
    </div>
  </Card>;
}

export function StaffUserAdmissions({
  requests,
  loading,
  error,
  onDecision,
  notify,
}: {
  requests: UserAccessRequest[];
  loading: boolean;
  error: string;
  onDecision: (requestId: string, decision: "approved" | "rejected") => Promise<void>;
  notify: (message: string) => void;
}) {
  const [busyRequestId, setBusyRequestId] = useState("");
  const pending = useMemo(
    () => requests.filter((request) => request.status === "pending"),
    [requests],
  );
  const decided = useMemo(
    () => requests.filter((request) => request.status !== "pending").slice(0, 8),
    [requests],
  );

  async function decide(request: UserAccessRequest, decision: "approved" | "rejected") {
    setBusyRequestId(request.requestId);
    try {
      await onDecision(request.requestId, decision);
      notify(decision === "approved"
        ? `${request.email} admitted to ${request.institutionName}.`
        : `${request.email} request rejected.`);
    } catch (decisionError) {
      notify(decisionError instanceof Error ? decisionError.message : "User admission decision failed.");
    } finally {
      setBusyRequestId("");
    }
  }

  return <Card className="admission-desk" title="Institution user admissions"
    subtitle="Each institution controls the alias request. Kifaru controls the tenant binding and final admission."
    actions={<span className={`pill ${pending.length ? "review" : "clear"}`}>
      {pending.length ? `${pending.length} pending` : "No pending requests"}
    </span>}>
    {error && <p className="request-state error" role="alert">{error}</p>}
    {!error && loading && <p className="request-state">Loading institution requests...</p>}
    {!error && !loading && !pending.length &&
      <p className="request-state">The admission desk is clear. New institution requests will appear here.</p>}
    {!!pending.length && <div className="admission-queue" role="list">{pending.map((request) =>
      <article className="admission-ticket" role="listitem" key={request.requestId}>
        <div className="admission-ticket-mark" aria-hidden="true"><Icon name="badge" /></div>
        <div className="admission-ticket-copy">
          <strong>{request.email}</strong>
          <p>{request.institutionName} · requested by {request.requestedByEmail}</p>
          <span>{formatDate(request.requestedAt)}</span>
        </div>
        <div className="admission-actions">
          <button className="btn primary" disabled={!!busyRequestId}
            onClick={() => void decide(request, "approved")}>
            <Icon name="how_to_reg" className="btn-icon" />
            {busyRequestId === request.requestId ? "Saving..." : "Admit user"}
          </button>
          <button className="btn" disabled={!!busyRequestId}
            onClick={() => void decide(request, "rejected")}>Reject</button>
        </div>
      </article>,
    )}</div>}
    {!!decided.length && <div className="decision-strip">
      <h4>Recent decisions</h4>
      <div className="decision-list">{decided.map((request) =>
        <div key={request.requestId}>
          <span className={`pill ${statusClass(request.status)}`}>{statusLabel(request.status)}</span>
          <strong>{request.email}</strong>
          <span>{request.institutionName}</span>
        </div>,
      )}</div>
    </div>}
  </Card>;
}
