"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, api } from "@/lib/api";
import { captureFrame, openRearCamera, shrinkFile } from "@/lib/camera";
import { breakdown, naira, tally } from "@/lib/format";
import type { Bank, BankAccount, PledgeResult, User } from "@/lib/types";

type Step = "amount" | "capture" | "working" | "result";

const QUICK = [100000, 500000, 1000000, 2000000]; // ₦1k, ₦5k, ₦10k, ₦20k

/** Nigerian NUBAN account numbers are always ten digits. */
const ACCOUNT_DIGITS = 10;

export function SendFlow({
  me,
  onDone,
}: {
  me: User;
  onDone: (transferId: string) => void;
}) {
  const [step, setStep] = useState<Step>("amount");
  const [amount, setAmount] = useState("");
  const [account, setAccount] = useState<BankAccount | null>(null);
  const [note, setNote] = useState("");
  const [result, setResult] = useState<PledgeResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const kobo = Math.round(Number(amount.replace(/[^0-9.]/g, "")) * 100) || 0;

  async function submit(photo: Blob) {
    if (!account) return;
    setStep("working");
    setError(null);
    try {
      const res = await api.pledge({
        senderId: me.id,
        account,
        amountKobo: kobo,
        note,
        photo,
      });
      setResult(res);
      setStep("result");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Something went wrong.");
      setStep("capture");
    }
  }

  if (step === "amount") {
    const canContinue = kobo > 0 && account !== null && !me.sendingFrozen;
    return (
      <div className="stack">
        {me.sendingFrozen && (
          <div className="banner danger">
            <div>
              <strong>Sending is frozen</strong>
              An earlier transfer was never settled. Clear the outstanding amount to
              send again.
            </div>
          </div>
        )}

        <div className="card stack">
          <div className="field">
            <label htmlFor="amount">How much cash are you holding?</label>
            <input
              id="amount"
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
        </div>

        <Destination userId={me.id} account={account} onResolved={setAccount} />

        <div className="card stack">
          <div className="field">
            <label htmlFor="note">What is it for? (optional)</label>
            <input
              id="note"
              placeholder="school fees"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              maxLength={80}
            />
          </div>
        </div>

        {kobo > 0 && (
          <div className="muted center">
            {naira(kobo)} in cash · a 0.5% fee comes out of the amount, so{" "}
            {account ? account.accountName : "they"} receives{" "}
            <strong style={{ color: "var(--text)" }}>
              {naira(kobo - Math.floor((kobo * 50) / 10000))}
            </strong>
          </div>
        )}

        <button className="btn" disabled={!canContinue} onClick={() => setStep("capture")}>
          Photograph the cash
        </button>
      </div>
    );
  }

  if (step === "capture") {
    return (
      <Capture
        amount={kobo}
        error={error}
        onBack={() => setStep("amount")}
        onPhoto={submit}
      />
    );
  }

  if (step === "working") {
    return (
      <div className="card center stack" style={{ padding: 40 }}>
        <div className="pulse" style={{ fontSize: 40 }}>
          🔎
        </div>
        <div style={{ fontWeight: 700, fontSize: 17 }}>Counting the notes</div>
        <div className="muted">
          Checking the denominations, reading serial numbers, and making sure this is
          paper rather than a screen.
        </div>
      </div>
    );
  }

  return (
    <PledgeOutcome
      result={result!}
      onRetry={() => {
        setResult(null);
        setStep("capture");
      }}
      onDone={onDone}
    />
  );
}

/* ------------------------------------------------------------ destination */

/**
 * Where the money is going: a bank, an account number, and the name the bank
 * holds for it.
 *
 * The name is never typed. It comes back from name enquiry and the sender has
 * to see it before they can go on, because reading the name back is the only
 * check standing between a mistyped digit and cash handed to a stranger for
 * nothing. A destination is only accepted once the bank has confirmed it, so
 * `onResolved(null)` fires the moment either field is edited.
 */
function Destination({
  userId,
  account,
  onResolved,
}: {
  userId: string;
  account: BankAccount | null;
  onResolved: (account: BankAccount | null) => void;
}) {
  const [banks, setBanks] = useState<Bank[]>([]);
  const [banksError, setBanksError] = useState<string | null>(null);
  const [sortCode, setSortCode] = useState("");
  const [number, setNumber] = useState("");
  const [looking, setLooking] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api
      .banks()
      .then((list) => {
        if (!live) return;
        // Alphabetical: this list is long, and a sender is looking for one name.
        setBanks([...list].sort((a, b) => a.name.localeCompare(b.name)));
      })
      .catch((e) => {
        if (live) setBanksError(e instanceof Error ? e.message : "Could not load banks.");
      });
    return () => {
      live = false;
    };
  }, []);

  // Every lookup is tagged so a slow earlier response cannot overwrite a newer
  // one -- otherwise correcting a typo can leave the previous account's name on
  // screen, which is precisely the thing this screen exists to prevent.
  const attempt = useRef(0);

  const resolve = useCallback(
    async (code: string, acct: string) => {
      const mine = ++attempt.current;
      setLooking(true);
      setError(null);
      try {
        const found = await api.resolveAccount({
          userId,
          accountNumber: acct,
          sortCode: code,
        });
        if (mine !== attempt.current) return;
        onResolved(found);
      } catch (e) {
        if (mine !== attempt.current) return;
        onResolved(null);
        setError(
          e instanceof ApiError && e.status === 404
            ? "No account with that number at this bank. Check the digits."
            : e instanceof Error
              ? e.message
              : "Could not check that account.",
        );
      } finally {
        if (mine === attempt.current) setLooking(false);
      }
    },
    [userId, onResolved],
  );

  // Look the account up as soon as it can be looked up. Waiting for a button
  // press just means the sender presses two buttons instead of one.
  useEffect(() => {
    if (sortCode === "" || number.length !== ACCOUNT_DIGITS) {
      attempt.current++;
      onResolved(null);
      setError(null);
      setLooking(false);
      return;
    }
    void resolve(sortCode, number);
  }, [sortCode, number, resolve, onResolved]);

  if (banksError) {
    return (
      <div className="banner warn">
        <div>
          <strong>Bank transfers are unavailable</strong>
          {banksError}
        </div>
      </div>
    );
  }

  return (
    <div className="card stack">
      <div className="field">
        <label htmlFor="bank">Recipient&rsquo;s bank</label>
        <select id="bank" value={sortCode} onChange={(e) => setSortCode(e.target.value)}>
          <option value="">
            {banks.length === 0 ? "Loading banks…" : "Choose a bank"}
          </option>
          {banks.map((b) => (
            <option key={b.code} value={b.code}>
              {b.name}
            </option>
          ))}
        </select>
      </div>

      <div className="field">
        <label htmlFor="account">Account number</label>
        <input
          id="account"
          inputMode="numeric"
          autoComplete="off"
          placeholder="0123456789"
          value={number}
          onChange={(e) =>
            setNumber(e.target.value.replace(/\D/g, "").slice(0, ACCOUNT_DIGITS))
          }
        />
      </div>

      {looking && (
        <div className="muted">
          <span className="spinner" /> Checking the account…
        </div>
      )}

      {error && (
        <div className="banner danger">
          <div>{error}</div>
        </div>
      )}

      {account && !looking && (
        <div className="payee">
          <div className="payee-head">
            <span aria-hidden="true">✓</span> Money goes to
          </div>
          <div className="payee-body">
            <div className="payee-name">{account.accountName}</div>
            <div className="payee-account">
              {account.bankName ?? "Bank"} · {account.accountNumber}
            </div>
            <p className="payee-note">
              <strong>Check this is the right person.</strong> Once you hand the cash
              over, the money is gone.
            </p>
          </div>
        </div>
      )}
    </div>
  );
}

/* ---------------------------------------------------------------- capture */

function Capture({
  amount,
  error,
  onBack,
  onPhoto,
}: {
  amount: number;
  error: string | null;
  onBack: () => void;
  onPhoto: (photo: Blob) => void;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [live, setLive] = useState(false);
  const [cameraError, setCameraError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Always release the camera when leaving this screen: a live indicator that
  // stays on after the user has moved on is alarming, and rightly so.
  useEffect(() => {
    return () => {
      streamRef.current?.getTracks().forEach((t) => t.stop());
      streamRef.current = null;
    };
  }, []);

  async function start() {
    setCameraError(null);
    try {
      const stream = await openRearCamera();
      streamRef.current = stream;
      if (videoRef.current) {
        videoRef.current.srcObject = stream;
        await videoRef.current.play();
      }
      setLive(true);
    } catch (e) {
      setCameraError(e instanceof Error ? e.message : "Could not open the camera.");
    }
  }

  async function take() {
    if (!videoRef.current) return;
    setBusy(true);
    try {
      onPhoto(await captureFrame(videoRef.current));
    } catch (e) {
      setCameraError(e instanceof Error ? e.message : "Could not take the photo.");
      setBusy(false);
    }
  }

  async function pickFile(file: File | undefined) {
    if (!file) return;
    setBusy(true);
    try {
      onPhoto(await shrinkFile(file));
    } catch (e) {
      setCameraError(e instanceof Error ? e.message : "Could not read that photo.");
      setBusy(false);
    }
  }

  return (
    <div className="stack">
      <div className="viewfinder">
        {/* playsInline and muted are both required or iOS refuses to preview. */}
        <video ref={videoRef} playsInline muted autoPlay />
        <div className="guide" />
        <div className="hint">
          {live
            ? "Lay the notes in rows, none overlapping"
            : "Lay the cash out before you start"}
        </div>
      </div>

      <div className="card">
        <p className="card-title">Lay out {naira(amount, { decimals: false })}</p>
        <div className="notes-grid">
          {breakdown(amount).map((b) => (
            <span key={b.denomination} className="note-pill">
              {b.count} × {naira(b.denomination, { decimals: false })}
            </span>
          ))}
        </div>
        <div className="muted" style={{ marginTop: 10 }}>
          Any mix adding up to {naira(amount, { decimals: false })} works. Spread them out in
          rows so none overlap — overlapping notes are the main reason a count comes back
          wrong.
        </div>
      </div>

      {(cameraError || error) && (
        <div className="banner warn">
          <div>{cameraError ?? error}</div>
        </div>
      )}

      {!live ? (
        <button className="btn" onClick={start}>
          Open the camera
        </button>
      ) : (
        <button className="btn" onClick={take} disabled={busy}>
          {busy ? <span className="spinner" /> : "Take the photo"}
        </button>
      )}

      <input
        ref={fileRef}
        type="file"
        accept="image/*"
        capture="environment"
        hidden
        onChange={(e) => pickFile(e.target.files?.[0])}
      />
      <button className="btn ghost" onClick={() => fileRef.current?.click()} disabled={busy}>
        Use a photo from this phone instead
      </button>
      <button className="btn ghost" onClick={onBack}>
        Back
      </button>
    </div>
  );
}

/* ---------------------------------------------------------------- outcome */

function PledgeOutcome({
  result,
  onRetry,
  onDone,
}: {
  result: PledgeResult;
  onRetry: () => void;
  onDone: (transferId: string) => void;
}) {
  // The recogniser being down is not a verdict on the photograph, so it does
  // not get the refusal screen or an invitation to shoot it again.
  if (result.code === "vision_unavailable") {
    return (
      <div className="stack">
        <div className="banner warn">
          <div>
            <strong>We cannot count notes right now</strong>
            {result.reason}
          </div>
        </div>
        <div className="muted center">
          Nothing was pledged and no cash is committed. Your money is where it was.
        </div>
        <button className="btn" onClick={onRetry}>
          Try again
        </button>
      </div>
    );
  }

  if (!result.accepted) {
    const miscounted = result.code === "amount_mismatch" || result.code === "low_confidence";
    return (
      <div className="stack">
        <div className="banner danger">
          <div>
            <strong>That pledge was not accepted</strong>
            {result.reason}
          </div>
        </div>
        {result.vision && <VisionSummary result={result} />}
        {miscounted && (
          <div className="banner warn">
            <div>
              <strong>If the notes on the table are right, the photo is the problem</strong>
              Spread them into rows with none overlapping, get the whole layout in frame, and
              try again in better light.
            </div>
          </div>
        )}
        <button className="btn" onClick={onRetry}>
          Photograph it again
        </button>
      </div>
    );
  }

  const t = result.transfer!;
  const who = t.bank?.accountName ?? t.recipientName ?? "The recipient";
  return (
    <div className="stack">
      <div className="banner info">
        <div>
          <strong>
            {t.mode === "credit"
              ? `${who} has been paid already`
              : `${who} is expecting the money`}
          </strong>
          {t.mode === "credit"
            ? "Your instant credit covered it. Hand the cash over to settle."
            : "It is sent the moment you hand the cash to the counterparty below."}
        </div>
      </div>
      {result.vision && <VisionSummary result={result} />}
      <button className="btn" onClick={() => onDone(t.id)}>
        See the handover
      </button>
    </div>
  );
}

function VisionSummary({ result }: { result: PledgeResult }) {
  const v = result.vision!;
  const counted = tally(v.notes ?? []);
  const confident = v.confidence >= 0.75;

  return (
    <div className="card">
      <p className="card-title">What the photo showed</p>
      <div className="spread">
        <div style={{ fontSize: 26, fontWeight: 700 }}>{naira(v.totalKobo)}</div>
        <div className={`chip ${confident ? "good" : "warn"}`}>
          {Math.round(v.confidence * 100)}% sure of the count
        </div>
      </div>
      <div className="notes-grid">
        {counted.length === 0 && <span className="note-pill">no notes recognised</span>}
        {counted.map((c) => (
          <span key={c.denomination} className="note-pill">
            {c.count} × {naira(c.denomination, { decimals: false })}
          </span>
        ))}
      </div>
      {v.warnings?.length ? (
        <div className="muted" style={{ marginTop: 12 }}>
          {v.warnings.join(" ")}
        </div>
      ) : null}
    </div>
  );
}
