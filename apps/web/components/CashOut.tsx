"use client";

import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import { naira } from "@/lib/format";
import type { CashoutRequest, User, Venue } from "@/lib/types";

const QUICK = [500000, 1000000, 2000000, 5000000];

/**
 * The supply side of the network: operators holding digital naira who want notes.
 *
 * Cash is only ever collected from **premises the operator actually runs** —
 * fixed, public, and answerable to somebody. Letting anyone nominate a meeting
 * place turns a cash request into a lure: the person choosing the spot is the
 * one who benefits from choosing a bad one, and the sender arrives carrying
 * exactly the amount that was advertised.
 */
export function CashOut({
  me,
  requests,
  venues,
  onPosted,
}: {
  me: User;
  requests: CashoutRequest[];
  venues: Venue[];
  onPosted: () => void;
}) {
  const [amount, setAmount] = useState("");
  const [venueId, setVenueId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const kobo = Math.round(Number(amount.replace(/[^0-9.]/g, "")) * 100) || 0;
  const affordable = kobo > 0 && kobo <= me.availableKobo;

  useEffect(() => {
    if (!venueId && venues.length) setVenueId(venues[0].id);
  }, [venues, venueId]);

  async function post() {
    setBusy(true);
    setError(null);
    try {
      const res = await api.requestCash({ userId: me.id, amountKobo: kobo, venueId });
      if ("code" in res && res.code) setError((res as { reason?: string }).reason ?? "That did not work.");
      else {
        setAmount("");
        onPosted();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not post that request.");
    } finally {
      setBusy(false);
    }
  }

  if (me.suspended) {
    return (
      <div className="banner danger">
        <div>
          <strong>This account is suspended</strong>
          A handover was reported and is being reviewed. You cannot take cash until
          that is resolved.
        </div>
      </div>
    );
  }

  // Someone with no registered premises cannot be a counterparty. That is the
  // point, not an oversight.
  if (venues.length === 0) {
    return (
      <>
        <div className="card stack">
          <p className="card-title">Taking cash</p>
          <div className="muted">
            Cash is only ever handed over at registered premises — an agent shop, a
            filling station, a bank branch — with an operator who is accountable for
            what happens there.
          </div>
          <div className="muted">
            You do not have a registered location, so you cannot take cash from
            senders yet. You can still send cash you are holding.
          </div>
        </div>
        <div className="banner info">
          <div>
            <strong>Why it works this way</strong>
            If anyone could name a meeting place, a robbery would look exactly like a
            cash request: pick a spot, advertise an amount, wait for someone to walk
            up carrying it.
          </div>
        </div>
      </>
    );
  }

  const selected = venues.find((v) => v.id === venueId);

  return (
    <>
      <div className="card stack">
        <p className="card-title">Ask for physical cash</p>
        <div className="muted">
          A sender nearby is holding notes they need to move. Take their cash at your
          counter and your balance goes to whoever they were paying.
        </div>

        <div className="field">
          <input
            className="amount-input"
            inputMode="decimal"
            placeholder="₦0"
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
          />
        </div>
        <div className="quick">
          {QUICK.map((k) => (
            <button key={k} onClick={() => setAmount(String(k / 100))}>
              {naira(k, { decimals: false })}
            </button>
          ))}
        </div>

        <div className="field">
          <label htmlFor="venue">Collected at</label>
          <select id="venue" value={venueId} onChange={(e) => setVenueId(e.target.value)}>
            {venues.map((v) => (
              <option key={v.id} value={v.id}>
                {v.name} — {v.address}
              </option>
            ))}
          </select>
        </div>

        {selected && !selected.openNow && (
          <div className="banner warn">
            <div>
              {selected.name} is outside its opening hours ({selected.opensAt}–
              {selected.closesAt}). Handovers are only matched while the premises are
              open.
            </div>
          </div>
        )}
        {kobo > me.availableKobo && kobo > 0 && (
          <div className="banner warn">
            <div>
              You hold {naira(me.availableKobo)}. You cannot ask for more cash than you
              can pay for.
            </div>
          </div>
        )}
        {error && (
          <div className="banner danger">
            <div>{error}</div>
          </div>
        )}

        <button className="btn" onClick={post} disabled={busy || !affordable || !venueId}>
          {busy ? <span className="spinner" /> : "Post the request"}
        </button>
      </div>

      <div className="card">
        <p className="card-title">Your open requests</p>
        <div className="rows">
          {requests.length === 0 && (
            <div className="empty">Nothing posted. Senders are matched to you automatically.</div>
          )}
          {requests.map((c) => (
            <div key={c.id} className="row" style={{ cursor: "default" }}>
              <div>
                <div className="who">{naira(c.amountKobo, { decimals: false })} in cash</div>
                <div className="meta">
                  {c.venueName} · {c.address}
                </div>
              </div>
              <div className={`chip ${c.state === "matched" ? "good" : ""}`}>{c.state}</div>
            </div>
          ))}
        </div>
      </div>

      <p className="muted">
        Nobody can see anyone else&apos;s open requests, including you. Only the
        matching engine reads the full book — publishing it would say who is about to
        be holding cash, where, and how much.
      </p>
    </>
  );
}
