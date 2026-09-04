package settle

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tender/api/internal/domain"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
	"tender/api/internal/stream"
)

// RunSweeper expires stale matches and transfers on a ticker until ctx ends.
func (s *Service) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				slog.Error("sweep failed", "err", err)
			}
		}
	}
}

// The states a sweep is allowed to act on. The batch query and the claim below
// both read from these, so a state can never be swept by one and not the other.
var (
	sweepableMatchStates = []string{
		domain.MatchProposed, domain.MatchSenderConfirmed, domain.MatchCounterpartyConfirmed}
	sweepableTransferStates = []string{
		domain.StatePledged, domain.StateCredited, domain.StateMatched, domain.StateHandoverPending}
)

// claimMatch and claimTransfer re-read a row inside the sweeping transaction and
// lock it, reporting false when it no longer needs sweeping.
//
// The sweeper picks its batch outside any transaction, so between that read and
// the write someone else may have moved the row on: a counterparty confirming a
// late handover, or -- now that Fly runs more than one machine -- a second
// sweeper working through the identical batch. Without this guard both would
// release the same escrow. Nothing downstream would notice, because each release
// balances on its own, so the ledger invariant stays satisfied while the
// counterparty is quietly paid twice. Every user-facing path already takes this
// lock; the sweeper was the one that did not.
func claimMatch(ctx context.Context, tx pgx.Tx, id uuid.UUID) (bool, error) {
	return claimed(tx.QueryRow(ctx, `
		SELECT 1 FROM matches
		 WHERE id = $1 AND state::text = ANY($2) FOR UPDATE`, id, sweepableMatchStates))
}

func claimTransfer(ctx context.Context, tx pgx.Tx, id uuid.UUID) (bool, error) {
	return claimed(tx.QueryRow(ctx, `
		SELECT 1 FROM transfers
		 WHERE id = $1 AND state::text = ANY($2) FOR UPDATE`, id, sweepableTransferStates))
}

func claimed(row pgx.Row) (bool, error) {
	var one int
	switch err := row.Scan(&one); {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// Sweep is the safety net that makes "nobody turned up" a non-event.
func (s *Service) Sweep(ctx context.Context) error {
	if err := s.expireMatches(ctx); err != nil {
		return err
	}
	return s.closeStaleTransfers(ctx)
}

// expireMatches releases escrow for meet-ups that never happened. The
// counterparty gets their money back untouched and the transfer returns to the
// pool for another match.
func (s *Service) expireMatches(ctx context.Context) error {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT m.id, m.transfer_id, m.counterparty_id, m.cashout_request_id,
		       m.amount_kobo, t.mode::text
		  FROM matches m JOIN transfers t ON t.id = m.transfer_id
		 WHERE m.expires_at < now()
		   AND m.state::text = ANY($1)`, sweepableMatchStates)
	if err != nil {
		return err
	}

	type stale struct {
		matchID, transferID, counterpartyID, cashoutID uuid.UUID
		amount                                         money.Kobo
		mode                                           string
	}
	var batch []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.matchID, &st.transferID, &st.counterpartyID,
			&st.cashoutID, &st.amount, &st.mode); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, st := range batch {
		tx, err := s.Store.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		switch ok, err := claimMatch(ctx, tx, st.matchID); {
		case err != nil:
			tx.Rollback(ctx)
			slog.Error("claim match", "match", st.matchID, "err", err)
			continue
		case !ok:
			// Already handled -- by a handover that landed late, or by the
			// other machine's sweeper.
			tx.Rollback(ctx)
			continue
		}

		err = s.unwindMatch(ctx, tx, st.transferID, st.matchID, st.counterpartyID,
			st.cashoutID, st.amount, st.mode, domain.MatchExpired,
			"escrow.release_expired", "nobody arrived within the handover window")
		if err != nil {
			tx.Rollback(ctx)
			slog.Error("expire match", "match", st.matchID, "err", err)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Error("commit expired match", "match", st.matchID, "err", err)
			continue
		}
		full, _ := s.Store.GetTransfer(ctx, st.transferID)
		s.publish(ctx, "match.expired", full, st.counterpartyID)
	}
	return nil
}

// closeStaleTransfers ends transfers that ran out of time.
//
// Tier 0 simply expires: no value ever moved, so nobody lost anything. Tier 1
// defaults, because the recipient was already paid out of float -- that debt is
// recovered from the sender's balance and whatever remains is written off.
func (s *Service) closeStaleTransfers(ctx context.Context) error {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id, sender_id, amount_kobo, mode::text
		  FROM transfers
		 WHERE expires_at < now()
		   AND state::text = ANY($1)
		   AND NOT EXISTS (
		       SELECT 1 FROM matches m WHERE m.transfer_id = transfers.id
		        AND m.state::text = ANY($2))`, sweepableTransferStates, sweepableMatchStates)
	if err != nil {
		return err
	}

	type stale struct {
		id, senderID uuid.UUID
		amount       money.Kobo
		mode         string
	}
	var batch []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.id, &st.senderID, &st.amount, &st.mode); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, st := range batch {
		if st.mode == domain.ModeCredit {
			if err := s.defaultTransfer(ctx, st.id, st.senderID, st.amount); err != nil {
				slog.Error("default transfer", "transfer", st.id, "err", err)
			}
			continue
		}
		if err := s.expireTransfer(ctx, st.id); err != nil {
			slog.Error("expire transfer", "transfer", st.id, "err", err)
		}
	}
	return nil
}

// expireTransfer closes a Tier 0 transfer that never found a counterparty.
// No ledger entries are written because no value ever moved.
func (s *Service) expireTransfer(ctx context.Context, transferID uuid.UUID) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	switch ok, err := claimTransfer(ctx, tx, transferID); {
	case err != nil:
		return err
	case !ok:
		return nil
	}

	if _, err := tx.Exec(ctx,
		`UPDATE transfers SET state='expired', updated_at=now() WHERE id=$1`, transferID); err != nil {
		return err
	}
	// Free the notes so the sender can pledge the same cash again.
	if _, err := tx.Exec(ctx, `
		UPDATE pledged_notes SET status='released'
		 WHERE pledge_id IN (SELECT id FROM pledges WHERE transfer_id=$1)`, transferID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	full, _ := s.Store.GetTransfer(ctx, transferID)
	s.publish(ctx, "transfer.expired", full)
	return nil
}

// defaultTransfer handles the one case where the platform is actually exposed.
func (s *Service) defaultTransfer(ctx context.Context, transferID, senderID uuid.UUID, amount money.Kobo) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Claim before touching the ledger: defaulting twice would recover the debt
	// twice and dock the sender's trust twice for one missed settlement.
	switch ok, err := claimTransfer(ctx, tx, transferID); {
	case err != nil:
		return err
	case !ok:
		return nil
	}

	// Recover from whatever the sender holds on the platform first.
	avail, err := ledger.Balance(ctx, tx, senderID, ledger.KindAvailable)
	if err != nil {
		return err
	}
	recovered := money.Kobo(0)
	if avail > 0 {
		recovered = min(avail, amount)
		if err := ledger.RecoverFromBalance(ctx, tx, transferID, senderID, recovered); err != nil {
			return err
		}
	}
	if remaining := amount - recovered; remaining > 0 {
		if err := ledger.WriteOff(ctx, tx, transferID, senderID, remaining); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE transfers SET state='defaulted', updated_at=now() WHERE id=$1`, transferID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE pledged_notes SET status='released'
		 WHERE pledge_id IN (SELECT id FROM pledges WHERE transfer_id=$1)`, transferID); err != nil {
		return err
	}
	// The credit line is withdrawn immediately and sending is frozen.
	if _, err := tx.Exec(ctx, `
		UPDATE users
		   SET defaulted_count = defaulted_count + 1,
		       credit_limit_kobo = 0,
		       sending_frozen = true,
		       trust_score = GREATEST(0, trust_score - 40)
		 WHERE id = $1`, senderID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	full, _ := s.Store.GetTransfer(ctx, transferID)
	s.publish(ctx, "transfer.defaulted", full)
	s.Hub.Publish(stream.Event{Type: "credit.revoked", Data: map[string]any{
		"userId":         senderID,
		"recoveredKobo":  recovered,
		"writtenOffKobo": amount - recovered,
		"message":        "Instant credit withdrawn and sending frozen after an unsettled obligation.",
	}}, senderID)
	return nil
}
