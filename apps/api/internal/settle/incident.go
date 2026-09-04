package settle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tender/api/internal/domain"
	"tender/api/internal/stream"
)

// ReportIncident records that a handover went wrong, and holds the money.
//
// Escrow alone cannot tell theft apart from an innocent no-show: in both cases
// the counterparty simply never confirms. Left to the sweeper, both end the same
// way -- the escrow is returned and the counterparty keeps whatever they were
// given. That made taking the cash and walking away completely free.
//
// A report breaks that symmetry. The match and transfer move to `disputed`,
// which the expiry sweeper deliberately does not touch, so the funds stay locked
// until a person looks at it.
func (s *Service) ReportIncident(
	ctx context.Context, transferID, reporterID uuid.UUID, kind, detail string,
) (*domain.Incident, error) {
	switch kind {
	case domain.IncidentCashTaken, domain.IncidentWrongAmount,
		domain.IncidentThreatened, domain.IncidentNoShow:
	default:
		return nil, reject("bad_kind", "That is not something we can act on.")
	}

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		senderID, recipientID uuid.UUID
		transferState         string
		matchID               *uuid.UUID
		counterpartyID        *uuid.UUID
		matchState            *string
	)
	err = tx.QueryRow(ctx, `
		SELECT t.sender_id, t.recipient_id, t.state::text,
		       m.id, m.counterparty_id, m.state::text
		  FROM transfers t
		  LEFT JOIN LATERAL (
		       SELECT id, counterparty_id, state FROM matches
		        WHERE transfer_id = t.id ORDER BY created_at DESC LIMIT 1
		  ) m ON true
		 WHERE t.id = $1
		 FOR UPDATE OF t`, transferID).
		Scan(&senderID, &recipientID, &transferState, &matchID, &counterpartyID, &matchState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, reject("no_transfer", "That transfer does not exist.")
	}
	if err != nil {
		return nil, err
	}
	if matchID == nil {
		return nil, reject("no_match", "No handover was ever arranged for this transfer.")
	}

	// Only the two people who were supposed to meet can report on the meeting.
	var accusedID uuid.UUID
	switch reporterID {
	case senderID:
		accusedID = *counterpartyID
	case *counterpartyID:
		accusedID = senderID
	default:
		return nil, reject("not_a_party", "You were not part of this handover.")
	}

	if transferState == domain.StateSettled {
		return nil, reject("already_settled",
			"This transfer already settled. Contact support if something was still wrong.")
	}

	// Funds can only be held while they are still held. Once a match has expired
	// the escrow is already back with the counterparty, and the report becomes a
	// record against their account rather than a hold on the money.
	live := *matchState == domain.MatchProposed ||
		*matchState == domain.MatchSenderConfirmed ||
		*matchState == domain.MatchCounterpartyConfirmed
	freeze := live && domain.FreezesEscrow(kind)

	if freeze {
		// No ledger movement: the escrow simply stays where it is. Neither
		// `disputed` state is swept, so nothing releases it on a timer.
		if _, err := tx.Exec(ctx,
			`UPDATE matches SET state = 'disputed', rejection_reason = $2 WHERE id = $1`,
			*matchID, kind); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE transfers SET state = 'disputed', updated_at = now() WHERE id = $1`,
			transferID); err != nil {
			return nil, err
		}
	}

	// Suspend on the reports that allege the other party took something or made
	// the meeting unsafe. A disputed count is not a conviction, but it has to
	// stop them being matched with anybody else in the meantime.
	suspend := kind == domain.IncidentCashTaken || kind == domain.IncidentThreatened
	if _, err := tx.Exec(ctx, `
		UPDATE users
		   SET incident_count = incident_count + 1,
		       suspended = suspended OR $2,
		       trust_score = GREATEST(0, trust_score - $3)
		 WHERE id = $1`, accusedID, suspend, penalty(kind)); err != nil {
		return nil, err
	}

	var inc domain.Incident
	err = tx.QueryRow(ctx, `
		INSERT INTO incidents (transfer_id, match_id, reporter_id, accused_id, kind, detail, froze_escrow)
		VALUES ($1,$2,$3,$4,$5::incident_kind,$6,$7)
		RETURNING id, transfer_id, reporter_id, accused_id, kind::text, detail,
		          froze_escrow, status::text, created_at`,
		transferID, *matchID, reporterID, accusedID, kind, detail, freeze).
		Scan(&inc.ID, &inc.TransferID, &inc.ReporterID, &inc.AccusedID, &inc.Kind,
			&inc.Detail, &inc.FrozeEscrow, &inc.Status, &inc.CreatedAt)
	if err != nil {
		if isUnique(err, "incidents_one_open") {
			return nil, reject("already_reported",
				"You have already reported this handover. Someone is looking at it.")
		}
		return nil, fmt.Errorf("record incident: %w", err)
	}

	// Any open cash-out request from a suspended operator has to come off the
	// book immediately, or the engine would keep sending people to them.
	if suspend {
		if _, err := tx.Exec(ctx,
			`UPDATE cashout_requests SET state = 'cancelled' WHERE user_id = $1 AND state = 'open'`,
			accusedID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	full, _ := s.Store.GetTransfer(ctx, transferID)
	s.publish(ctx, "transfer.disputed", full, accusedID)
	s.Hub.Publish(stream.Event{Type: "incident.reported", Data: inc}, reporterID, accusedID)

	return &inc, nil
}

// penalty is how far a report knocks the accused's reliability down.
func penalty(kind string) int {
	switch kind {
	case domain.IncidentThreatened:
		return 60
	case domain.IncidentCashTaken:
		return 50
	case domain.IncidentWrongAmount:
		return 20
	default: // a no-show is careless rather than dangerous
		return 10
	}
}
