import type {
  Audit,
  CashoutRequest,
  Incident,
  IncidentKind,
  PledgeResult,
  Transfer,
  User,
  Venue,
} from "./types";

const BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`${path} failed: ${res.status}`);
  return res.json();
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok && !("code" in data)) {
    throw new Error(data?.reason ?? data?.error ?? `${path} failed: ${res.status}`);
  }
  return data as T;
}

export const api = {
  users: () => get<User[]>("/v1/users"),
  user: (id: string) => get<User>(`/v1/users/${id}`),
  transfers: (userId: string) => get<Transfer[]>(`/v1/users/${userId}/transfers`),
  transfer: (id: string) => get<Transfer>(`/v1/transfers/${id}`),
  // Only ever your own. There is no endpoint listing everybody's open requests:
  // that would publish who is about to be holding cash, and where.
  cashouts: (userId: string) => get<CashoutRequest[]>(`/v1/cashouts?userId=${userId}`),
  venues: (operatorId?: string) =>
    get<Venue[]>(operatorId ? `/v1/venues?operatorId=${operatorId}` : "/v1/venues"),
  audit: () => get<Audit>("/v1/ledger/audit"),

  /** Uploads the photograph as multipart, which is what phones handle best. */
  async pledge(input: {
    senderId: string;
    recipientId: string;
    amountKobo: number;
    note?: string;
    photo: Blob;
  }): Promise<PledgeResult> {
    const form = new FormData();
    form.set("senderId", input.senderId);
    form.set("recipientId", input.recipientId);
    form.set("amountKobo", String(input.amountKobo));
    form.set("note", input.note ?? "");
    form.set("photo", input.photo, "cash.jpg");

    const res = await fetch(`${BASE}/v1/pledge`, { method: "POST", body: form });
    const data = await res.json().catch(() => ({}));
    if (!res.ok && data?.accepted === undefined && !data?.code) {
      throw new Error(data?.error ?? `Pledge failed: ${res.status}`);
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
