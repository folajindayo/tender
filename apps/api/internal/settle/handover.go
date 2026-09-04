package settle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tender/api/internal/domain"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
	"tender/api/internal/stream"
)

// ConfirmHandover records one side of the physical exchange.
//
// The counterparty must quote the code shown on the sender's phone, which is
// what ties the digital settlement to a specific face-to-face meeting. Value
// moves only once both sides have confirmed.
func (s *Service) ConfirmHandover(ctx context.Context, transferID, userID uuid.UUID, code string) (*domain.Transfer, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		t                  domain.Transfer
		matchID            uuid.UUID
		counterpartyID     uuid.UUID
		cashoutID          uuid.UUID
		storedCode         string
		matchState         string
		senderConf, cpConf *time.Time
		expiresAt          time.Time

		// Nullable: set only when the recipient is a bank account.
		acctNo, sortCode, acctName, bankName *string
	)
	err = tx.QueryRow(ctx, `
		SELECT t.id, t.ref, t.sender_id, t.recipient_id, t.amount_kobo, t.fee_kobo,
		       t.mode::text, t.state::text,
		       t.recipient_account_number, t.recipient_sort_code,
		       t.recipient_account_name, t.recipient_bank_name,
		       m.id, m.counterparty_id, m.cashout_request_id, m.handover_code, m.state::text,
		       m.sender_confirmed_at, m.counterparty_confirmed_at, m.expires_at
		  FROM transfers t
		  JOIN matches m ON m.transfer_id = t.id
		 WHERE t.id = $1
		   AND m.state IN ('proposed','sender_confirmed','counterparty_confirmed')
		 FOR UPDATE OF t, m`, transferID).
		Scan(&t.ID, &t.Ref, &t.SenderID, &t.RecipientID, &t.Amount, &t.Fee, &t.Mode, &t.State,
			&acctNo, &sortCode, &acctName, &bankName,
			&matchID, &counterpartyID, &cashoutID, &storedCode, &matchState,
			&senderConf, &cpConf, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, reject("no_live_match", "There is no handover waiting on this transfer.")
	}
	if err != nil {
		return nil, err
	}
	if acctNo != nil && sortCode != nil && acctName != nil {
		t.Bank = &domain.BankAccount{
			AccountNumber: *acctNo, SortCode: *sortCode, AccountName: *acctName,
		}
		if bankName != nil {
			t.Bank.BankName = *bankName
		}
	}

	if time.Now().After(expiresAt) {
		return nil, reject("expired", "This handover window has closed. Ask for a new match.")
	}

	now := time.Now()
	switch userID {
	case counterpartyID:
		if code != storedCode {
			return nil, reject("bad_code",
				"That code does not match. Read the six digits from the sender's phone.")
		}
		cpConf = &now
	case t.SenderID:
		// The sender owns the code, so confirming only asserts they handed over.
		senderConf = &now
	default:
		return nil, reject("not_a_party", "You are not part of this handover.")
	}

	both := senderConf != nil && cpConf != nil
	newMatchState := domain.MatchProposed
	switch {
	case both:
		newMatchState = domain.MatchCompleted
	case cpConf != nil:
		newMatchState = domain.MatchCounterpartyConfirmed
	case senderConf != nil:
		newMatchState = domain.MatchSenderConfirmed
	}

	if _, err := tx.Exec(ctx, `
		UPDATE matches SET state=$2::match_state, sender_confirmed_at=$3, counterparty_confirmed_at=$4
		 WHERE id=$1`, matchID, newMatchState, senderConf, cpConf); err != nil {
		return nil, err
	}

	if !both {
		// One side is waiting on the other; nothing moves yet.
		if _, err := tx.Exec(ctx,
			`UPDATE transfers SET state='handover_pending', updated_at=now() WHERE id=$1`, t.ID); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		full, _ := s.Store.GetTransfer(ctx, t.ID)
		s.publish(ctx, "transfer.handover_pending", full, counterpartyID)
		return full, nil
	}

	// ---- both confirmed: the cash has physically changed hands ----------
	var payoutID uuid.UUID
	if t.Mode == domain.ModeCredit {
		// The recipient was paid at snap time; the counterparty's escrow now
		// restores float and clears the sender's obligation.
		if err := ledger.RepayCreditFromEscrow(ctx, tx, t.ID, counterpartyID, t.SenderID, t.Amount); err != nil {
			return nil, fmt.Errorf("repay credit: %w", err)
		}
	} else {
		if t.Bank != nil {
			// The handover happened, so the ledger settles now. Delivery to the
			// bank is a separate step: it must not be able to fail settlement.
			if err := ledger.SettleToPayable(ctx, tx, t.ID, counterpartyID, t.Amount, t.Fee); err != nil {
				return nil, fmt.Errorf("settle to payable: %w", err)
			}
			if payoutID, err = s.createPayout(ctx, tx, t.ID, t.Bank, t.Amount-t.Fee); err != nil {
				return nil, err
			}
		} else if err := ledger.SettleEscrow(ctx, tx, t.ID, counterpartyID, *t.RecipientID, t.Amount, t.Fee); err != nil {
			return nil, fmt.Errorf("settle escrow: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE transfers SET state='settled', settled_at=now(), updated_at=now() WHERE id=$1`, t.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cashout_requests SET state='fulfilled' WHERE id=$1`, cashoutID); err != nil {
		return nil, err
	}
	// The notes are spent: free them so the sender can pledge cash again later.
	if _, err := tx.Exec(ctx, `
		UPDATE pledged_notes SET status='released'
		 WHERE pledge_id IN (SELECT id FROM pledges WHERE transfer_id=$1)`, t.ID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE users SET settled_count = settled_count + 1,
		                 trust_score = LEAST(100, trust_score + 5)
		 WHERE id = $1`, t.SenderID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET trust_score = LEAST(100, trust_score + 2) WHERE id = $1`, counterpartyID); err != nil {
		return nil, err
	}

	// Earning the credit line: history, not the photograph, is what unlocks it.
	var creditUnlocked bool
	err = tx.QueryRow(ctx, `
		UPDATE users
		   SET credit_limit_kobo = $2
		 WHERE id = $1
		   AND credit_limit_kobo = 0
		   AND settled_count >= $3
		   AND defaulted_count = 0
		   AND NOT sending_frozen
		RETURNING true`, t.SenderID, s.Cfg.CreditLimitKobo, s.Cfg.SettlementsForCredit).Scan(&creditUnlocked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Settled in the ledger; now actually deliver it.
	s.sendPayout(ctx, payoutID)

	full, err := s.Store.GetTransfer(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, "transfer.settled", full, counterpartyID)

	if creditUnlocked {
		s.Hub.Publish(stream.Event{Type: "credit.unlocked", Data: map[string]any{
			"userId":          t.SenderID,
			"creditLimitKobo": s.Cfg.CreditLimitKobo,
			"message": fmt.Sprintf("Instant credit unlocked: up to %s sends immediately.",
				money.Kobo(s.Cfg.CreditLimitKobo)),
		}}, t.SenderID)
	}
	return full, nil
}

// RejectHandover is the counterparty refusing the notes in person -- wrong
// count, or they do not believe the notes are genuine. This is the control that
// actually establishes authenticity, because the refuser is the one who would
// have eaten the loss.
func (s *Service) RejectHandover(ctx context.Context, transferID, userID uuid.UUID, reason string) (*domain.Transfer, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		matchID, counterpartyID, cashoutID uuid.UUID
		amount                             money.Kobo
		mode                               string
	)
	err = tx.QueryRow(ctx, `
		SELECT m.id, m.counterparty_id, m.cashout_request_id, m.amount_kobo, t.mode::text
		  FROM matches m JOIN transfers t ON t.id = m.transfer_id
		 WHERE m.transfer_id = $1
		   AND m.state IN ('proposed','sender_confirmed','counterparty_confirmed')
		 FOR UPDATE OF m, t`, transferID).
		Scan(&matchID, &counterpartyID, &cashoutID, &amount, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, reject("no_live_match", "There is no handover waiting on this transfer.")
	}
	if err != nil {
		return nil, err
	}
	if userID != counterpartyID {
		return nil, reject("not_counterparty", "Only the person receiving the cash can refuse it.")
	}

	if err := s.unwindMatch(ctx, tx, transferID, matchID, counterpartyID, cashoutID,
		amount, mode, domain.MatchRejected, "escrow.release_rejected", reason); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	full, _ := s.Store.GetTransfer(ctx, transferID)
	s.publish(ctx, "transfer.rejected", full, counterpartyID)
	return full, nil
}

// unwindMatch releases a counterparty's escrow and returns the transfer to the
// pool so it can be matched again. Nobody is out of pocket at any point.
func (s *Service) unwindMatch(ctx context.Context, tx pgx.Tx,
	transferID, matchID, counterpartyID, cashoutID uuid.UUID,
	amount money.Kobo, mode, matchState, ledgerReason, reason string) error {

	if err := ledger.ReleaseEscrow(ctx, tx, transferID, counterpartyID, amount, ledgerReason); err != nil {
		return fmt.Errorf("release escrow: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE matches SET state=$2::match_state, rejection_reason=$3 WHERE id=$1`,
		matchID, matchState, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cashout_requests SET state='open' WHERE id=$1`, cashoutID); err != nil {
		return err
	}

	// Back into the pool: a Tier 1 transfer keeps its obligation, a Tier 0 one
	// simply waits for another counterparty.
	back := domain.StatePledged
	if mode == domain.ModeCredit {
		back = domain.StateCredited
	}
	_, err := tx.Exec(ctx,
		`UPDATE transfers SET state=$2::transfer_state, updated_at=now() WHERE id=$1`, transferID, back)
	return err
}
