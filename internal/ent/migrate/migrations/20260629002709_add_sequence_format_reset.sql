-- Modify "document_sequences" table
ALTER TABLE "document_sequences" ADD COLUMN "format" character varying NULL, ADD COLUMN "reset_period" character varying NOT NULL DEFAULT 'none', ADD COLUMN "period_key" character varying NULL;
