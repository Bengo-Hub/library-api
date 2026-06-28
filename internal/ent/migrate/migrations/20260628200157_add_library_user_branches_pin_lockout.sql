-- Modify "library_users" table
ALTER TABLE "library_users" ADD COLUMN "branch_ids" jsonb NULL, ADD COLUMN "pin_failed_attempts" bigint NOT NULL DEFAULT 0, ADD COLUMN "pin_locked_until" timestamptz NULL;
