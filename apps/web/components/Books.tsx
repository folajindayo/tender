"use client";

import { naira } from "@/lib/format";
import type { Audit } from "@/lib/types";

/**
 * The ledger, in the open.
 *
 * Tender's claim is that it settles value without ever holding cash and without
 * carrying risk on the escrow path. That claim is only worth anything if the
 * books are visible, so they are.
 */
export function Books({ audit }: { audit: Audit | null }) {
  if (!audit) {
    return <div className="card empty">Loading the books…</div>;
  }

  const balanced = audit.unbalancedTransactions === 0 && audit.globalSumKobo === 0;

  return (
    <>
      <div className={`banner ${balanced ? "info" : "danger"}`}>
        <div>
          <strong>
            {balanced ? "Every transaction balances" : "The books do not balance"}
          </strong>
          {balanced
            ? "Each entry sums to zero and no value has been created or destroyed. The database refuses to store anything else."
            : `${audit.unbalancedTransactions} transactions are out by ${naira(audit.globalSumKobo)}.`}
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="k">Capital at risk</div>
          <div className={`v ${audit.capitalAtRiskKobo === 0 ? "good" : ""}`}>
            {naira(audit.capitalAtRiskKobo, { decimals: false })}
          </div>
        </div>
        <div className="stat">
          <div className="k">Locked in escrow</div>
          <div className="v">{naira(audit.escrowedKobo, { decimals: false })}</div>
        </div>
        <div className="stat">
          <div className="k">Fees earned</div>
          <div className="v">{naira(audit.revenueKobo)}</div>
        </div>
        <div className="stat">
          <div className="k">Settlement float</div>
          <div className="v">{naira(audit.floatKobo, { decimals: false })}</div>
        </div>
      </div>

      <div className="card">
        <p className="card-title">Recent entries</p>
        <div className="ledger">
          {audit.lines.length === 0 && <div className="empty">No entries yet.</div>}
          {audit.lines.map((l, i) => (
            <div key={`${l.txId}-${i}`} className="ledger-row">
              <div>
                <div>{l.reason}</div>
                <div style={{ color: "var(--muted)" }}>
                  {l.owner} · {l.account}
                  {l.transferRef ? ` · #${l.transferRef}` : ""}
                </div>
              </div>
              <div className={`amt ${l.amountKobo > 0 ? "pos" : "neg"}`}>
                {l.amountKobo > 0 ? "+" : ""}
                {naira(l.amountKobo)}
              </div>
            </div>
          ))}
        </div>
      </div>

      <p className="muted">
        The escrow path never touches platform capital: a counterparty&apos;s own funds
        are locked and released. Capital is only at risk when instant credit has been
        extended and not yet settled.
      </p>
    </>
  );
}
