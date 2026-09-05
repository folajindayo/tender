"use client";

import { useState } from "react";

type Mode = "signin" | "signup";

/**
 * The sign-in screen. Tender holds money, so a device is never simply "in" —
 * it is signed in as a specific account, and the token behind that is what
 * every request carries.
 *
 * There is no email verification step: an unverified address is still a real
 * credential for signing back in, and a verification wall between signing up
 * and using the app buys nothing at this stage.
 */
export function Auth({
  onSignIn,
  onSignUp,
}: {
  onSignIn: (email: string, password: string) => Promise<void>;
  onSignUp: (input: {
    email: string;
    password: string;
    displayName: string;
    phone: string;
    city: string;
  }) => Promise<void>;
}) {
  const [mode, setMode] = useState<Mode>("signin");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [phone, setPhone] = useState("");
  const [city, setCity] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const canSubmit =
    email.trim() !== "" &&
    password.length >= 8 &&
    (mode === "signin" || displayName.trim() !== "");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSubmit || busy) return;
    setBusy(true);
    setError(null);
    try {
      if (mode === "signin") {
        await onSignIn(email.trim(), password);
      } else {
        await onSignUp({
          email: email.trim(),
          password,
          displayName: displayName.trim(),
          phone: phone.trim(),
          city: city.trim(),
        });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not reach Tender.");
      setBusy(false);
    }
  }

  function swap(to: Mode) {
    setMode(to);
    setError(null);
    setPassword("");
  }

  return (
    <div className="app">
      <header className="header">
        <div className="wordmark">
          ten<span>der</span>
        </div>
      </header>

      <main>
        <div className="stack">
          <h1 style={{ fontSize: 24, margin: 0, letterSpacing: "-0.02em" }}>
            {mode === "signin" ? "Sign in" : "Create your account"}
          </h1>
          <p className="muted" style={{ margin: 0 }}>
            Tender moves physical cash without a bank. Your notes go to someone nearby
            who wanted cash; the value goes to the account you send it to.
          </p>
        </div>

        <form className="card stack" onSubmit={submit}>
          {mode === "signup" && (
            <div className="field">
              <label htmlFor="name">Your name</label>
              <input
                id="name"
                autoComplete="name"
                placeholder="Ada Okafor"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                maxLength={60}
              />
            </div>
          )}

          <div className="field">
            <label htmlFor="email">Email</label>
            <input
              id="email"
              type="email"
              inputMode="email"
              autoCapitalize="none"
              autoCorrect="off"
              autoComplete={mode === "signin" ? "username" : "email"}
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>

          <div className="field">
            <label htmlFor="password">Password</label>
            <input
              id="password"
              type="password"
              autoComplete={mode === "signin" ? "current-password" : "new-password"}
              placeholder={mode === "signup" ? "at least 8 characters" : ""}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>

          {mode === "signup" && (
            <>
              <div className="field">
                <label htmlFor="phone">Phone number (optional)</label>
                <input
                  id="phone"
                  type="tel"
                  inputMode="tel"
                  autoComplete="tel"
                  placeholder="0803 000 0000"
                  value={phone}
                  onChange={(e) => setPhone(e.target.value)}
                  maxLength={20}
                />
              </div>
              <div className="field">
                <label htmlFor="city">City (optional)</label>
                <input
                  id="city"
                  autoComplete="address-level2"
                  placeholder="Lagos"
                  value={city}
                  onChange={(e) => setCity(e.target.value)}
                  maxLength={40}
                />
                <div className="muted">
                  Used to find someone near you who wants the cash you are holding.
                </div>
              </div>
            </>
          )}

          {error && (
            <div className="banner danger">
              <div>{error}</div>
            </div>
          )}

          <button className="btn" type="submit" disabled={!canSubmit || busy}>
            {busy ? (
              <span className="spinner" />
            ) : mode === "signin" ? (
              "Sign in"
            ) : (
              "Create account"
            )}
          </button>
        </form>

        <button
          className="btn ghost"
          onClick={() => swap(mode === "signin" ? "signup" : "signin")}
        >
          {mode === "signin"
            ? "I do not have an account yet"
            : "I already have an account"}
        </button>
      </main>
    </div>
  );
}
