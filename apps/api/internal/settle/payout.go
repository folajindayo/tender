package settle

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"tender/api/internal/domain"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
	"tender/api/internal/payout"
)

// createPayout records the instruction to deliver a settled transfer to a bank
// account. It runs inside the settling transaction, so a transfer can never be
// marked settled without the corresponding payout existing.
//
// It does not talk to the bank. Settlement is about the handover that already
// happened, and it must not become conditional on a payment rail being
// reachable at that moment.
func (s *Service) createPayout(ctx context.Context, q ledger.Querier, transferID uuid.UUID,
	bank *domain.BankAccount, amount money.Kobo) (uuid.UUID, error) {

	if s.Payouts == nil {
		return uuid.Nil, fmt.Errorf("bank payouts are not configured")
	}
	id, err := s.Payouts.Create(ctx, q, transferID, payout.Destination{
		AccountNumber: bank.AccountNumber,
		SortCode:      bank.SortCode,
		AccountName:   bank.AccountName,
		BankName:      bank.BankName,
	}, amount, "Tender transfer")
	if err != nil {
		return uuid.Nil, fmt.Errorf("record payout: %w", err)
	}
	return id, nil
}

// sendPayout pushes a payout to the bank once its transaction has committed.
//
// The context is detached from the request on purpose: the sender's HTTP call
// is over, but the money still has to move, and cancelling mid-flight is the
// one thing that turns a clean outcome into an indeterminate one.
func (s *Service) sendPayout(ctx context.Context, id uuid.UUID) {
	if s.Payouts == nil || id == uuid.Nil {
		return
	}
	go func() {
		if err := s.Payouts.Submit(context.WithoutCancel(ctx), id); err != nil {
			slog.Error("submit payout", "payout", id, "err", err)
		}
	}()
}
