package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"tender/api/internal/ledger"
	"tender/api/internal/money"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://tender:tender@localhost:5433/tender?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no database: %v", err)
	}
	return pool
}

// Seeding tops the float up to FloatCapital and hands out opening balances.
// Against a deployment holding real value that fabricates money the bank does
// not have, and wipes out the reconciliation that made the books mean anything.
func TestSeedingIsRefusedOnceTheBooksAreReconciled(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	ctx := context.Background()

	if err := Truncate(ctx, pool); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// An empty database is a laptop: seeding is exactly what it is for.
	if err := CheckSafeToSeed(ctx, pool); err != nil {
		t.Fatalf("an empty database should be seedable, got %v", err)
	}

	// Reconcile through the real path rather than hand-writing a row, so the
	// test blocks on what actually happens in production.
	posted, err := ledger.ReconcileFloat(ctx, pool, money.Kobo(1925),
		"safety-test-opening", "probe")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !posted {
		t.Fatal("reconciliation did not post")
	}

	err = CheckSafeToSeed(ctx, pool)
	if !errors.Is(err, ErrLiveDeployment) {
		t.Errorf("got %v, want ErrLiveDeployment: a reconciled float must block seeding", err)
	}

	_ = Truncate(ctx, pool)
}
