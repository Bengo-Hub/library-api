-- Create "stock_counts" table
CREATE TABLE "stock_counts" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "branch_id" uuid NOT NULL, "reference" character varying NULL, "status" character varying NOT NULL DEFAULT 'COUNTING', "scanned_copy_ids" jsonb NULL, "expected_count" bigint NOT NULL DEFAULT 0, "scanned_count" bigint NOT NULL DEFAULT 0, "missing_count" bigint NOT NULL DEFAULT 0, "counted_by" character varying NULL, "completed_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "stockcount_tenant_id" to table: "stock_counts"
CREATE INDEX "stockcount_tenant_id" ON "stock_counts" ("tenant_id");
-- Create index "stockcount_tenant_id_branch_id_status" to table: "stock_counts"
CREATE INDEX "stockcount_tenant_id_branch_id_status" ON "stock_counts" ("tenant_id", "branch_id", "status");
