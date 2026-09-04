"use client";

import { useCallback, useEffect, useState } from "react";

const KEY = "tender.userId";

/**
 * Which account this device is signed in as.
 *
 * Kept in localStorage so each phone stays on its own account across reloads.
 * Reads are wrapped because private browsing and blocked site data both make
 * storage throw rather than return null.
 */
export function useSession() {
  const [userId, setUserId] = useState<string | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    try {
      setUserId(window.localStorage.getItem(KEY));
    } catch {
      // No storage available; the picker will simply show every reload.
    }
    setReady(true);
  }, []);

  const signIn = useCallback((id: string) => {
    setUserId(id);
    try {
      window.localStorage.setItem(KEY, id);
    } catch {}
  }, []);

  const signOut = useCallback(() => {
    setUserId(null);
    try {
      window.localStorage.removeItem(KEY);
    } catch {}
  }, []);

  return { userId, ready, signIn, signOut };
}
