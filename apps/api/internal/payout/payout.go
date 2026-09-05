// Package payout moves settled money out of Tender and into a bank account.
//
// Fintava publishes no idempotency key, so the payout call cannot be made safe
// by repeating it. Safety is built here instead, from three things:
//
//   - one payout row per transfer, enforced by a unique constraint, so no code
//     path can create a second one;
//   - an atomic claim, so only one machine and one goroutine can ever have a
//     given payout in flight;
//   - a hard rule that a request which timed out is never retried. It goes to
//     'unknown' and is resolved by asking the provider what happened, because a
//     timeout means the money may already have moved.
//
// The ledger is touched only on a confirmed outcome, never on a hopeful one.
package payout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/fintava"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
	"tender/api/internal/stream"
)

// States a payout can be in. They mirror the payout_state enum.
const (
	StatePending    = "pending"
	StateSubmitting = "submitting"
	StateUnknown    = "unknown"
	StateSent       = "sent"
	StateConfirmed  = "confirmed"
	StateFailed     = "failed"
)

type Service struct {
	Pool   *pgxpool.Pool
	Client *fintava.Client
	Hub    *stream.Hub
}

// Destination is where the money is going.
type Destination struct {
	AccountNumber string
	SortCode      string
	AccountName   string
	BankName      string
}

// Create records the intent to pay, inside the same transaction that settles the
// handover. The money is already in 'payable' by this point; this row is the
// instruction to get it out.
//
// It deliberately does not call the provider. Settlement must not depend on a
// bank rail being reachable at that instant -- the handover happened, the ledger
// is correct, and delivery is a separate concern that can be retried.
func (s *Service) Create(ctx context.Context, q ledger.Querier, transferID uuid.UUID,
	dest Destination, amount money.Kobo, narration string) (uuid.UUID, error) {

	var id uuid.UUID
	err := q.QueryRow(ctx, `
		INSERT INTO payouts (transfer_id, account_number, sort_code, account_name,
		                     bank_name, amount_kobo, narration)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (transfer_id) DO NOTHING
		RETURNING id`,
		transferID, dest.AccountNumber, dest.SortCode, dest.AccountName,
		nullable(dest.BankName), int64(amount), nullable(narration)).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		// The row already existed. That is the constraint doing its job, not an
		// error: something tried to pay the same transfer twice.
		return uuid.Nil, q.QueryRow(ctx,
			`SELECT id FROM payouts WHERE transfer_id = $1`, transferID).Scan(&id)
	}
	return id, err
}

// ---------------------------------------------------------------- submitting

type record struct {
	id, transferID, senderID uuid.UUID
	dest                     Destination
	amount                   money.Kobo
	narration                string
	attempts                 int
}

// claim moves exactly one payout from 'pending' to 'submitting'.
//
// This is a single conditional UPDATE rather than a select-then-update, so two
// machines racing for the same row cannot both win: the second sees no rows
// affected and moves on. It runs outside any surrounding transaction on purpose
// -- the claim must be visible to everyone else before the network call starts.
func (s *Service) claim(ctx context.Context, id uuid.UUID) (*record, error) {
	var r record
	var bankName, narration *string
	err := s.Pool.QueryRow(ctx, `
		UPDATE payouts p
		   SET state = 'submitting', attempts = attempts + 1,
		       submitted_at = now(), updated_at = now()
		  FROM transfers t
		 WHERE p.id = $1 AND p.transfer_id = t.id AND p.state = 'pending'
		RETURNING p.id, p.transfer_id, t.sender_id, p.account_number, p.sort_code,
		          p.account_name, p.bank_name, p.amount_kobo, p.narration, p.attempts`, id).
		Scan(&r.id, &r.transferID, &r.senderID, &r.dest.AccountNumber, &r.dest.SortCode,
			&r.dest.AccountName, &bankName, &r.amount, &narration, &r.attempts)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // somebody else has it, or it is no longer pending
	}
	if err != nil {
		return nil, err
	}
	if bankName != nil {
		r.dest.BankName = *bankName
	}
	if narration != nil {
		r.narration = *narration
	}
	return &r, nil
}

// Submit sends one payout to the bank.
//
// Every exit from here leaves the row in a state that describes reality: 'sent'
// when the provider accepted it, 'failed' when the provider refused and nothing
// moved, and 'unknown' when no answer arrived at all. Only 'failed' returns the
// money, because only 'failed' is known not to have paid anyone.
func (s *Service) Submit(ctx context.Context, id uuid.UUID) error {
	r, err := s.claim(ctx, id)
	if err != nil || r == nil {
		return err
	}

	if !s.Client.CanPayOut() {
		// Put it back rather than burning the attempt: nothing was sent.
		return s.release(ctx, r.id, "bank transfers are not configured")
	}

	res, err := s.Client.BankCredit(ctx, fintava.BankCreditRequest{
		AccountNumber: r.dest.AccountNumber,
		AccountName:   r.dest.AccountName,
		SortCode:      r.dest.SortCode,
		Amount:        fintava.Naira(r.amount),
		SourceID:      s.Client.SourceID(),
		Narration:     r.narration,
	})

	switch {
	case errors.Is(err, fintava.ErrIndeterminate):
		// The dangerous case. Do not retry, do not refund: find out first.
		slog.Error("payout outcome unknown", "payout", r.id, "transfer", r.transferID, "err", err)
		return s.mark(ctx, r.id, StateUnknown, err.Error(), "", "")

	case errors.Is(err, fintava.ErrTemporary):
		// The provider refused for a reason that will stop being true -- an
		// empty float, a rate limit, their side down. No money moved, and none
		// should move back: returning it would cancel a transfer that is going
		// to go out perfectly well as soon as the wallet is topped up. Back to
		// pending, with the reason visible, and the ticker will try again.
		slog.Warn("payout deferred, will retry", "payout", r.id, "err", err)
		return s.mark(ctx, r.id, StatePending, err.Error(), "", "")

	case err != nil:
		// The provider answered and refused on the merits -- a wrong account
		// number, a closed account. Waiting cannot fix that, so the money goes
		// back to the sender.
		slog.Warn("payout rejected", "payout", r.id, "err", err)
		return s.fail(ctx, r, err.Error())

	default:
		if err := s.mark(ctx, r.id, StateSent, "", res.Reference, res.ID); err != nil {
			return err
		}
		// Some providers report the final status on the initial call. Take it
		// when offered; otherwise wait for the webhook or reconciliation.
		if res.Status != "" && (fintava.Transaction{Status: res.Status}).Succeeded() {
			return s.Confirm(ctx, r.id)
		}
		return nil
	}
}

// SubmitPending sends everything waiting. Called after settlement and on a
// ticker, so a payout that could not be sent immediately still goes out.
func (s *Service) SubmitPending(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx,
		`SELECT id FROM payouts WHERE state = 'pending' ORDER BY created_at LIMIT 50`)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		if err := s.Submit(ctx, id); err != nil {
			slog.Error("submit payout", "payout", id, "err", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- outcomes

// Confirm discharges the obligation: the money reached the account.
//
// The ledger write and the state change share one transaction, and the state
// change is conditional on the payout not already being terminal. A webhook
// redelivery, a reconciliation and an inline confirmation can therefore all
// arrive for the same payout and only the first will move the ledger.
func (s *Service) Confirm(ctx context.Context, id uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var transferID, senderID uuid.UUID
	var amount money.Kobo
	err = tx.QueryRow(ctx, `
		UPDATE payouts p SET state = 'confirmed', settled_at = now(), updated_at = now()
		  FROM transfers t
		 WHERE p.id = $1 AND p.transfer_id = t.id
		   AND p.state IN ('submitting','sent','unknown','pending')
		RETURNING p.transfer_id, t.sender_id, p.amount_kobo`, id).
		Scan(&transferID, &senderID, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already terminal; nothing to do
	}
	if err != nil {
		return err
	}

	if err := ledger.PayoutSent(ctx, tx, transferID, amount); err != nil {
		return fmt.Errorf("ledger payout: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.publish(ctx, "payout.confirmed", transferID, senderID, amount, "")
	return nil
}

// fail returns money for a payout the provider refused.
func (s *Service) fail(ctx context.Context, r *record, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE payouts SET state = 'failed', last_error = $2, updated_at = now()
		 WHERE id = $1 AND state IN ('submitting','sent','unknown','pending')`, r.id, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	if err := ledger.PayoutReturned(ctx, tx, r.transferID, r.senderID, r.amount); err != nil {
		return fmt.Errorf("ledger return: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE transfers SET state = 'payout_failed', updated_at = now() WHERE id = $1`,
		r.transferID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.publish(ctx, "payout.failed", r.transferID, r.senderID, r.amount, reason)
	return nil
}

// FailByID looks the payout up first, for callers that only have an id.
func (s *Service) FailByID(ctx context.Context, id uuid.UUID, reason string) error {
	var r record
	err := s.Pool.QueryRow(ctx, `
		SELECT p.id, p.transfer_id, t.sender_id, p.amount_kobo
		  FROM payouts p JOIN transfers t ON t.id = p.transfer_id
		 WHERE p.id = $1`, id).Scan(&r.id, &r.transferID, &r.senderID, &r.amount)
	if err != nil {
		return err
	}
	return s.fail(ctx, &r, reason)
}

// ---------------------------------------------------------------- reconcile

// Reconcile resolves payouts whose outcome is not yet known.
//
// This is the counterweight to having no idempotency key. Anything sitting in
// 'unknown' had a request leave this process with no answer, so the only safe
// move is to ask the provider what became of it. 'sent' rows are chased too, in
// case a webhook was lost.
func (s *Service) Reconcile(ctx context.Context, stale time.Duration) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.transfer_id, t.sender_id, p.amount_kobo, p.state::text,
		       COALESCE(p.provider_tx_id,''), COALESCE(p.provider_ref,'')
		  FROM payouts p JOIN transfers t ON t.id = p.transfer_id
		 WHERE p.state IN ('sent','unknown','submitting')
		   AND p.updated_at < now() - $1::interval
		 ORDER BY p.updated_at LIMIT 50`, stale.String())
	if err != nil {
		return err
	}

	type pending struct {
		r       record
		state   string
		txID    string
		provRef string
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.r.id, &p.r.transferID, &p.r.senderID, &p.r.amount,
			&p.state, &p.txID, &p.provRef); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range batch {
		if p.txID == "" {
			// Nothing to look up. A payout stuck here needs a human: it may have
			// paid somebody, and no automated guess is safe.
			slog.Error("payout needs manual reconciliation",
				"payout", p.r.id, "transfer", p.r.transferID, "state", p.state)
			continue
		}
		tx, err := s.Client.Transaction(ctx, p.txID)
		if err != nil {
			slog.Warn("reconcile lookup failed", "payout", p.r.id, "err", err)
			continue
		}
		switch {
		case tx.Succeeded():
			if err := s.Confirm(ctx, p.r.id); err != nil {
				slog.Error("reconcile confirm", "payout", p.r.id, "err", err)
			}
		case tx.Failed():
			r := p.r
			if err := s.fail(ctx, &r, "provider reported "+tx.Status); err != nil {
				slog.Error("reconcile fail", "payout", p.r.id, "err", err)
			}
		}
	}
	return nil
}

// Run drives submission and reconciliation until the context ends.
func (s *Service) Run(ctx context.Context, every, stale time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.SubmitPending(ctx); err != nil {
				slog.Error("submit pending payouts", "err", err)
			}
			if err := s.Reconcile(ctx, stale); err != nil {
				slog.Error("reconcile payouts", "err", err)
			}
		}
	}
}

// ---------------------------------------------------------------- helpers

func (s *Service) mark(ctx context.Context, id uuid.UUID, state, lastErr, ref, txID string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE payouts
		   SET state = $2::payout_state,
		       last_error = NULLIF($3,''),
		       provider_ref = COALESCE(NULLIF($4,''), provider_ref),
		       provider_tx_id = COALESCE(NULLIF($5,''), provider_tx_id),
		       updated_at = now()
		 WHERE id = $1`, id, state, lastErr, ref, txID)
	return err
}

// release puts a claimed payout back without counting it as attempted output.
func (s *Service) release(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE payouts SET state = 'pending', last_error = $2, updated_at = now()
		 WHERE id = $1 AND state = 'submitting'`, id, reason)
	return err
}

func (s *Service) publish(ctx context.Context, event string, transferID, senderID uuid.UUID,
	amount money.Kobo, reason string) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(stream.Event{Type: event, Data: map[string]any{
		"transferId": transferID,
		"amountKobo": amount,
		"reason":     reason,
	}}, senderID)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
