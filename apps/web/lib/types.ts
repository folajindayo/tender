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
  | "disputed";

export type Transfer = {
  id: string;
  ref: number;
  senderId: string;
  recipientId: string;
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
