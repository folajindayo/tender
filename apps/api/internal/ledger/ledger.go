// Package ledger is the only place in Tender where money moves.
//
// Every movement is a set of signed entries sharing a transaction id, and every
// set must sum to exactly zero. The database enforces this with a deferred
// constraint trigger, so an unbalanced write cannot be committed even by a bug.
//
// Sign convention:
//
//	user available  positive = spendable balance
//	user escrow     positive = locked, pending a physical handover
//	user obligation NEGATIVE = the user owes the platform this much
//	float           positive = settlement capital available to lend
//	revenue         positive = fees earned
//	payable         positive = owed to a bank account, not yet paid out
package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/money"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const (
	KindAvailable   = "available"
	KindEscrow      = "escrow"
	KindObligation  = "obligation"
	KindFloat       = "float"
	KindRevenue     = "revenue"
	KindLossReserve = "loss_reserve"
	KindPayable     = "payable"
	KindExternal    = "external"
)

// Entry is one leg of a ledger transaction.
type Entry struct {
	AccountID uuid.UUID
	Amount    money.Kobo
	Reason    string
}

// AccountFor resolves a user account, creating it on first use. Pass a nil
// userID for the system accounts (float, revenue, loss_reserve).
func AccountFor(ctx context.Context, q Querier, userID *uuid.UUID, kind string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		INSERT INTO accounts (user_id, kind) VALUES ($1, $2::account_kind)
		ON CONFLICT (user_id, kind) DO UPDATE SET kind = EXCLUDED.kind
		RETURNING id`, userID, kind).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve %s account: %w", kind, err)
	}
	return id, nil
}

func userAccount(ctx context.Context, q Querier, u uuid.UUID, kind string) (uuid.UUID, error) {
	return AccountFor(ctx, q, &u, kind)
}

func systemAccount(ctx context.Context, q Querier, kind string) (uuid.UUID, error) {
	return AccountFor(ctx, q, nil, kind)
}

// Post writes a balanced set of entries.
//
// It refuses to issue the write at all if the entries do not sum to zero, so
// the caller gets a clear error rather than a deferred trigger failure at
// commit time.
//
// The entries of one transaction must land inside a single database
// transaction, or the deferred balance trigger fires after the first row and
// rejects it. When handed a bare pool, Post therefore opens its own
// transaction; when handed an existing pgx.Tx it joins the caller's.
func Post(ctx context.Context, q Querier, transferID *uuid.UUID, entries []Entry) (uuid.UUID, error) {
	if len(entries) < 2 {
		return uuid.Nil, fmt.Errorf("ledger: a transaction needs at least two entries, got %d", len(entries))
	}

	var sum money.Kobo
	for _, e := range entries {
		if e.Amount == 0 {
			return uuid.Nil, fmt.Errorf("ledger: zero-amount entry (%s) is not a movement", e.Reason)
		}
		sum += e.Amount
	}
	if sum != 0 {
		return uuid.Nil, fmt.Errorf("ledger: unbalanced transaction, entries sum to %d kobo", sum)
	}

	if pool, isPool := q.(*pgxpool.Pool); isPool {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return uuid.Nil, err
		}
		defer tx.Rollback(ctx)

		txID, err := insertEntries(ctx, tx, transferID, entries)
		if err != nil {
			return uuid.Nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, fmt.Errorf("ledger: commit: %w", err)
		}
		return txID, nil
	}

	return insertEntries(ctx, q, transferID, entries)
}

func insertEntries(ctx context.Context, q Querier, transferID *uuid.UUID, entries []Entry) (uuid.UUID, error) {
	txID := uuid.New()
	for _, e := range entries {
		_, err := q.Exec(ctx, `
			INSERT INTO ledger_entries (tx_id, account_id, amount_kobo, reason, transfer_id)
			VALUES ($1, $2, $3, $4, $5)`,
			txID, e.AccountID, int64(e.Amount), e.Reason, transferID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("ledger: post %q: %w", e.Reason, err)
		}
	}
	return txID, nil
}

// Balance returns the current balance of one user account.
func Balance(ctx context.Context, q Querier, userID uuid.UUID, kind string) (money.Kobo, error) {
	var bal int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(e.amount_kobo), 0)
		  FROM accounts a LEFT JOIN ledger_entries e ON e.account_id = a.id
		 WHERE a.user_id = $1 AND a.kind = $2::account_kind`, userID, kind).Scan(&bal)
	return money.Kobo(bal), err
}

// ---------------------------------------------------------------- operations

// LockEscrow moves a counterparty's digital funds out of reach while they go to
// meet the sender. Nothing is promised to anyone until this succeeds.
func LockEscrow(ctx context.Context, q Querier, transferID uuid.UUID, counterparty uuid.UUID, amount money.Kobo) error {
	avail, err := userAccount(ctx, q, counterparty, KindAvailable)
	if err != nil {
		return err
	}
	esc, err := userAccount(ctx, q, counterparty, KindEscrow)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{avail, -amount, "escrow.lock"},
		{esc, amount, "escrow.lock"},
	})
	return err
}

// ReleaseEscrow undoes a lock when a match expires or the notes are refused.
// This is the path that makes "the sender never showed up" cost nobody anything.
func ReleaseEscrow(ctx context.Context, q Querier, transferID uuid.UUID, counterparty uuid.UUID, amount money.Kobo, reason string) error {
	avail, err := userAccount(ctx, q, counterparty, KindAvailable)
	if err != nil {
		return err
	}
	esc, err := userAccount(ctx, q, counterparty, KindEscrow)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{esc, -amount, reason},
		{avail, amount, reason},
	})
	return err
}

// SettleEscrow completes a Tier 0 transfer: the counterparty has taken physical
// possession of the notes, so their locked digital funds are released to the
// recipient, less the platform fee.
func SettleEscrow(ctx context.Context, q Querier, transferID, counterparty, recipient uuid.UUID, amount, fee money.Kobo) error {
	esc, err := userAccount(ctx, q, counterparty, KindEscrow)
	if err != nil {
		return err
	}
	rcpt, err := userAccount(ctx, q, recipient, KindAvailable)
	if err != nil {
		return err
	}
	rev, err := systemAccount(ctx, q, KindRevenue)
	if err != nil {
		return err
	}
	entries := []Entry{
		{esc, -amount, "settle.escrow"},
		{rcpt, amount - fee, "settle.credit_recipient"},
	}
	if fee > 0 {
		entries = append(entries, Entry{rev, fee, "settle.fee"})
	}
	_, err = Post(ctx, q, &transferID, entries)
	return err
}

// ExtendCredit is the Tier 1 path: the platform fronts the recipient from float
// and books a receivable against the sender. This is the only point in the
// system where the platform carries risk, which is why the limit is earned.
func ExtendCredit(ctx context.Context, q Querier, transferID, sender, recipient uuid.UUID, amount, fee money.Kobo) error {
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	rev, err := systemAccount(ctx, q, KindRevenue)
	if err != nil {
		return err
	}
	rcpt, err := userAccount(ctx, q, recipient, KindAvailable)
	if err != nil {
		return err
	}
	obl, err := userAccount(ctx, q, sender, KindObligation)
	if err != nil {
		return err
	}
	entries := []Entry{
		// fund the recipient out of float
		{flt, -amount, "credit.fund"},
		{rcpt, amount - fee, "credit.credit_recipient"},
		// book the receivable: float swaps cash for a claim on the sender
		{obl, -amount, "credit.obligation"},
		{flt, amount, "credit.receivable"},
	}
	if fee > 0 {
		entries = append(entries, Entry{rev, fee, "credit.fee"})
	}
	_, err = Post(ctx, q, &transferID, entries)
	return err
}

// RepayCreditFromEscrow clears a Tier 1 receivable once the counterparty has
// taken the cash: their escrowed funds restore float, and float releases its
// claim on the sender.
func RepayCreditFromEscrow(ctx context.Context, q Querier, transferID, counterparty, sender uuid.UUID, amount money.Kobo) error {
	esc, err := userAccount(ctx, q, counterparty, KindEscrow)
	if err != nil {
		return err
	}
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	obl, err := userAccount(ctx, q, sender, KindObligation)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{esc, -amount, "repay.escrow"},
		{flt, amount, "repay.float_restored"},
		{flt, -amount, "repay.claim_released"},
		{obl, amount, "repay.obligation_cleared"},
	})
	return err
}

// RecoverFromBalance claws back part of a defaulted obligation from whatever
// the sender holds on the platform. This is the first recovery step before the
// remainder is written off.
func RecoverFromBalance(ctx context.Context, q Querier, transferID, sender uuid.UUID, amount money.Kobo) error {
	avail, err := userAccount(ctx, q, sender, KindAvailable)
	if err != nil {
		return err
	}
	obl, err := userAccount(ctx, q, sender, KindObligation)
	if err != nil {
		return err
	}
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{avail, -amount, "recover.from_balance"},
		{flt, amount, "recover.float_restored"},
		{flt, -amount, "recover.claim_released"},
		{obl, amount, "recover.obligation_cleared"},
	})
	return err
}

// WriteOff absorbs an unrecoverable obligation into the loss reserve. Tier 1
// limits are sized so that expected write-offs stay below fee revenue.
func WriteOff(ctx context.Context, q Querier, transferID, sender uuid.UUID, amount money.Kobo) error {
	obl, err := userAccount(ctx, q, sender, KindObligation)
	if err != nil {
		return err
	}
	loss, err := systemAccount(ctx, q, KindLossReserve)
	if err != nil {
		return err
	}
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{obl, amount, "writeoff.obligation_cleared"},
		{flt, -amount, "writeoff.claim_lost"},
		{flt, amount, "writeoff.absorbed"},
		{loss, -amount, "writeoff.loss"},
	})
	return err
}

// FundFloat seeds settlement capital from outside the system.
func FundFloat(ctx context.Context, q Querier, amount money.Kobo) error {
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	ext, err := systemAccount(ctx, q, KindExternal)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, nil, []Entry{
		{flt, amount, "float.funded"},
		{ext, -amount, "float.capital_in"},
	})
	return err
}

// Deposit brings value into the system from the banking world: somebody paid
// money into the platform and now holds a spendable balance against it.
func Deposit(ctx context.Context, q Querier, user uuid.UUID, amount money.Kobo) error {
	avail, err := userAccount(ctx, q, user, KindAvailable)
	if err != nil {
		return err
	}
	ext, err := systemAccount(ctx, q, KindExternal)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, nil, []Entry{
		{avail, amount, "deposit.in"},
		{ext, -amount, "deposit.external"},
	})
	return err
}

// ---------------------------------------------------------------- bank payouts

// SettleToPayable settles a handover whose recipient is a bank account.
//
// It is SettleEscrow with a different destination: the counterparty's escrow is
// consumed, but the money lands in 'payable' rather than a Tender balance,
// because it has been earned by the recipient and not yet reached their bank.
// Holding it there means the audit view always shows what Tender owes the
// outside world but has not yet delivered.
func SettleToPayable(ctx context.Context, q Querier, transferID, counterparty uuid.UUID, amount, fee money.Kobo) error {
	esc, err := userAccount(ctx, q, counterparty, KindEscrow)
	if err != nil {
		return err
	}
	pay, err := systemAccount(ctx, q, KindPayable)
	if err != nil {
		return err
	}
	rev, err := systemAccount(ctx, q, KindRevenue)
	if err != nil {
		return err
	}
	entries := []Entry{
		{esc, -amount, "settle.escrow"},
		{pay, amount - fee, "settle.payable"},
	}
	if fee > 0 {
		entries = append(entries, Entry{rev, fee, "settle.fee"})
	}
	_, err = Post(ctx, q, &transferID, entries)
	return err
}

// PayoutSent records money actually leaving Tender for a bank account. This is
// the only point at which the obligation is discharged, so it is driven by the
// provider confirming the credit -- never by our own request succeeding.
func PayoutSent(ctx context.Context, q Querier, transferID uuid.UUID, amount money.Kobo) error {
	pay, err := systemAccount(ctx, q, KindPayable)
	if err != nil {
		return err
	}
	ext, err := systemAccount(ctx, q, KindExternal)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{pay, -amount, "payout.settled"},
		{ext, amount, "payout.out"},
	})
	return err
}

// PayoutReturned handles a payout the bank would not accept.
//
// The cash is already gone: the sender handed it to a counterparty who has been
// paid. So the value cannot simply be cancelled, and it cannot go back to the
// counterparty either. It belongs to the sender, who is the person now holding
// nothing -- they can correct the account number and send it on, or withdraw
// it. Anything else would quietly keep money that Tender did not earn.
func PayoutReturned(ctx context.Context, q Querier, transferID, sender uuid.UUID, amount money.Kobo) error {
	pay, err := systemAccount(ctx, q, KindPayable)
	if err != nil {
		return err
	}
	avail, err := userAccount(ctx, q, sender, KindAvailable)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, &transferID, []Entry{
		{pay, -amount, "payout.returned"},
		{avail, amount, "payout.refund_sender"},
	})
	return err
}

// FundFloatFromBank records money arriving in the Fintava wallet that backs
// Tender's balances. It is FundFloat with a provider reference attached, so the
// audit trail ties a float increase to a specific bank credit.
func FundFloatFromBank(ctx context.Context, q Querier, amount money.Kobo, reference string) error {
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	ext, err := systemAccount(ctx, q, KindExternal)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, nil, []Entry{
		{flt, amount, "float.funded:" + reference},
		{ext, -amount, "float.capital_in"},
	})
	return err
}

// AllocateFromFloat moves platform capital into a user's spendable balance.
//
// This is how a counterparty is put in funds before there are customers paying
// money in: the platform lends its own float to somebody willing to hand out
// cash, so the demand side of the market can exist at all.
//
// It moves rather than mints, which is the point. The wallet at the bank backs
// every claim in the books, so float going down by exactly what a balance goes
// up by keeps the total backed. Crediting a user with Deposit instead would
// draw on `external` and leave two claims against the same money.
func AllocateFromFloat(ctx context.Context, q Querier, user uuid.UUID, amount money.Kobo, reference string) error {
	if amount <= 0 {
		return fmt.Errorf("ledger: an allocation must be positive")
	}
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	avail, err := userAccount(ctx, q, user, KindAvailable)
	if err != nil {
		return err
	}
	_, err = Post(ctx, q, nil, []Entry{
		{avail, amount, "float.allocated:" + reference},
		{flt, -amount, "float.allocated_out:" + reference},
	})
	return err
}

// ReconcileFloat records money that is in the settlement wallet but not in the
// books -- an opening balance carried in from before Tender existed, a bank fee
// the provider took without telling us, a credit no webhook ever arrived for.
//
// It is deliberately the same double entry as an ordinary funding: float
// against external, because the money genuinely did come from outside the
// system. What differs is the reason, which records that a human asserted this
// rather than a webhook reporting it -- so a later reader can tell a
// reconciliation from a movement Tender actually observed.
//
// A negative amount is allowed and means the opposite: the wallet holds less
// than the books think, which is what a fee looks like.
//
// Idempotent on reference. Reconciliation is the one operation most likely to
// be run twice by a nervous operator, and running it twice must not invent
// money.
func ReconcileFloat(ctx context.Context, q Querier, amount money.Kobo, reference, note string) (bool, error) {
	if amount == 0 {
		return false, fmt.Errorf("ledger: a zero reconciliation is not a movement")
	}
	reason := "float.reconciled:" + reference

	var exists bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ledger_entries WHERE reason = $1)`, reason).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return false, err
	}
	ext, err := systemAccount(ctx, q, KindExternal)
	if err != nil {
		return false, err
	}

	counter := "float.reconciliation_source"
	if note != "" {
		counter += ":" + note
	}
	if _, err := Post(ctx, q, nil, []Entry{
		{flt, amount, reason},
		{ext, -amount, counter},
	}); err != nil {
		return false, err
	}
	return true, nil
}

// ExtendCreditToPayable is ExtendCredit for a bank recipient.
//
// The difference is only where the money lands: a Tender recipient is credited
// directly, a bank recipient is owed. Tier 1 is riskier in this shape, because
// the money leaves the building before the sender has settled -- so the payout
// is real, immediate, and unrecoverable if the sender defaults. That is exactly
// what the credit limit exists to bound.
func ExtendCreditToPayable(ctx context.Context, q Querier, transferID, sender uuid.UUID, amount, fee money.Kobo) error {
	flt, err := systemAccount(ctx, q, KindFloat)
	if err != nil {
		return err
	}
	rev, err := systemAccount(ctx, q, KindRevenue)
	if err != nil {
		return err
	}
	pay, err := systemAccount(ctx, q, KindPayable)
	if err != nil {
		return err
	}
	obl, err := userAccount(ctx, q, sender, KindObligation)
	if err != nil {
		return err
	}
	entries := []Entry{
		{flt, -amount, "credit.fund"},
		{pay, amount - fee, "credit.payable"},
		{obl, -amount, "credit.obligation"},
		{flt, amount, "credit.receivable"},
	}
	if fee > 0 {
		entries = append(entries, Entry{rev, fee, "credit.fee"})
	}
	_, err = Post(ctx, q, &transferID, entries)
	return err
}
