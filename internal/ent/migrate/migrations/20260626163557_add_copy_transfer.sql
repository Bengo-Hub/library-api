-- Create "copy_transfers" table
CREATE TABLE "copy_transfers" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "copy_id" uuid NOT NULL, "from_branch_id" uuid NOT NULL, "to_branch_id" uuid NOT NULL, "status" character varying NOT NULL DEFAULT 'IN_TRANSIT', "initiated_by" character varying NULL, "received_by" character varying NULL, "notes" character varying NULL, "received_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "copytransfer_tenant_id" to table: "copy_transfers"
CREATE INDEX "copytransfer_tenant_id" ON "copy_transfers" ("tenant_id");
-- Create index "copytransfer_tenant_id_copy_id" to table: "copy_transfers"
CREATE INDEX "copytransfer_tenant_id_copy_id" ON "copy_transfers" ("tenant_id", "copy_id");
-- Create index "copytransfer_tenant_id_status" to table: "copy_transfers"
CREATE INDEX "copytransfer_tenant_id_status" ON "copy_transfers" ("tenant_id", "status");
