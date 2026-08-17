-- Modify "stock_counts" table
-- DEFAULT 0 backfills existing rows for the NOT NULL constraint; dropped immediately after so
-- the column matches the ent schema (no DB-level default — app always supplies a value).
ALTER TABLE "stock_counts" ADD COLUMN "expected_value" numeric(18,4) NOT NULL DEFAULT 0, ADD COLUMN "scanned_value" numeric(18,4) NOT NULL DEFAULT 0, ADD COLUMN "missing_value" numeric(18,4) NOT NULL DEFAULT 0;
ALTER TABLE "stock_counts" ALTER COLUMN "expected_value" DROP DEFAULT, ALTER COLUMN "scanned_value" DROP DEFAULT, ALTER COLUMN "missing_value" DROP DEFAULT;
