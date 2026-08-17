#!/bin/sh
# Entrypoint for library-api: wait for DB, run migrations + seed, then start the server.

set -e

# Use the direct PostgreSQL URL for migrate/seed to bypass PgBouncer transaction mode.
MIGRATE_URL="${POSTGRES_MIGRATE_URL:-$POSTGRES_URL}"

echo "=========================================="
echo "Library-API Service Startup"
echo "=========================================="

echo "Waiting for database and running migrations..."
MAX_RETRIES=60
RETRY_COUNT=0
MIGRATE_OUT="/tmp/library-migrate.out"

until POSTGRES_URL="$MIGRATE_URL" /usr/local/bin/library-migrate > "$MIGRATE_OUT" 2>&1 || [ $RETRY_COUNT -eq $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT+1))
  echo "Migration attempt $RETRY_COUNT/$MAX_RETRIES failed:"
  tail -c 2000 "$MIGRATE_OUT"
  sleep 5
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "Migrations did not succeed after $MAX_RETRIES attempts. Last error:"
  tail -c 2000 "$MIGRATE_OUT"
  exit 1
fi

echo "Migrations applied successfully"

echo ""
echo "=========================================="
echo "Running seed (idempotent)"
echo "=========================================="
POSTGRES_URL="$MIGRATE_URL" /usr/local/bin/library-seed || echo "Seed completed with warnings (non-fatal)"

echo "Preparing media + e-book directories on the persistent volume..."
mkdir -p "${MEDIA_ROOT:-/data/media}/images"
mkdir -p "${EBOOK_ROOT:-/data/media/ebooks}"

echo ""
echo "=========================================="
echo "Starting Library-API server"
echo "=========================================="
echo ""

exec /usr/local/bin/library
