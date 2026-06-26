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

until POSTGRES_URL="$MIGRATE_URL" /usr/local/bin/library-migrate > /dev/null 2>&1 || [ $RETRY_COUNT -eq $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT+1))
  echo "Database not ready yet... (attempt $RETRY_COUNT/$MAX_RETRIES)"
  sleep 5
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "Database connection timeout after $MAX_RETRIES attempts"
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
