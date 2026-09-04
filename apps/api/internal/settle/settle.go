// Package settle orchestrates the life of a transfer: accepting a cash pledge,
// finding a counterparty who wants the physical notes, and closing the books
// when the handover happens.
//
// The guiding rule is that value only moves after cash has physically moved.
// The one exception is Tier 1 credit, which is earned, capped, and explicit.
package settle

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"tender/api/internal/config"
	"tender/api/internal/domain"
	"tender/api/internal/ledger"
	"tender/api/internal/money"
	"tender/api/internal/payout"
	"tender/api/internal/store"
	"tender/api/internal/stream"
	"tender/api/internal/vision"
)

type Service struct {
	Store   *store.Store
	Vision  vision.Provider
	Hub     *stream.Hub
	Payouts *payout.Service
	Cfg     config.Config
}

// Rejection is a refusal the user should see, as opposed to an internal fault.
type Rejection struct {
	Code   string
	Reason string
}

func (r *Rejection) Error() string { return r.Reason }

func reject(code, format string, args ...any) *Rejection {
	return &Rejection{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// PledgeRequest is a sender offering physical cash for delivery to someone else.
type PledgeRequest struct {
	SenderID uuid.UUID

	// Exactly one destination. RecipientID sends to another Tender account;
	// Bank sends to an account number the sender typed and confirmed by name.
	RecipientID *uuid.UUID
	Bank        *domain.BankAccount

	Amount money.Kobo
	Note   string
	Image  []byte
}

// destination validates that a pledge is going exactly one place.
func (r PledgeRequest) destination() *Rejection {
	switch {
	case r.RecipientID == nil && r.Bank == nil:
		return reject("no_recipient", "Choose who this money is going to.")
	case r.RecipientID != nil && r.Bank != nil:
		return reject("two_recipients", "A transfer can only have one destination.")
	case r.RecipientID != nil && *r.RecipientID == r.SenderID:
		return reject("self_send", "You cannot send cash to yourself.")
	case r.Bank != nil && (r.Bank.AccountNumber == "" || r.Bank.SortCode == ""):
		return reject("bad_account", "Enter an account number and choose a bank.")
	case r.Bank != nil && r.Bank.AccountName == "":
		// The name is resolved by the bank, not typed. Without it there is
		// nothing for the sender to have checked.
		return reject("unverified_account",
			"Confirm the account name before sending.")
	}
	return nil
}

// PledgeResult reports what happened, including a refusal.
type PledgeResult struct {
	Accepted bool             `json:"accepted"`
	Reason   string           `json:"reason,omitempty"`
	Code     string           `json:"code,omitempty"`
	Vision   *vision.Result   `json:"vision,omitempty"`
	Transfer *domain.Transfer `json:"transfer,omitempty"`
}

// Pledge photographs cash into a transfer.
//
// Nothing here treats the photograph as collateral. It establishes what is being
// offered, screens out obvious fraud, and reserves the specific notes so they
// cannot be pledged twice while this transfer is open.
func (s *Service) Pledge(ctx context.Context, req PledgeRequest) (*PledgeResult, error) {
	if req.Amount <= 0 {
		return nil, reject("bad_amount", "Enter an amount greater than zero.")
	}
	if out := req.destination(); out != nil {
		return nil, out
	}
	// A large handover is worth targeting. Refuse rather than concentrate a big
	// amount into a single meeting; several smaller transfers are safer.
	if max := money.Kobo(s.Cfg.MaxHandoverKobo); req.Amount > max {
		return nil, reject("amount_too_large",
			"The most that can change hands in one meeting is %s. Send it as several smaller transfers.", max)
	}

	sender, err := s.Store.GetUser(ctx, req.SenderID)
	if err != nil {
		return nil, fmt.Errorf("load sender: %w", err)
	}
	if sender.SendingFrozen {
		return nil, reject("frozen",
			"Sending is frozen on this account until an outstanding obligation is cleared.")
	}
	if sender.Suspended {
		return nil, reject("suspended",
			"This account is suspended while a reported incident is reviewed.")
	}

	// ---- recognition -------------------------------------------------
	vres, err := s.Vision.Analyze(ctx, req.Image, req.Amount)
	if err != nil {
		return nil, fmt.Errorf("recognise notes: %w", err)
	}

	if out := screen(vres, req.Amount); out != nil {
		s.recordRefusedPledge(ctx, req, vres, out)
		return &PledgeResult{Accepted: false, Reason: out.Reason, Code: out.Code, Vision: vres}, nil
	}

	// ---- reserve the notes and open the transfer ----------------------
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if ref, dup := s.notesAlreadyPledged(ctx, vres); dup {
		return &PledgeResult{
			Accepted: false, Code: "already_pledged", Vision: vres,
			Reason: fmt.Sprintf("These notes are already pledged in transfer #%d. "+
				"Settle that transfer before pledging the same cash again.", ref),
		}, nil
	}

	fee := money.FeeFor(req.Amount, s.Cfg.FeeBPS)
	mode, expiry := domain.ModeEscrow, time.Now().Add(s.Cfg.MatchTTL)
	if s.creditEligible(ctx, tx, sender, req.Amount) {
		mode, expiry = domain.ModeCredit, time.Now().Add(s.Cfg.CreditTTL)
	}

	var t domain.Transfer
	acctNo, sortCode, acctName, bankName := bankColumns(req.Bank)
	err = tx.QueryRow(ctx, `
		INSERT INTO transfers (sender_id, recipient_id, amount_kobo, fee_kobo, mode, state, note, expires_at,
		                       recipient_account_number, recipient_sort_code,
		                       recipient_account_name, recipient_bank_name)
		VALUES ($1,$2,$3,$4,$5::transfer_mode,'draft',$6,$7,$8,$9,$10,$11)
		RETURNING id, ref`,
		req.SenderID, req.RecipientID, int64(req.Amount), int64(fee), mode, req.Note, expiry,
		acctNo, sortCode, acctName, bankName).
		Scan(&t.ID, &t.Ref)
	if err != nil {
		return nil, fmt.Errorf("create transfer: %w", err)
	}

	var pledgeID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO pledges (transfer_id, image_sha256, declared_kobo, detected_kobo,
		                     confidence, screen_replay, photocopy_suspected, accepted,
		                     vision_mode, vision_raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,$9) RETURNING id`,
		t.ID, vres.ImageSHA256, int64(req.Amount), int64(vres.Total), vres.Confidence,
		vres.ScreenReplay, vres.PhotocopySuspected, vres.Mode, jsonb(vres)).Scan(&pledgeID)
	if err != nil {
		if isUnique(err, "pledges_image_active") {
			return &PledgeResult{Accepted: false, Code: "duplicate_photo", Vision: vres,
				Reason: "This exact photograph has already been used for a pledge."}, nil
		}
		return nil, fmt.Errorf("record pledge: %w", err)
	}

	for _, n := range vres.Notes {
		var serial *string
		if n.Serial != "" {
			v := n.Serial
			serial = &v
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO pledged_notes (pledge_id, denomination_kobo, serial, serial_confidence, note_phash)
			VALUES ($1,$2,$3,$4,$5)`,
			pledgeID, int64(n.Denomination), serial, n.SerialConfidence, n.PHash)
		if err != nil {
			if isUnique(err, "pledged_notes_serial_active") || isUnique(err, "pledged_notes_phash_active") {
				return &PledgeResult{Accepted: false, Code: "already_pledged", Vision: vres,
					Reason: "One or more of these notes is already pledged in another open transfer."}, nil
			}
			return nil, fmt.Errorf("register note: %w", err)
		}
	}

	// ---- Tier 1: front the recipient now, collect from the sender later
	state := domain.StatePledged
	var payoutID uuid.UUID
	if mode == domain.ModeCredit {
		if req.Bank != nil {
			if err := ledger.ExtendCreditToPayable(ctx, tx, t.ID, req.SenderID, req.Amount, fee); err != nil {
				return nil, fmt.Errorf("extend credit: %w", err)
			}
			payoutID, err = s.createPayout(ctx, tx, t.ID, req.Bank, req.Amount-fee)
			if err != nil {
				return nil, err
			}
		} else if err := ledger.ExtendCredit(ctx, tx, t.ID, req.SenderID, *req.RecipientID, req.Amount, fee); err != nil {
			return nil, fmt.Errorf("extend credit: %w", err)
		}
		state = domain.StateCredited
	}

	if _, err := tx.Exec(ctx,
		`UPDATE transfers SET state=$2::transfer_state, updated_at=now() WHERE id=$1`,
		t.ID, state); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit pledge: %w", err)
	}

	// Tier 1 pays the recipient at snap time, so a bank payout goes out now.
	s.sendPayout(ctx, payoutID)

	full, err := s.Store.GetTransfer(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, "transfer.created", full)

	// Look for a counterparty straight away; the recipient is watching a clock.
	// A failure here is not fatal -- the transfer stays open and the sweeper or
	// a manual retry will pick it up -- but it must never pass silently.
	m, matchErr := s.TryMatch(ctx, t.ID)
	if matchErr != nil {
		slog.Error("matching failed after pledge", "transfer", t.ID, "err", matchErr)
	} else if m != nil {
		full, _ = s.Store.GetTransfer(ctx, t.ID)
	}

	return &PledgeResult{Accepted: true, Vision: vres, Transfer: full}, nil
}

// screen applies the checks that should stop a pledge before anyone travels.
func screen(v *vision.Result, declared money.Kobo) *Rejection {
	switch {
	case v.ScreenReplay:
		return reject("screen_replay",
			"That looks like a photograph of a screen, not physical cash. Photograph the notes themselves.")
	case v.PhotocopySuspected:
		return reject("photocopy",
			"These notes do not look genuine. A counterparty would refuse them at handover.")
	case len(v.Notes) == 0:
		return reject("no_notes",
			"No naira notes were visible. Lay the notes flat, spread them out, and try again.")
	case v.Total != declared:
		return reject("amount_mismatch",
			"You said %s but %s is visible. Recount and photograph the notes again.",
			declared, v.Total)
	case v.Confidence < 0.5:
		return reject("low_confidence",
			"The photograph is too unclear to count reliably. Try again in better light.")
	}
	return nil
}

// recordRefusedPledge keeps refused attempts for the fraud record, without
// reserving any notes.
// bankColumns flattens an optional bank destination into the four nullable
// columns the transfers table carries, so a nil destination writes NULLs rather
// than empty strings -- the one_destination constraint distinguishes them.
func bankColumns(b *domain.BankAccount) (acctNo, sortCode, acctName, bankName *string) {
	if b == nil {
		return nil, nil, nil, nil
	}
	name := b.BankName
	var bankPtr *string
	if name != "" {
		bankPtr = &name
	}
	return &b.AccountNumber, &b.SortCode, &b.AccountName, bankPtr
}

func (s *Service) recordRefusedPledge(ctx context.Context, req PledgeRequest, v *vision.Result, r *Rejection) {
	acctNo, sortCode, acctName, bankName := bankColumns(req.Bank)
	_, _ = s.Store.Pool.Exec(ctx, `
		INSERT INTO transfers (sender_id, recipient_id, amount_kobo, fee_kobo, state, note,
		                       recipient_account_number, recipient_sort_code,
		                       recipient_account_name, recipient_bank_name)
		VALUES ($1,$2,$3,0,'voided',$4,$5,$6,$7,$8)`,
		req.SenderID, req.RecipientID, int64(req.Amount), "refused: "+r.Code,
		acctNo, sortCode, acctName, bankName)
}

// notesAlreadyPledged reports the transfer holding any of these notes, so the
// refusal can name it. The unique indexes remain the actual guarantee.
func (s *Service) notesAlreadyPledged(ctx context.Context, v *vision.Result) (int64, bool) {
	serials := make([]string, 0, len(v.Notes))
	phashes := make([]string, 0, len(v.Notes))
	for _, n := range v.Notes {
		if n.Serial != "" {
			serials = append(serials, n.Serial)
		}
		phashes = append(phashes, n.PHash)
	}

	var ref int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT t.ref
		  FROM pledged_notes pn
		  JOIN pledges p  ON p.id = pn.pledge_id
		  JOIN transfers t ON t.id = p.transfer_id
		 WHERE pn.status = 'active'
		   AND (pn.serial = ANY($1) OR pn.note_phash = ANY($2))
		 LIMIT 1`, serials, phashes).Scan(&ref)
	if err != nil {
		return 0, false
	}
	return ref, true
}

// creditEligible decides whether the platform will front this transfer. The
// photograph plays no part -- only settlement history, the standing limit, and
// whether there is float to lend.
func (s *Service) creditEligible(ctx context.Context, q ledger.Querier, u *domain.User, amount money.Kobo) bool {
	if u.SendingFrozen || u.CreditLimit <= 0 || amount > u.CreditLimit {
		return false
	}
	if u.SettledCount < s.Cfg.SettlementsForCredit {
		return false
	}
	if u.Owed > 0 { // one outstanding obligation at a time
		return false
	}

	var float int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(balance_kobo,0) FROM account_balances WHERE kind='float'`).Scan(&float); err != nil {
		return false
	}
	return money.Kobo(float) >= amount
}

// ---------------------------------------------------------------- matching

// TryMatch pairs an open transfer with somebody who wants the physical cash,
// and locks their digital funds. Returns nil when nobody suitable is waiting.
func (s *Service) TryMatch(ctx context.Context, transferID uuid.UUID) (*domain.Match, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		t         domain.Transfer
		senderLat float64
		senderLng float64
	)
	err = tx.QueryRow(ctx, `
		SELECT t.id, t.ref, t.sender_id, t.recipient_id, t.amount_kobo, t.fee_kobo,
		       t.mode::text, t.state::text, COALESCE(u.lat,0), COALESCE(u.lng,0)
		  FROM transfers t JOIN users u ON u.id = t.sender_id
		 WHERE t.id = $1 FOR UPDATE OF t`, transferID).
		Scan(&t.ID, &t.Ref, &t.SenderID, &t.RecipientID, &t.Amount, &t.Fee,
			&t.Mode, &t.State, &senderLat, &senderLng)
	if err != nil {
		return nil, err
	}

	if t.State != domain.StatePledged && t.State != domain.StateCredited {
		return nil, nil // already matched, settled, or closed
	}

	// Candidates: an open request for this amount, from an operator who actually
	// holds the digital funds to give, at a verified venue that is open right
	// now. Handovers only ever happen at registered premises with somebody
	// accountable for them -- never at a location a counterparty nominated for
	// a single transaction.
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.user_id, u.display_name, v.lat, v.lng, v.name, v.address,
		       u.trust_score, COALESCE(ab.balance_kobo,0)
		  FROM cashout_requests c
		  JOIN users u  ON u.id = c.user_id
		  JOIN venues v ON v.id = c.venue_id
		  LEFT JOIN account_balances ab ON ab.user_id = u.id AND ab.kind = 'available'
		 WHERE c.state = 'open'
		   AND c.user_id <> $1 AND c.user_id <> $2
		   AND $3 BETWEEN c.amount_kobo - c.tolerance_kobo AND c.amount_kobo + c.tolerance_kobo
		   AND COALESCE(ab.balance_kobo,0) >= $3
		   AND v.active AND v.verified
		   AND NOT u.suspended
		   AND ($4 = false
		        OR (now() AT TIME ZONE 'Africa/Lagos')::time BETWEEN v.opens_at AND v.closes_at)
		 ORDER BY u.trust_score DESC`,
		t.SenderID, t.RecipientID, int64(t.Amount), s.Cfg.EnforceVenueHours)
	if err != nil {
		return nil, err
	}

	type candidate struct {
		reqID, userID uuid.UUID
		name          string
		venue         string
		address       string
		distance      int
	}
	var best *candidate
	for rows.Next() {
		var c candidate
		var lat, lng float64
		var trust int
		var bal int64
		if err := rows.Scan(&c.reqID, &c.userID, &c.name, &lat, &lng,
			&c.venue, &c.address, &trust, &bal); err != nil {
			rows.Close()
			return nil, err
		}
		c.distance = haversineMetres(senderLat, senderLng, lat, lng)
		if best == nil || c.distance < best.distance {
			cc := c
			best = &cc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if best == nil {
		return nil, nil
	}

	// Lock the counterparty's funds. Until this succeeds nothing is promised.
	if err := ledger.LockEscrow(ctx, tx, t.ID, best.userID, t.Amount); err != nil {
		return nil, fmt.Errorf("lock counterparty escrow: %w", err)
	}

	code, err := handoverCode()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(s.Cfg.MatchTTL)

	var m domain.Match
	err = tx.QueryRow(ctx, `
		INSERT INTO matches (transfer_id, cashout_request_id, counterparty_id, amount_kobo,
		                     handover_code, distance_m, state, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,'proposed',$7) RETURNING id`,
		t.ID, best.reqID, best.userID, int64(t.Amount), code, best.distance, expires).Scan(&m.ID)
	if err != nil {
		return nil, fmt.Errorf("create match: %w", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE cashout_requests SET state='matched' WHERE id=$1`, best.reqID); err != nil {
		return nil, err
	}
	// The transfer keeps the overall deadline it was given at pledge time; the
	// match carries its own, shorter, meet-up window.
	if _, err := tx.Exec(ctx,
		`UPDATE transfers SET state='matched', updated_at=now() WHERE id=$1`, t.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	full, err := s.Store.GetTransfer(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, "transfer.matched", full, best.userID)
	return full.Match, nil
}

// ---------------------------------------------------------------- helpers

func handoverCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// haversineMetres is great-circle distance, good enough for "how far is the
// counterparty" inside one city.
func haversineMetres(lat1, lng1, lat2, lng2 float64) int {
	const earthRadiusM = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }

	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return int(earthRadiusM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a)))
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(constraint == "" || pgErr.ConstraintName == constraint)
}

func jsonb(v any) []byte {
	b, err := marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// publish notifies everyone with a stake in a transfer.
func (s *Service) publish(_ context.Context, kind string, t *domain.Transfer, extra ...uuid.UUID) {
	if t == nil {
		return
	}
	// A bank recipient has no Tender account to notify, so the list is built
	// from whoever actually has a subscription.
	ids := append([]uuid.UUID{t.SenderID}, extra...)
	if t.RecipientID != nil {
		ids = append(ids, *t.RecipientID)
	}
	if t.Match != nil {
		ids = append(ids, t.Match.CounterpartyID)
	}
	s.Hub.Publish(stream.Event{Type: kind, Data: t}, ids...)
}

var _ = pgx.ErrNoRows
