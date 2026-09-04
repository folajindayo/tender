"use client";

import type { User } from "@/lib/types";
import { naira } from "@/lib/format";

/**
 * Account chooser. Tender identifies people by phone number; on a shared device
 * this simply says which of the provisioned accounts is using it.
 */
export function PersonaPicker({
  users,
  onPick,
  onClose,
}: {
  users: User[];
  onPick: (id: string) => void;
  onClose?: () => void;
}) {
  return (
    <div className="app">
      <header className="header">
        <div className="wordmark">
          ten<span>der</span>
        </div>
        {onClose && (
          <button className="persona" onClick={onClose}>
            Close
          </button>
        )}
      </header>

      <main>
        <div className="stack">
          <h1 style={{ fontSize: 24, margin: 0, letterSpacing: "-0.02em" }}>
            Who is using this phone?
          </h1>
          <p className="muted" style={{ margin: 0 }}>
            Tender moves physical cash without a bank. Your notes go to someone nearby
            who wanted cash; the value goes where you sent it.
          </p>
        </div>

        <div className="card">
          <div className="rows">
            {users.map((u) => (
              <button key={u.id} className="row" onClick={() => onPick(u.id)}>
                <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                  <span style={{ fontSize: 26 }}>{u.avatarEmoji}</span>
                  <div>
                    <div className="who">{u.displayName}</div>
                    <div className="meta">
                      {u.city} · {u.settledCount} settlements
                    </div>
                  </div>
                </div>
                <div className="val out">{naira(u.availableKobo, { decimals: false })}</div>
              </button>
            ))}
            {users.length === 0 && (
              <div className="empty">
                No accounts yet. Run the bootstrap command against the database.
              </div>
            )}
          </div>
        </div>
      </main>
    </div>
  );
}
