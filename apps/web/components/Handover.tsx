"use client";

import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import { distance, naira, payoutLabel, recipientLabel, stateLabel, timeLeft } from "@/lib/format";
import type { IncidentKind, Match as MatchType, Transfer, User } from "@/lib/types";

/**
 * The handover screen, shown from whichever side you are on.
 *
 * The sender holds a six-digit code; the counterparty quotes it back after
 * counting the notes. Both sides must confirm before any value moves, which is
 * what ties the digital settlement to a real meeting.
 */
export function Handover({
  transfer,
  me,
  onBack,
  onChanged,
}: {
  transfer: Transfer;
  me: User;
  onBack: () => void;
  onChanged: () => void;
}) {
  const isSender = transfer.senderId === me.id;
  const isCounterparty = transfer.match?.counterpartyId === me.id;

  if (transfer.state === "settled") return <Settled t={transfer} me={me} onBack={onBack} />;
  if (transfer.state === "disputed") return <Disputed t={transfer} onBack={onBack} />;
  if (["expired", "rejected", "voided", "defaulted"].includes(transfer.state)) {
    return <Closed t={transfer} onBack={onBack} />;
  }
  if (!transfer.match) return <Searching t={transfer} onBack={onBack} onChanged={onChanged} />;
  if (isCounterparty) return <CounterpartySide t={transfer} me={me} onBack={onBack} onChanged={onChanged} />;
  if (isSender) return <SenderSide t={transfer} me={me} onBack={onBack} onChanged={onChanged} />;
  return <Watching t={transfer} onBack={onBack} />;
}

/* ---------------------------------------------------------------- states */

function Searching({
  t,
  onBack,
  onChanged,
}: {
  t: Transfer;
  onBack: () => void;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  async function retry() {
    setBusy(true);
    try {
      const res = await api.rematch(t.id);
      setMessage(res.matched ? null : (res.reason ?? null));
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="stack">
      <div className="card center stack" style={{ padding: 32 }}>
        <div className="pulse" style={{ fontSize: 38 }}>
          📡
        </div>
        <div style={{ fontWeight: 700, fontSize: 17 }}>Finding someone who wants cash</div>
        <div className="muted">
          Looking for a person near you who holds {naira(t.amountKobo)} digitally and
          wants notes instead. Nothing moves until one is found.
        </div>
      </div>
      {message && (
        <div className="banner warn">
          <div>{message}</div>
        </div>
      )}
      <button className="btn secondary" onClick={retry} disabled={busy}>
        {busy ? <span className="spinner" /> : "Look again"}
      </button>
      <button className="btn ghost" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

function SenderSide({
  t,
  me,
  onBack,
  onChanged,
}: {
  t: Transfer;
  me: User;
  onBack: () => void;
  onChanged: () => void;
}) {
  const m = t.match!;
  const [busy, setBusy] = useState(false);
  const alreadyConfirmed =
    m.state === "sender_confirmed" || m.state === "completed";

  async function confirm() {
    setBusy(true);
    try {
      await api.confirm(t.id, me.id);
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="stack">
      <div className="card">
        <p className="card-title">Show this code to {m.counterpartyName}</p>
        <div className="code">{m.handoverCode}</div>
        <div className="center muted">
          <Countdown iso={m.expiresAt} />
        </div>
      </div>

      <MeetCard m={m} />

      <div className="banner info">
        <div>
          <strong>Hand over {naira(t.amountKobo)} in cash</strong>
          {m.counterpartyName} will count the notes and quote your code. Once you both
          confirm, {recipientLabel(t)} is paid.
        </div>
      </div>

      <button className="btn" onClick={confirm} disabled={busy || alreadyConfirmed}>
        {busy ? (
          <span className="spinner" />
        ) : alreadyConfirmed ? (
          `Waiting for ${m.counterpartyName}`
        ) : (
          "I have handed over the cash"
        )}
      </button>
      <ReportProblem transferId={t.id} me={me} role="sender" onChanged={onChanged} />
      <button className="btn ghost" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

function CounterpartySide({
  t,
  me,
  onBack,
  onChanged,
}: {
  t: Transfer;
  me: User;
  onBack: () => void;
  onChanged: () => void;
}) {
  const m = t.match!;
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const confirmed = m.state === "counterparty_confirmed" || m.state === "completed";

  async function confirm() {
    setBusy(true);
    setError(null);
    try {
      const res = await api.confirm(t.id, me.id, code);
      if ("code" in res && res.code) setError(res.reason ?? "That did not work.");
      else onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : "That did not work.");
    } finally {
      setBusy(false);
    }
  }

  async function refuse() {
    setBusy(true);
    try {
      await api.reject(t.id, me.id, "notes refused at handover");
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  if (confirmed) {
    return (
      <div className="stack">
        <div className="card center stack" style={{ padding: 32 }}>
          <div className="pulse" style={{ fontSize: 38 }}>
            ⏳
          </div>
          <div style={{ fontWeight: 700 }}>Waiting for {t.senderName} to confirm</div>
          <div className="muted">
            Your {naira(m.amountKobo)} stays locked until they do.
          </div>
        </div>
        <ReportProblem transferId={t.id} me={me} role="counterparty" onChanged={onChanged} />
        <button className="btn ghost" onClick={onBack}>
          Back
        </button>
      </div>
    );
  }

  return (
    <div className="stack">
      <div className="banner info">
        <div>
          <strong>Collect {naira(m.amountKobo)} in cash from {t.senderName}</strong>
          Count the notes and check they are genuine before you confirm. Your{" "}
          {naira(m.amountKobo)} is locked and will only move once you do.
        </div>
      </div>

      <MeetCard m={m} />

      <div className="card stack">
        <div className="field">
          <label htmlFor="code">Code on {t.senderName}&apos;s phone</label>
          <input
            id="code"
            className="code-input"
            inputMode="numeric"
            maxLength={6}
            placeholder="––––––"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
          />
        </div>
        <div className="center muted">
          <Countdown iso={m.expiresAt} />
        </div>
      </div>

      {error && (
        <div className="banner danger">
          <div>{error}</div>
        </div>
      )}

      <button className="btn" onClick={confirm} disabled={busy || code.length !== 6}>
        {busy ? <span className="spinner" /> : "I have the cash in hand"}
      </button>
      <button className="btn danger" onClick={refuse} disabled={busy}>
        Refuse these notes
      </button>
      <ReportProblem transferId={t.id} me={me} role="counterparty" onChanged={onChanged} />
      <button className="btn ghost" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

function Watching({ t, onBack }: { t: Transfer; onBack: () => void }) {
  return (
    <div className="stack">
      <div className="card center stack" style={{ padding: 32 }}>
        <div className="pulse" style={{ fontSize: 38 }}>
          💸
        </div>
        <div style={{ fontWeight: 700, fontSize: 17 }}>
          {naira(t.amountKobo - t.feeKobo)} on the way
        </div>
        <div className="muted">
          {t.senderName} is handing cash to {t.match?.counterpartyName} now. It lands in
          your balance the moment they both confirm.
        </div>
      </div>
      <button className="btn ghost" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

function Settled({ t, me, onBack }: { t: Transfer; me: User; onBack: () => void }) {
  const received = t.recipientId === me.id;
  return (
    <div className="stack">
      <div className="card center stack" style={{ padding: 34 }}>
        <div style={{ fontSize: 44 }}>✅</div>
        <div style={{ fontSize: 26, fontWeight: 700 }}>
          {naira(t.amountKobo - t.feeKobo)}
        </div>
        <div className="muted">
          {received
            ? `Received from ${t.senderName}.`
            : `Handed over. Transfer #${t.ref} is settled.`}
        </div>
      </div>
      {/* Settled means the cash changed hands and the ledger balanced. Whether
          the bank has paid it out yet is a separate fact, and shown as one. */}
      {t.payout && (
        <div className={`banner ${payoutLabel(t.payout).tone === "bad" ? "danger" : payoutLabel(t.payout).tone === "warn" ? "warn" : "info"}`}>
          <div>
            <strong>
              {t.payout.accountName}
              {t.payout.bankName ? ` · ${t.payout.bankName}` : ""}
            </strong>
            {payoutLabel(t.payout).text}
          </div>
        </div>
      )}
      <button className="btn" onClick={onBack}>
        Done
      </button>
    </div>
  );
}

function Closed({ t, onBack }: { t: Transfer; onBack: () => void }) {
  const expired = t.state === "expired";
  return (
    <div className="stack">
      <div className={`banner ${expired ? "warn" : "danger"}`}>
        <div>
          <strong>Transfer #{t.ref} — {stateLabel(t.state)}</strong>
          {expired
            ? "Nobody completed the handover in time. The counterparty's funds were released and nothing was charged. Nobody lost money."
            : "This transfer was closed without settling."}
        </div>
      </div>
      <button className="btn" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

/* ---------------------------------------------------------------- pieces */

function MeetCard({ m }: { m: MatchType }) {
  return (
    <div className="meet">
      <span style={{ fontSize: 26 }}>🏪</span>
      <div style={{ flex: 1 }}>
        <div className="who">{m.venueName}</div>
        <div className="where">
          {m.venueAddress} · {distance(m.distanceM)}
        </div>
        <div className="where">Ask for {m.counterpartyName}</div>
      </div>
    </div>
  );
}

/**
 * Reporting a handover that went wrong.
 *
 * Escrow cannot tell theft from an innocent no-show — in both cases the other
 * side simply never confirms, and the sweeper releases the money either way.
 * A report breaks that tie: the funds are held instead of released, and stay
 * held until a person looks at it.
 */
function ReportProblem({
  transferId,
  me,
  role,
  onChanged,
}: {
  transferId: string;
  me: User;
  role: "sender" | "counterparty";
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const options: { kind: IncidentKind; label: string; hint: string }[] =
    role === "sender"
      ? [
          {
            kind: "cash_taken",
            label: "I handed over the cash and they will not confirm",
            hint: "Holds their funds instead of releasing them",
          },
          { kind: "no_show", label: "Nobody was there", hint: "" },
          { kind: "threatened", label: "I felt unsafe or was threatened", hint: "Suspends the account" },
        ]
      : [
          { kind: "wrong_amount", label: "The amount handed over was wrong", hint: "" },
          { kind: "no_show", label: "The sender never arrived", hint: "" },
          { kind: "threatened", label: "I felt unsafe or was threatened", hint: "Suspends the account" },
        ];

  async function report(kind: IncidentKind) {
    setBusy(true);
    setError(null);
    try {
      const res = await api.reportIncident(transferId, me.id, kind, "");
      if (res.code) setError(res.reason ?? "Could not file that report.");
      else onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not file that report.");
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <button className="btn ghost" onClick={() => setOpen(true)}>
        Something went wrong
      </button>
    );
  }

  return (
    <div className="card stack">
      <p className="card-title">What happened?</p>
      {options.map((o) => (
        <button
          key={o.kind}
          className="btn secondary"
          style={{ flexDirection: "column", alignItems: "flex-start", gap: 3 }}
          disabled={busy}
          onClick={() => report(o.kind)}
        >
          <span style={{ fontSize: 14 }}>{o.label}</span>
          {o.hint && (
            <span style={{ fontSize: 12, color: "var(--muted)", fontWeight: 500 }}>{o.hint}</span>
          )}
        </button>
      ))}
      {error && (
        <div className="banner danger">
          <div>{error}</div>
        </div>
      )}
      <button className="btn ghost" onClick={() => setOpen(false)}>
        Never mind
      </button>
    </div>
  );
}

function Disputed({ t, onBack }: { t: Transfer; onBack: () => void }) {
  return (
    <div className="stack">
      <div className="banner warn">
        <div>
          <strong>Transfer #{t.ref} is under review</strong>
          A problem was reported with this handover. The counterparty&apos;s funds are
          being held — not released — until it is resolved. Nothing expires while a
          report is open.
        </div>
      </div>
      <div className="muted">
        Holding the money is the point: if a report simply let the timer run out, the
        person who took the cash would get their balance back anyway.
      </div>
      <button className="btn" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

function Countdown({ iso }: { iso: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, []);
  return <>{timeLeft(iso)}</>;
}
