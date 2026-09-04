// Package migrate applies schema migrations from inside the binary.
//
// Tender's migrations used to be applied by a shell script with psql, which
// works from a laptop and not from a host that only ever runs the container.
// Embedding them means the schema travels with the code that expects it, and a
// deployment cannot start against a database it predates.
//
// Applying migrations on boot is safe here because it is guarded by an advisory
// lock: when several instances start at once, one applies and the others wait
// and then find nothing to do.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var files embed.FS

// lockID is an arbitrary constant; it only has to be the same in every instance.
const lockID = 8_675_309

// Up applies every migration that has not been applied yet, in filename order.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Serialise across instances. Two containers booting together would
	// otherwise both try to add the same column.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := files.ReadDir("sql")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT true FROM schema_migrations WHERE filename = $1`, name).Scan(&applied); err == nil {
			continue
		}

		body, err := files.ReadFile("sql/" + name)
		if err != nil {
			return err
		}
		slog.Info("applying migration", "file", name)

		// Deliberately not wrapped in one transaction: several migrations use
		// ALTER TYPE ... ADD VALUE, which Postgres refuses to run inside a
		// transaction block that later uses the new value. Each file manages
		// its own BEGIN/COMMIT instead.
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			return err
		}
	}
	return nil
}
