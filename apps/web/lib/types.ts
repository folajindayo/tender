export type User = {
  id: string;
  phone: string;
  displayName: string;
  avatarEmoji: string;
  city: string;
  lat: number;
  lng: number;
  trustScore: number;
  settledCount: number;
  defaultedCount: number;
  creditLimitKobo: number;
  sendingFrozen: boolean;
  suspended: boolean;
  incidentCount: number;
  availableKobo: number;
  escrowKobo: number;
  owedKobo: number;
};

export type Match = {
  id: string;
  transferId: string;
  counterpartyId: string;
  counterpartyName: string;
  amountKobo: number;
  handoverCode: string;
  distanceM: number;
  venueName: string;
  venueAddress: string;
  state:
    | "proposed"
    | "sender_confirmed"
    | "counterparty_confirmed"
    | "completed"
    | "expired"
    | "rejected"
    | "disputed";
  expiresAt: string;
};

export type TransferState =
  | "draft"
  | "pledged"
  | "credited"
  | "matched"
  | "handover_pending"
  | "settled"
  | "expired"
  | "rejected"
  | "voided"
  | "defaulted"
  | "disputed"
  | "payout_failed";

/** A destination outside Tender. The account name is what the bank returned
 *  for the number, never what the sender typed. */
export type BankAccount = {
  accountNumber: string;
  accountName: string;
  sortCode: string;
  bankName?: string;
};

export type Bank = { code: string; name: string };

/** Where a settled bank transfer has actually got to. "Settled" and "arrived"
 *  are different claims and the sender is shown both. */
export type Payout = {
  state: "pending" | "submitting" | "sent" | "unknown" | "delivered" | "failed" | "returned";
  accountName: string;
  bankName?: string;
  reference?: string;
  lastError?: string;
};

export type Transfer = {
  id: string;
  ref: number;
  senderId: string;
  /** Exactly one of these is set. */
  recipientId?: string;
  bank?: BankAccount | null;
  amountKobo: number;
  feeKobo: number;
  mode: "escrow" | "credit";
  state: TransferState;
  note: string;
  createdAt: string;
  expiresAt?: string;
  settledAt?: string;
  senderName?: string;
  recipientName?: string;
  payout?: Payout | null;
  match?: Match | null;
};

export type Note = {
  denominationKobo: number;
  serial: string;
  serialConfidence: number;
  phash: string;
};

export type VisionResult = {
  notes: Note[];
  totalKobo: number;
  confidence: number;
  screenReplay: boolean;
  photocopySuspected: boolean;
  warnings: string[] | null;
  mode: string;
};

export type PledgeResult = {
  accepted: boolean;
  reason?: string;
  code?: string;
  vision?: VisionResult;
  transfer?: Transfer;
};

/** Fixed, public premises where a handover may take place. */
export type Venue = {
  id: string;
  name: string;
  kind: "agent" | "bank" | "filling_station" | "market_office";
  address: string;
  lat: number;
  lng: number;
  opensAt: string;
  closesAt: string;
  verified: boolean;
  openNow: boolean;
};

export type CashoutRequest = {
  id: string;
  userId: string;
  userName: string;
  amountKobo: number;
  toleranceKobo: number;
  venueId: string;
  venueName: string;
  address: string;
  state: string;
};

export type IncidentKind = "cash_taken" | "wrong_amount" | "threatened" | "no_show";

export type Incident = {
  id: string;
  transferId: string;
  reporterId: string;
  accusedId: string;
  kind: IncidentKind;
  detail: string;
  frozeEscrow: boolean;
  status: string;
  createdAt: string;
};

export type Audit = {
  lines: {
    txId: string;
    reason: string;
    account: string;
    owner: string;
    amountKobo: number;
    transferRef?: number;
    createdAt: string;
  }[];
  globalSumKobo: number;
  unbalancedTransactions: number;
  floatKobo: number;
  revenueKobo: number;
  capitalAtRiskKobo: number;
  escrowedKobo: number;
};

/** What POST /v1/auth/signin and /signup return. */
export type Session = { token: string; user: User };
