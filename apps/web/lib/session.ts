"use client";

import { useCallback, useEffect, useState } from "react";

import { api, loadToken, setToken } from "@/lib/api";
import type { User } from "@/lib/types";

/**
 * Who is signed in on this device.
 *
 * The token in localStorage is only a claim; the account itself always comes
 * from GET /v1/auth/me, so a revoked or expired session lands on the sign-in
 * screen rather than on a stale copy of somebody's balance.
 */
export function useSession() {
  const [me, setMe] = useState<User | null>(null);
  const [ready, setReady] = useState(false);

  const refresh = useCallback(async () => {
    if (!loadToken()) {
      setMe(null);
      return;
    }
    try {
      setMe(await api.me());
    } catch {
      // Unauthorized, or the API is unreachable. Either way this device has no
      // account it can act as, and clearing the token avoids a retry loop.
      setToken(null);
      setMe(null);
    }
  }, []);

  useEffect(() => {
    void refresh().finally(() => setReady(true));
  }, [refresh]);

  const signIn = useCallback(async (email: string, password: string) => {
    const session = await api.signin({ email, password });
    setToken(session.token);
    setMe(session.user);
  }, []);

  const signUp = useCallback(
    async (input: {
      email: string;
      password: string;
      displayName: string;
      phone: string;
      city: string;
    }) => {
      const session = await api.signup(input);
      setToken(session.token);
      setMe(session.user);
    },
    [],
  );

  const signOut = useCallback(async () => {
    // Revoke server-side first, but drop the local session either way: a phone
    // that cannot reach the API must still be able to sign out of it.
    try {
      await api.signout();
    } catch {}
    setToken(null);
    setMe(null);
  }, []);

  return { me, setMe, ready, refresh, signIn, signUp, signOut };
}
