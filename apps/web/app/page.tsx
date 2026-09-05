"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Auth } from "@/components/Auth";
import { Books } from "@/components/Books";
import { CashOut } from "@/components/CashOut";
import { Handover } from "@/components/Handover";
import { Home } from "@/components/Home";
import { SendFlow } from "@/components/SendFlow";
import { api, subscribe } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { Audit, CashoutRequest, Transfer, Venue } from "@/lib/types";

type Tab = "home" | "send" | "cashout" | "books";

export default function Page() {
  const { me, setMe, ready, signIn, signUp, signOut } = useSession();
  const userId = me?.id ?? null;

  const [transfers, setTransfers] = useState<Transfer[]>([]);
  const [requests, setRequests] = useState<CashoutRequest[]>([]);
  const [venues, setVenues] = useState<Venue[]>([]);
  const [audit, setAudit] = useState<Audit | null>(null);
  const [tab, setTab] = useState<Tab>("home");
  const [openTransferId, setOpenTransferId] = useState<string | null>(null);
  const [offline, setOffline] = useState(false);
  const [flash, setFlash] = useState<string | null>(null);

  const openTransfer = useMemo(
    () => transfers.find((t) => t.id === openTransferId) ?? null,
    [transfers, openTransferId],
  );

  const refresh = useCallback(async () => {
    if (!userId) return;
    try {
      // Balances move without this device doing anything -- an escrow release
      // or a payout return lands here -- so the account is re-read alongside
      // its activity rather than trusted from sign-in.
      // Cash requests and venues are per-account: the demand book is never
      // published, so there is nothing to fetch until we know who is asking.
      const [user, ts, cs, vs] = await Promise.all([
        api.user(userId),
        api.transfers(userId),
        api.cashouts(userId),
        api.venues(userId),
      ]);
      setMe(user);
      setTransfers(ts ?? []);
      setRequests(cs ?? []);
      setVenues(vs ?? []);
      setOffline(false);
    } catch {
      setOffline(true);
    }
  }, [userId, setMe]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (tab === "books") void api.audit().then(setAudit).catch(() => {});
  }, [tab, transfers]);

  // Push, not poll: a recipient watching for money should see it land.
  useEffect(() => {
    if (!userId) return;
    return subscribe(userId, (event) => {
      if (event.type === "credit.unlocked" || event.type === "credit.revoked") {
        const data = event.data as { message?: string } | undefined;
        if (data?.message) setFlash(data.message);
      }
      void refresh();
    });
  }, [userId, refresh]);

  // A settled transfer should take you to its receipt rather than leaving you
  // staring at a stale handover screen.
  const prevStates = useRef(new Map<string, string>());
  useEffect(() => {
    for (const t of transfers) {
      const was = prevStates.current.get(t.id);
      if (was && was !== t.state && t.id === openTransferId) setTab("home");
      prevStates.current.set(t.id, t.state);
    }
  }, [transfers, openTransferId]);

  if (!ready) return <div className="app" />;

  if (!me) {
    return (
      <Auth
        onSignIn={async (email, password) => {
          await signIn(email, password);
          setTab("home");
        }}
        onSignUp={async (input) => {
          await signUp(input);
          setTab("home");
        }}
      />
    );
  }

  return (
    <div className="app">
      <header className="header">
        <div className="wordmark">
          ten<span>der</span>
        </div>
        <button className="persona" onClick={() => void signOut()}>
          <span className="avatar">{me.avatarEmoji}</span>
          {me.displayName.split(" ")[0]} · Sign out
        </button>
      </header>

      <main>
        {offline && (
          <div className="banner warn">
            <div>Offline — showing the last known state.</div>
          </div>
        )}
        {flash && (
          <button
            className="banner info"
            style={{ textAlign: "left", cursor: "pointer", width: "100%" }}
            onClick={() => setFlash(null)}
          >
            <div>{flash}</div>
          </button>
        )}

        {openTransfer ? (
          <Handover
            transfer={openTransfer}
            me={me}
            onBack={() => setOpenTransferId(null)}
            onChanged={() => void refresh()}
          />
        ) : tab === "home" ? (
          <Home
            me={me}
            transfers={transfers}
            onOpen={(t) => setOpenTransferId(t.id)}
            onSend={() => setTab("send")}
          />
        ) : tab === "send" ? (
          <SendFlow
            me={me}
            onDone={(id) => {
              setOpenTransferId(id);
              setTab("home");
              void refresh();
            }}
          />
        ) : tab === "cashout" ? (
          <CashOut
            me={me}
            requests={requests}
            venues={venues}
            onPosted={() => void refresh()}
          />
        ) : (
          <Books audit={audit} />
        )}
      </main>

      <nav className="tabbar">
        {(
          [
            ["home", "🏠", "Home"],
            ["send", "📸", "Send cash"],
            ["cashout", "🤝", "Get cash"],
            ["books", "📊", "Books"],
          ] as const
        ).map(([key, glyph, label]) => (
          <button
            key={key}
            className={tab === key && !openTransfer ? "active" : ""}
            onClick={() => {
              setOpenTransferId(null);
              setTab(key);
            }}
          >
            <span className="glyph">{glyph}</span>
            {label}
          </button>
        ))}
      </nav>
    </div>
  );
}
