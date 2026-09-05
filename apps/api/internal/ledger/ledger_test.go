package ledger

import (
	"context"
	"math/rand"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/money"
)

// The ledger's whole guarantee is that value is conserved. These tests run
// against a real database because the invariant is enforced there, by a
// deferred constraint trigger, not in Go.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://tender:tender@localhost:5433/tender?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no database available: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no database available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newUser creates a throwaway user and removes it afterwards.
func newUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	phone := "+234TEST" + uuid.NewString()[:8]
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (phone, display_name) VALUES ($1,'ledger test') RETURNING id`,
		phone).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM ledger_entries WHERE account_id IN
			(SELECT id FROM accounts WHERE user_id=$1)`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE user_id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	})
	return id
}

func TestPostRejectsUnbalancedEntries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newUser(t, pool)

	avail, err := AccountFor(ctx, pool, &u, KindAvailable)
	if err != nil {
		t.Fatal(err)
	}
	esc, err := AccountFor(ctx, pool, &u, KindEscrow)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Post(ctx, pool, nil, []Entry{
		{avail, -100, "test.debit"},
		{esc, 99, "test.credit"},
	}); err == nil {
		t.Fatal("an unbalanced set of entries must be refused")
	}
}

func TestPostRejectsZeroAmounts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newUser(t, pool)
	avail, _ := AccountFor(ctx, pool, &u, KindAvailable)
	esc, _ := AccountFor(ctx, pool, &u, KindEscrow)

	if _, err := Post(ctx, pool, nil, []Entry{
		{avail, 0, "test.nothing"},
		{esc, 0, "test.nothing"},
	}); err == nil {
		t.Fatal("a zero-amount entry is not a movement and must be refused")
	}
}

// Whatever sequence of operations the system performs, the total value in the
// ledger must not change.
func TestValueIsConservedAcrossRandomOperations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	sum := func() int64 {
		var s int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kobo),0) FROM ledger_entries`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	unbalanced := func() int {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM (SELECT tx_id FROM ledger_entries
			  GROUP BY tx_id HAVING SUM(amount_kobo) <> 0) bad`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	before := sum()

	sender := newUser(t, pool)
	recipient := newUser(t, pool)
	counterparty := newUser(t, pool)

	if err := Deposit(ctx, pool, counterparty, money.FromNaira(100000)); err != nil {
		t.Fatal(err)
	}
	if err := FundFloat(ctx, pool, money.FromNaira(100000)); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 25; i++ {
		amount := money.Kobo(rng.Int63n(50000) + 100)
		transfer := uuid.New()
		fee := money.FeeFor(amount, 50)

		if err := LockEscrow(ctx, pool, transfer, counterparty, amount); err != nil {
			t.Fatalf("lock: %v", err)
		}
		switch rng.Intn(3) {
		case 0: // the handover happened
			if err := SettleEscrow(ctx, pool, transfer, counterparty, recipient, amount, fee); err != nil {
				t.Fatalf("settle: %v", err)
			}
		case 1: // nobody turned up
			if err := ReleaseEscrow(ctx, pool, transfer, counterparty, amount, "test.expire"); err != nil {
				t.Fatalf("release: %v", err)
			}
		case 2: // credit extended, then repaid out of escrow
			if err := ExtendCredit(ctx, pool, transfer, sender, recipient, amount, fee); err != nil {
				t.Fatalf("credit: %v", err)
			}
			if err := RepayCreditFromEscrow(ctx, pool, transfer, counterparty, sender, amount); err != nil {
				t.Fatalf("repay: %v", err)
			}
		}
	}

	if got := unbalanced(); got != 0 {
		t.Errorf("%d transactions do not sum to zero", got)
	}
	if after := sum(); after != before {
		t.Errorf("value was created or destroyed: %d kobo before, %d after", before, after)
	}
}

// A default must not create or destroy value either: the debt moves into the
// loss reserve, it does not evaporate.
func TestDefaultConservesValue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var before int64
	_ = pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kobo),0) FROM ledger_entries`).Scan(&before)

	sender := newUser(t, pool)
	recipient := newUser(t, pool)
	amount := money.FromNaira(5000)
	transfer := uuid.New()

	if err := FundFloat(ctx, pool, amount); err != nil {
		t.Fatal(err)
	}
	if err := ExtendCredit(ctx, pool, transfer, sender, recipient, amount, money.FeeFor(amount, 50)); err != nil {
		t.Fatal(err)
	}
	if err := WriteOff(ctx, pool, transfer, sender, amount); err != nil {
		t.Fatal(err)
	}

	owed, err := Balance(ctx, pool, sender, KindObligation)
	if err != nil {
		t.Fatal(err)
	}
	if owed != 0 {
		t.Errorf("obligation should be cleared after write-off, got %s", owed)
	}

	var after int64
	_ = pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kobo),0) FROM ledger_entries`).Scan(&after)
	if after != before {
		t.Errorf("write-off changed total value: %d -> %d", before, after)
	}
}
