import type {
  Audit,
  Bank,
  BankAccount,
  CashoutRequest,
  Incident,
  IncidentKind,
  PledgeResult,
  Session,
  Transfer,
  User,
  Venue,
} from "./types";

const BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

/**
 * The session token, held in memory and mirrored to localStorage by lib/session.
 *
 * The API also sets an HttpOnly cookie, but the PWA and the API are on
 * different origins, so that cookie is third-party and some browsers drop it
 * outright. The bearer header is what actually keeps a phone signed in;
 * credentials: "include" is there for the browsers that do keep the cookie.
 */
const TOKEN_KEY = "tender.token";
let token: string | null = null;

export function setToken(value: string | null) {
  token = value;
  try {
    if (value) window.localStorage.setItem(TOKEN_KEY, value);
    else window.localStorage.removeItem(TOKEN_KEY);
  } catch {
    // Private browsing: the token still works for this tab's lifetime.
  }
}

export function loadToken(): string | null {
  try {
    token = window.localStorage.getItem(TOKEN_KEY);
  } catch {
    token = null;
  }
  return token;
}

function authHeaders(base: Record<string, string> = {}): Record<string, string> {
  return token ? { ...base, Authorization: `Bearer ${token}` } : base;
}

/** Thrown for any non-2xx response, carrying the API's own wording. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function body(res: Response): Promise<Record<string, unknown>> {
  return (await res.json().catch(() => ({}))) as Record<string, unknown>;
}

function reasonOf(data: Record<string, unknown>, fallback: string): string {
  const text = data.error ?? data.reason;
  return typeof text === "string" && text !== "" ? text : fallback;
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    cache: "no-store",
    credentials: "include",
    headers: authHeaders(),
  });
  if (!res.ok) throw new ApiError(reasonOf(await body(res), `${path} failed`), res.status);
  return res.json();
}

async function post<T>(path: string, payload: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    credentials: "include",
    headers: authHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(payload),
  });
  const data = await body(res);
  // A refused pledge or handover is a normal outcome carrying a `code`, not a
  // transport failure, so those come back to the caller intact.
  if (!res.ok && !("code" in data)) {
    throw new ApiError(reasonOf(data, `${path} failed`), res.status);
  }
  return data as T;
}

export const api = {
  signup: (input: {
    email: string;
    password: string;
    displayName: string;
    phone: string;
    city: string;
  }) => post<Session>("/v1/auth/signup", input),
  signin: (input: { email: string; password: string }) =>
    post<Session>("/v1/auth/signin", input),
  signout: () => post<{ ok: boolean }>("/v1/auth/signout", {}),
  me: () => get<User>("/v1/auth/me"),

  user: (id: string) => get<User>(`/v1/users/${id}`),
  transfers: (userId: string) => get<Transfer[]>(`/v1/users/${userId}/transfers`),
  transfer: (id: string) => get<Transfer>(`/v1/transfers/${id}`),
  // Only ever your own. There is no endpoint listing everybody's open requests:
  // that would publish who is about to be holding cash, and where.
  cashouts: (userId: string) => get<CashoutRequest[]>(`/v1/cashouts?userId=${userId}`),
  venues: (operatorId?: string) =>
    get<Venue[]>(operatorId ? `/v1/venues?operatorId=${operatorId}` : "/v1/venues"),
  audit: () => get<Audit>("/v1/ledger/audit"),

  /** The banks a recipient account can belong to. */
  banks: () => get<Bank[]>("/v1/banks"),

  /**
   * Name enquiry: turns a typed account number into the name the bank holds.
   * The sender reads this back before any cash changes hands, which is what
   * makes typing an account number safe.
   */
  resolveAccount: (input: { userId: string; accountNumber: string; sortCode: string }) =>
    post<BankAccount>("/v1/accounts/resolve", input),

  /** Uploads the photograph as multipart, which is what phones handle best. */
  async pledge(input: {
    senderId: string;
    account: BankAccount;
    amountKobo: number;
    note?: string;
    photo: Blob;
  }): Promise<PledgeResult> {
    const form = new FormData();
    form.set("senderId", input.senderId);
    form.set("accountNumber", input.account.accountNumber);
    form.set("sortCode", input.account.sortCode);
    form.set("accountName", input.account.accountName);
    form.set("bankName", input.account.bankName ?? "");
    form.set("amountKobo", String(input.amountKobo));
    form.set("note", input.note ?? "");
    form.set("photo", input.photo, "cash.jpg");

    const res = await fetch(`${BASE}/v1/pledge`, {
      method: "POST",
      credentials: "include",
      headers: authHeaders(),
      body: form,
    });
    const data = await body(res);
    if (!res.ok && data?.accepted === undefined && !data?.code) {
      throw new ApiError(reasonOf(data, "Pledge failed"), res.status);
    }
    return data as PledgeResult;
  },

  confirm: (transferId: string, userId: string, code?: string) =>
    post<Transfer & { code?: string; reason?: string }>(
      `/v1/transfers/${transferId}/confirm`,
      { userId, code: code ?? "" },
    ),

  reject: (transferId: string, userId: string, reason: string) =>
    post<Transfer & { code?: string; reason?: string }>(
      `/v1/transfers/${transferId}/reject`,
      { userId, reason },
    ),

  rematch: (transferId: string) =>
    post<{ matched: boolean; reason?: string }>(`/v1/transfers/${transferId}/match`, {}),

  requestCash: (input: {
    userId: string;
    amountKobo: number;
    toleranceKobo?: number;
    venueId: string;
  }) => post<{ id: string }>("/v1/cashouts", { toleranceKobo: 0, ...input }),

  /** Reports a handover that went wrong. This holds the money rather than
   *  letting the expiry sweeper quietly release it. */
  reportIncident: (transferId: string, userId: string, kind: IncidentKind, detail: string) =>
    post<Incident & { code?: string; reason?: string }>(
      `/v1/transfers/${transferId}/incident`,
      { userId, kind, detail },
    ),
};

export type StreamEvent = { type: string; data?: unknown };

/**
 * Opens the server-sent event stream for one user. The recipient of a transfer
 * is watching a clock, so state is pushed rather than polled.
 */
export function subscribe(userId: string, onEvent: (e: StreamEvent) => void): () => void {
  const source = new EventSource(`${BASE}/v1/stream?userId=${userId}`);
  source.onmessage = (msg) => {
    try {
      onEvent(JSON.parse(msg.data));
    } catch {
      // A malformed frame should not tear down the stream.
    }
  };
  // EventSource reconnects on its own; nothing to do but let it.
  source.onerror = () => {};
  return () => source.close();
}
