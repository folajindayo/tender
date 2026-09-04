"use client";

import { naira, relativeTime, stateLabel } from "@/lib/format";
import type { Transfer, User } from "@/lib/types";

export function Home({
  me,
  transfers,
  onOpen,
  onSend,
}: {
  me: User;
  transfers: Transfer[];
  onOpen: (t: Transfer) => void;
  onSend: () => void;
}) {
  const live = transfers.filter((t) =>
    ["pledged", "credited", "matched", "handover_pending"].includes(t.state),
  );

  return (
    <>
      <div className="card balance">
        <p className="card-title">Balance</p>
        <div className="amount">{naira(me.availableKobo, { decimals: false })}</div>
        <div className="sub">
          {me.escrowKobo > 0
            ? `${naira(me.escrowKobo, { decimals: false })} locked for a handover`
            : `${me.settledCount} settlements · trust ${me.trustScore}`}
        </div>

        <div className="chips">
          {me.creditLimitKobo > 0 ? (
            <span className="chip good">
              Instant credit up to {naira(me.creditLimitKobo, { decimals: false })}
            </span>
          ) : (
            <span className="chip">
              Instant credit unlocks after 3 settlements
            </span>
          )}
          {me.owedKobo > 0 && (
            <span className="chip warn">Owes {naira(me.owedKobo, { decimals: false })}</span>
          )}
          {me.sendingFrozen && <span className="chip bad">Sending frozen</span>}
        </div>
      </div>

      <button className="btn" onClick={onSend} disabled={me.sendingFrozen}>
        📸 Send cash you are holding
      </button>

      {live.length > 0 && (
        <div className="card">
          <p className="card-title">Happening now</p>
          <div className="rows">
            {live.map((t) => (
              <TransferRow key={t.id} t={t} me={me} onOpen={onOpen} />
            ))}
          </div>
        </div>
      )}

      <div className="card">
        <p className="card-title">Activity</p>
        <div className="rows">
          {transfers.length === 0 && (
            <div className="empty">Nothing yet. Photograph some cash to get started.</div>
          )}
          {transfers.map((t) => (
            <TransferRow key={t.id} t={t} me={me} onOpen={onOpen} />
          ))}
        </div>
      </div>
    </>
  );
}

function TransferRow({
  t,
  me,
  onOpen,
}: {
  t: Transfer;
  me: User;
  onOpen: (t: Transfer) => void;
}) {
  const incoming = t.recipientId === me.id;
  const settling = t.match?.counterpartyId === me.id;

  let who = incoming ? `From ${t.senderName}` : `To ${t.recipientName}`;
  if (settling) who = `Cash from ${t.senderName}`;

  const settled = t.state === "settled";
  const value = incoming || settling ? t.amountKobo - t.feeKobo : t.amountKobo;

  return (
    <button className="row" onClick={() => onOpen(t)}>
      <div>
        <div className="who">{who}</div>
        <div className="meta">
          #{t.ref} · {stateLabel(t.state)} · {relativeTime(t.createdAt)}
          {t.mode === "credit" && " · instant"}
        </div>
      </div>
      <div className={`val ${incoming && settled ? "in" : "out"}`}>
        {incoming ? "+" : settling ? "−" : ""}
        {naira(value, { decimals: false })}
      </div>
    </button>
  );
}
