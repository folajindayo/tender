// Package bootstrap provisions the initial accounts and settlement capital a
// fresh Tender deployment needs before anyone can transact.
package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/ledger"
	"tender/api/internal/money"
)

// Account is a person to provision, with an optional opening balance and, for
// operators, the premises they run.
type Account struct {
	Key      string
	Phone    string
	Name     string
	Emoji    string
	City     string
	Lat, Lng float64
	Opening  money.Kobo
	Trust    int
	Settled  int

	// Operators run a venue. Only they can post cash-out requests, and only
	// against premises they are accountable for.
	Venue     *Venue
	WantsCash money.Kobo
	Tolerance money.Kobo
}

// Venue is fixed, public premises where a handover may take place.
type Venue struct {
	Name     string
	Kind     string // agent | bank | filling_station | market_office
	Address  string
	OpensAt  string
	ClosesAt string
}

// Accounts is the starting roster. Sender-side users open with no balance
// because they hold physical cash; the cash-out side opens with the digital
// funds they intend to swap for notes.
var Accounts = []Account{
	{
		Key: "ada", Phone: "+2348030000001", Name: "Ada Okafor", Emoji: "🧕",
		City: "Lagos Island", Lat: 6.4541, Lng: 3.3841, Trust: 55,
	},
	{
		Key: "bola", Phone: "+2348030000002", Name: "Bola Adeyemi", Emoji: "👨🏿‍💼",
		City: "Abuja", Lat: 9.0765, Lng: 7.3986, Trust: 60,
	},
	{
		Key: "chidi", Phone: "+2348030000003", Name: "Chidi Nwosu", Emoji: "🧑🏿‍🌾",
		City: "Balogun Market", Lat: 6.4577, Lng: 3.3855,
		Opening: money.FromNaira(50000), Trust: 78, Settled: 12,
		Venue: &Venue{
			Name: "Nwosu Provisions", Kind: "agent",
			Address: "Stall 114, Balogun Market, Lagos Island",
			OpensAt: "07:00", ClosesAt: "19:00",
		},
		WantsCash: money.FromNaira(20000),
	},
	{
		Key: "funke", Phone: "+2348030000004", Name: "Funke Bello", Emoji: "💳",
		City: "Yaba", Lat: 6.4600, Lng: 3.3900,
		Opening: money.FromNaira(200000), Trust: 84, Settled: 40,
		Venue: &Venue{
			Name: "Bello POS Point", Kind: "agent",
			Address: "12 Herbert Macaulay Way, Yaba bus stop",
			OpensAt: "08:00", ClosesAt: "20:00",
		},
		WantsCash: money.FromNaira(5000), Tolerance: money.FromNaira(500),
	},
	{
		Key: "musa", Phone: "+2348030000005", Name: "Musa Ibrahim", Emoji: "🛵",
		City: "Ikoyi", Lat: 6.4650, Lng: 3.3960,
		Opening: money.FromNaira(80000), Trust: 70, Settled: 6,
		Venue: &Venue{
			Name: "Awolowo Fuel & Pay", Kind: "filling_station",
			Address: "48 Awolowo Road, Ikoyi",
			OpensAt: "06:00", ClosesAt: "22:00",
		},
		WantsCash: money.FromNaira(20000),
	},
}

// FloatCapital backs Tier 1 instant credit. Escrow settlement consumes none of
// it, so this only has to cover credit exposure, not transfer volume.
const FloatCapital = money.Kobo(50000000) // ₦500,000

// Provision creates any missing account and tops the float up to its target.
// It is idempotent: running it against a live database changes nothing that
// already exists, and never touches balances of accounts that are present.
func Provision(ctx context.Context, pool *pgxpool.Pool) (map[string]uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var floatBal int64
	_ = tx.QueryRow(ctx,
		`SELECT COALESCE(balance_kobo,0) FROM account_balances WHERE kind='float'`).Scan(&floatBal)
	if money.Kobo(floatBal) < FloatCapital {
		if err := ledger.FundFloat(ctx, tx, FloatCapital-money.Kobo(floatBal)); err != nil {
			return nil, fmt.Errorf("fund float: %w", err)
		}
	}

	ids := make(map[string]uuid.UUID, len(Accounts))
	for _, a := range Accounts {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE phone = $1`, a.Phone).Scan(&id)
		if err == nil {
			ids[a.Key] = id
			continue // already provisioned; leave it exactly as it is
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO users (phone, display_name, avatar_emoji, city, lat, lng, trust_score, settled_count)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
			a.Phone, a.Name, a.Emoji, a.City, a.Lat, a.Lng, a.Trust, a.Settled).Scan(&id); err != nil {
			return nil, fmt.Errorf("create %s: %w", a.Name, err)
		}
		ids[a.Key] = id

		if a.Opening > 0 {
			if err := ledger.Deposit(ctx, tx, id, a.Opening); err != nil {
				return nil, fmt.Errorf("opening balance for %s: %w", a.Name, err)
			}
		}
		if a.Venue == nil {
			continue
		}

		var venueID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO venues (name, kind, address, lat, lng, operator_id, opens_at, closes_at, verified)
			VALUES ($1,$2::venue_kind,$3,$4,$5,$6,$7::time,$8::time,true) RETURNING id`,
			a.Venue.Name, a.Venue.Kind, a.Venue.Address, a.Lat, a.Lng, id,
			a.Venue.OpensAt, a.Venue.ClosesAt).Scan(&venueID); err != nil {
			return nil, fmt.Errorf("venue for %s: %w", a.Name, err)
		}

		if a.WantsCash > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO cashout_requests (user_id, amount_kobo, tolerance_kobo, venue_id)
				VALUES ($1,$2,$3,$4)`,
				id, int64(a.WantsCash), int64(a.Tolerance), venueID); err != nil {
				return nil, fmt.Errorf("cash-out request for %s: %w", a.Name, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

// Truncate erases every account and all activity.
//
// This exists for local development and the end-to-end test suite. It is not
// reachable over HTTP and must never be run against a live deployment.
func Truncate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Deletion order follows the foreign keys inwards: incidents point at
	// matches, matches at cash-out requests, requests at venues, venues at users.
	for _, stmt := range []string{
		`DELETE FROM provider_events`,
		`DELETE FROM payouts`,
		`DELETE FROM sessions`,
		`DELETE FROM incidents`,
		`DELETE FROM ledger_entries`,
		`DELETE FROM matches`,
		`DELETE FROM pledged_notes`,
		`DELETE FROM pledges`,
		`DELETE FROM cashout_requests`,
		`DELETE FROM venues`,
		`DELETE FROM transfers`,
		`DELETE FROM accounts`,
		`DELETE FROM users`,
		`ALTER SEQUENCE transfers_ref_seq RESTART WITH 4471`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("truncate (%s): %w", stmt, err)
		}
	}
	return tx.Commit(ctx)
}
