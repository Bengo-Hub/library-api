-- Modify "library_users" table
ALTER TABLE "library_users" ADD COLUMN "pin_hash" character varying NULL, ADD COLUMN "pin_fast_hash" character varying NULL;
