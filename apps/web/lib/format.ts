/** Renders an integer kobo amount the way a Nigerian user expects to read it. */
export function naira(kobo: number, opts: { decimals?: boolean } = {}): string {
  const negative = kobo < 0;
  const abs = Math.abs(kobo);
  const whole = Math.floor(abs / 100);
  const rem = abs % 100;

  const grouped = whole.toLocaleString("en-NG");
  const showDecimals = opts.decimals ?? rem !== 0;
  const body = showDecimals ? `${grouped}.${String(rem).padStart(2, "0")}` : grouped;
  return `${negative ? "-" : ""}₦${body}`;
}

export function distance(metres: number): string {
  if (metres < 1000) return `${metres}m away`;
  return `${(metres / 1000).toFixed(1)}km away`;
}

/** Counts down to an ISO deadline, e.g. "4:12 left". */
export function timeLeft(iso: string, now = Date.now()): string {
  const ms = new Date(iso).getTime() - now;
  if (ms <= 0) return "expired";
  const total = Math.floor(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")} left`;
}

export function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

const STATE_LABELS: Record<string, string> = {
  draft: "Starting",
  pledged: "Looking for a counterparty",
  credited: "Delivered, awaiting settlement",
  matched: "Counterparty found",
  handover_pending: "Waiting on the other side",
  settled: "Settled",
  expired: "Expired",
  rejected: "Notes refused",
  voided: "Cancelled",
  defaulted: "Unsettled",
  disputed: "Under review",
};

export function stateLabel(state: string): string {
  return STATE_LABELS[state] ?? state;
}

const DENOMINATIONS = [100000, 50000, 20000, 10000, 5000, 2000, 1000, 500];

/**
 * The canonical largest-first way to make up an amount in real banknotes.
 *
 * Shown before capture so the sender knows what to lay out. It is guidance for
 * the person, never sent to the recognizer — telling the model what to expect
 * would anchor it, and the case that matters most is the one where the
 * expectation is wrong.
 */
export function breakdown(kobo: number): { denomination: number; count: number }[] {
  let left = kobo;
  const out: { denomination: number; count: number }[] = [];
  for (const d of DENOMINATIONS) {
    const count = Math.floor(left / d);
    if (count > 0) {
      out.push({ denomination: d, count });
      left -= count * d;
    }
  }
  return out;
}

/** Groups recognised notes into "20 × ₦1,000" tallies, largest first. */
export function tally(
  notes: { denominationKobo: number }[],
): { denomination: number; count: number }[] {
  const counts = new Map<number, number>();
  for (const n of notes) {
    counts.set(n.denominationKobo, (counts.get(n.denominationKobo) ?? 0) + 1);
  }
  return [...counts.entries()]
    .sort((a, b) => b[0] - a[0])
    .map(([denomination, count]) => ({ denomination, count }));
}
