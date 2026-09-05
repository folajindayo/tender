// Package store owns the database pool and the read queries behind the API.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/domain"
	"tender/api/internal/money"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := NewPool(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// NewPool opens a pool that is safe to point at a connection pooler.
//
// Managed Postgres hands out a PgBouncer endpoint, and PgBouncer in transaction
// pooling mode gives each transaction whichever server connection is free. pgx
// defaults to caching prepared statements per client connection, so a cached
// statement name eventually travels to a backend that never prepared it and the
// query fails with "prepared statement does not exist". It only shows up once
// more than one transaction is in flight, which means it survives every quiet
// test and appears under load.
//
// QueryExecModeDescribeExec describes each statement immediately before running
// it and prepares nothing that outlives the call, so nothing is cached across a
// pooled connection. The cheaper QueryExecModeExec is wrong here: it skips the
// describe, so pgx never learns a parameter's type and sends jsonb columns as
// untyped text, which Postgres rejects outright.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	cfg.ConnConfig.StatementCacheCapacity = 0
	cfg.ConnConfig.DescriptionCacheCapacity = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func (s *Store) Close() { s.Pool.Close() }

// userSelect projects a user together with their three balances. The obligation
// account carries a negative balance when money is owed, so it is negated here
// to give the API a positive "owed" figure.
const userSelect = `
SELECT u.id, u.phone, u.display_name, u.avatar_emoji, u.city,
       COALESCE(u.lat,0), COALESCE(u.lng,0),
       u.trust_score, u.settled_count, u.defaulted_count,
       u.credit_limit_kobo, u.sending_frozen, u.suspended, u.incident_count,
       COALESCE(av.balance_kobo,0),
       COALESCE(es.balance_kobo,0),
       -COALESCE(ob.balance_kobo,0)
  FROM users u
  LEFT JOIN account_balances av ON av.user_id = u.id AND av.kind = 'available'
  LEFT JOIN account_balances es ON es.user_id = u.id AND es.kind = 'escrow'
  LEFT JOIN account_balances ob ON ob.user_id = u.id AND ob.kind = 'obligation'`

func scanUser(row interface{ Scan(...any) error }) (*domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Phone, &u.DisplayName, &u.AvatarEmoji, &u.City,
		&u.Lat, &u.Lng, &u.TrustScore, &u.SettledCount, &u.DefaultedCount,
		&u.CreditLimit, &u.SendingFrozen, &u.Suspended, &u.IncidentCount,
		&u.Available, &u.Escrow, &u.Owed)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return scanUser(s.Pool.QueryRow(ctx, userSelect+` WHERE u.id = $1`, id))
}

const transferSelect = `
SELECT t.id, t.ref, t.sender_id, t.recipient_id, t.amount_kobo, t.fee_kobo,
       t.mode::text, t.state::text, COALESCE(t.note,''), t.created_at,
       t.expires_at, t.settled_at,
       su.display_name, ru.display_name,
       t.recipient_account_number, t.recipient_sort_code,
       t.recipient_account_name, t.recipient_bank_name,
       p.state::text, COALESCE(p.provider_ref,''), COALESCE(p.last_error,'')
  FROM transfers t
  JOIN users su ON su.id = t.sender_id
  LEFT JOIN users ru ON ru.id = t.recipient_id
  LEFT JOIN payouts p ON p.transfer_id = t.id`

func scanTransfer(row interface{ Scan(...any) error }) (*domain.Transfer, error) {
	var t domain.Transfer
	var recipientName, acctNo, sortCode, acctName, bankName *string
	var payoutState, payoutRef, payoutErr *string

	err := row.Scan(&t.ID, &t.Ref, &t.SenderID, &t.RecipientID, &t.Amount, &t.Fee,
		&t.Mode, &t.State, &t.Note, &t.CreatedAt, &t.ExpiresAt, &t.SettledAt,
		&t.SenderName, &recipientName,
		&acctNo, &sortCode, &acctName, &bankName,
		&payoutState, &payoutRef, &payoutErr)
	if err != nil {
		return nil, err
	}

	if recipientName != nil {
		t.RecipientName = *recipientName
	}
	// A bank destination is stored on the transfer itself, so the recipient
	// never needs a Tender account.
	if acctNo != nil && sortCode != nil {
		t.Bank = &domain.BankAccount{AccountNumber: *acctNo, SortCode: *sortCode}
		if acctName != nil {
			t.Bank.AccountName = *acctName
			t.RecipientName = *acctName
		}
		if bankName != nil {
			t.Bank.BankName = *bankName
		}
	}
	// "Settled" and "arrived in the account" are different claims, so delivery
	// is reported separately rather than folded into the transfer state.
	if payoutState != nil {
		t.Payout = &domain.Payout{State: *payoutState}
		if t.Bank != nil {
			t.Payout.AccountName = t.Bank.AccountName
			t.Payout.BankName = t.Bank.BankName
		}
		if payoutRef != nil {
			t.Payout.Reference = *payoutRef
		}
		if payoutErr != nil {
			t.Payout.LastError = *payoutErr
		}
	}
	return &t, nil
}

func (s *Store) GetTransfer(ctx context.Context, id uuid.UUID) (*domain.Transfer, error) {
	t, err := scanTransfer(s.Pool.QueryRow(ctx, transferSelect+` WHERE t.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if m, err := s.LiveMatch(ctx, id); err == nil {
		t.Match = m
	}
	return t, nil
}

// LiveMatch returns the active match for a transfer, or nil when there is none.
func (s *Store) LiveMatch(ctx context.Context, transferID uuid.UUID) (*domain.Match, error) {
	var m domain.Match
	err := s.Pool.QueryRow(ctx, `
		SELECT m.id, m.transfer_id, m.counterparty_id, u.display_name, m.amount_kobo,
		       m.handover_code, m.distance_m, v.name, v.address, m.state::text, m.expires_at
		  FROM matches m
		  JOIN users u ON u.id = m.counterparty_id
		  JOIN cashout_requests c ON c.id = m.cashout_request_id
		  JOIN venues v ON v.id = c.venue_id
		 WHERE m.transfer_id = $1
		 ORDER BY m.created_at DESC LIMIT 1`, transferID).
		Scan(&m.ID, &m.TransferID, &m.CounterpartyID, &m.CounterpartyName, &m.Amount,
			&m.HandoverCode, &m.DistanceM, &m.VenueName, &m.VenueAddress,
			&m.State, &m.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// TransfersForUser returns everything a user is involved in, newest first --
// as sender, as recipient, or as the settling counterparty.
func (s *Store) TransfersForUser(ctx context.Context, id uuid.UUID) ([]domain.Transfer, error) {
	rows, err := s.Pool.Query(ctx, transferSelect+`
		WHERE t.sender_id = $1 OR t.recipient_id = $1
		   OR EXISTS (SELECT 1 FROM matches m WHERE m.transfer_id = t.id AND m.counterparty_id = $1)
		ORDER BY t.created_at DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		if m, err := s.LiveMatch(ctx, t.ID); err == nil {
			t.Match = m
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// CashoutsFor returns one operator's own open requests.
//
// There is deliberately no endpoint that lists everybody's. An open request says
// "this person will be holding cash, here, shortly", and publishing that was a
// target list for anyone who cared to read it. The matching engine is the only
// thing that needs the full book.
func (s *Store) CashoutsFor(ctx context.Context, userID uuid.UUID) ([]domain.CashoutRequest, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.user_id, u.display_name, c.amount_kobo, c.tolerance_kobo,
		       c.venue_id, v.name, v.address, c.state::text
		  FROM cashout_requests c
		  JOIN users u  ON u.id = c.user_id
		  JOIN venues v ON v.id = c.venue_id
		 WHERE c.user_id = $1 AND c.state IN ('open','matched')
		 ORDER BY c.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.CashoutRequest
	for rows.Next() {
		var c domain.CashoutRequest
		if err := rows.Scan(&c.ID, &c.UserID, &c.UserName, &c.Amount, &c.Tolerance,
			&c.VenueID, &c.VenueName, &c.Address, &c.State); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// VenuesFor lists the premises an operator runs. Venue locations are public --
// they are shopfronts -- but which of them currently wants cash is not.
func (s *Store) VenuesFor(ctx context.Context, operatorID uuid.UUID) ([]domain.Venue, error) {
	return s.venues(ctx, `WHERE v.operator_id = $1 AND v.active`, operatorID)
}

// ActiveVenues lists every verified, active venue.
func (s *Store) ActiveVenues(ctx context.Context) ([]domain.Venue, error) {
	return s.venues(ctx, `WHERE v.active AND v.verified`)
}

func (s *Store) venues(ctx context.Context, where string, args ...any) ([]domain.Venue, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT v.id, v.name, v.kind::text, v.address, v.lat, v.lng,
		       to_char(v.opens_at,'HH24:MI'), to_char(v.closes_at,'HH24:MI'), v.verified,
		       ((now() AT TIME ZONE 'Africa/Lagos')::time BETWEEN v.opens_at AND v.closes_at)
		  FROM venues v `+where+` ORDER BY v.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Venue
	for rows.Next() {
		var v domain.Venue
		if err := rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Address, &v.Lat, &v.Lng,
			&v.OpensAt, &v.ClosesAt, &v.Verified, &v.Open); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- audit

type AuditLine struct {
	TxID      uuid.UUID  `json:"txId"`
	Reason    string     `json:"reason"`
	Account   string     `json:"account"`
	Owner     string     `json:"owner"`
	Amount    money.Kobo `json:"amountKobo"`
	Ref       *int64     `json:"transferRef,omitempty"`
	CreatedAt string     `json:"createdAt"`
}

type Audit struct {
	Lines        []AuditLine `json:"lines"`
	GlobalSum    money.Kobo  `json:"globalSumKobo"`
	Unbalanced   int         `json:"unbalancedTransactions"`
	FloatKobo    money.Kobo  `json:"floatKobo"`
	RevenueKobo  money.Kobo  `json:"revenueKobo"`
	AtRiskKobo   money.Kobo  `json:"capitalAtRiskKobo"`
	EscrowedKobo money.Kobo  `json:"escrowedKobo"`
}

// LedgerAudit is the proof shown on stage: every transaction sums to zero, and
// capital at risk is the total of outstanding Tier 1 obligations.
func (s *Store) LedgerAudit(ctx context.Context, limit int) (*Audit, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.tx_id, e.reason, a.kind::text, COALESCE(u.display_name,'platform'),
		       e.amount_kobo, t.ref, to_char(e.created_at,'HH24:MI:SS')
		  FROM ledger_entries e
		  JOIN accounts a ON a.id = e.account_id
		  LEFT JOIN users u ON u.id = a.user_id
		  LEFT JOIN transfers t ON t.id = e.transfer_id
		 ORDER BY e.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	audit := &Audit{Lines: []AuditLine{}}
	for rows.Next() {
		var l AuditLine
		if err := rows.Scan(&l.TxID, &l.Reason, &l.Account, &l.Owner,
			&l.Amount, &l.Ref, &l.CreatedAt); err != nil {
			return nil, err
		}
		audit.Lines = append(audit.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_kobo),0),
		       (SELECT COUNT(*) FROM (
		            SELECT tx_id FROM ledger_entries
		             GROUP BY tx_id HAVING SUM(amount_kobo) <> 0) bad)
		  FROM ledger_entries`).Scan(&audit.GlobalSum, &audit.Unbalanced)
	if err != nil {
		return nil, err
	}

	_ = s.Pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT balance_kobo FROM account_balances WHERE kind='float'),0),
		       COALESCE((SELECT balance_kobo FROM account_balances WHERE kind='revenue'),0),
		       COALESCE((SELECT -SUM(balance_kobo) FROM account_balances WHERE kind='obligation'),0),
		       COALESCE((SELECT SUM(balance_kobo) FROM account_balances WHERE kind='escrow'),0)
		`).Scan(&audit.FloatKobo, &audit.RevenueKobo, &audit.AtRiskKobo, &audit.EscrowedKobo)

	return audit, nil
}
