#!/usr/bin/env bash
# Applies any migration that has not been applied yet, in filename order.
set -euo pipefail
DB_URL="${DATABASE_URL:-postgres://tender:tender@localhost:5432/tender?sslmode=disable}"
cd "$(dirname "$0")/.."

psql "$DB_URL" -v ON_ERROR_STOP=1 -q -c \
  "CREATE TABLE IF NOT EXISTS schema_migrations (
     filename text PRIMARY KEY,
     applied_at timestamptz NOT NULL DEFAULT now());"

# The migrations live with the Go package that embeds them, so the binary and
# this script can never disagree about what the schema is.
for f in apps/api/internal/migrate/sql/*.sql; do
  name="$(basename "$f")"
  applied=$(psql "$DB_URL" -tAc \
    "SELECT 1 FROM schema_migrations WHERE filename='$name'")
  if [ "$applied" = "1" ]; then
    echo "    $name (already applied)"
    continue
  fi
  echo "--> $name"
  psql "$DB_URL" -v ON_ERROR_STOP=1 -q -f "$f"
  psql "$DB_URL" -v ON_ERROR_STOP=1 -q -c \
    "INSERT INTO schema_migrations (filename) VALUES ('$name');"
done
echo "migrations up to date"
