// Command bootstrap provisions the accounts and settlement capital a fresh
// deployment needs. It is idempotent unless --truncate is passed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"tender/api/internal/bootstrap"
	"tender/api/internal/config"
	"tender/api/internal/store"
)

func main() {
	truncate := flag.Bool("truncate", false,
		"erase all accounts and activity first (local development only)")
	force := flag.Bool("force", false,
		"seed even when the database looks live (it will fabricate money)")
	flag.Parse()

	// Provisioning only touches the database. Configuration problems that have
	// nothing to do with storage (a missing vision key, say) must not block it.
	cfg, err := config.Load()
	if err != nil && cfg.DatabaseURL == "" {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *truncate {
		if err := bootstrap.Truncate(ctx, pool); err != nil {
			fmt.Fprintln(os.Stderr, "truncate:", err)
			os.Exit(1)
		}
		fmt.Println("erased all accounts and activity")
	}

	// Seeding mints float and hands out opening balances. That is fine on a
	// laptop and ruinous on a deployment carrying real value, so the check comes
	// before the write rather than after somebody notices the drift.
	if !*force {
		if err := bootstrap.CheckSafeToSeed(ctx, pool); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "pass -force only if you mean it")
			os.Exit(1)
		}
	}

	ids, err := bootstrap.Provision(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision:", err)
		os.Exit(1)
	}
	for _, a := range bootstrap.Accounts {
		fmt.Printf("%-6s %s  %s\n", a.Key, ids[a.Key], a.Name)
	}
}
