-- Modify "circulation_rules" table
ALTER TABLE "circulation_rules" ADD COLUMN "late_fee_uses_rental_charge" boolean NOT NULL DEFAULT false;
-- Create index "fine_tenant_id_loan_id" to table: "fines"
CREATE INDEX "fine_tenant_id_loan_id" ON "fines" ("tenant_id", "loan_id");
-- Create index "fine_tenant_id_status_assessed_at" to table: "fines"
CREATE INDEX "fine_tenant_id_status_assessed_at" ON "fines" ("tenant_id", "status", "assessed_at");
-- Create "fine_payments" table
CREATE TABLE "fine_payments" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "fine_id" uuid NOT NULL, "member_id" uuid NOT NULL, "amount" numeric(18,4) NOT NULL, "method" character varying NOT NULL DEFAULT 'CASH', "reference" character varying NULL, "note" character varying NULL, "received_by" character varying NULL, "paid_at" timestamptz NOT NULL, "idempotency_key" character varying NULL, PRIMARY KEY ("id"));
-- Create index "finepayment_tenant_id" to table: "fine_payments"
CREATE INDEX "finepayment_tenant_id" ON "fine_payments" ("tenant_id");
-- Create index "finepayment_tenant_id_fine_id" to table: "fine_payments"
CREATE INDEX "finepayment_tenant_id_fine_id" ON "fine_payments" ("tenant_id", "fine_id");
-- Create index "finepayment_tenant_id_idempotency_key" to table: "fine_payments"
CREATE UNIQUE INDEX "finepayment_tenant_id_idempotency_key" ON "fine_payments" ("tenant_id", "idempotency_key");
-- Create index "finepayment_tenant_id_paid_at" to table: "fine_payments"
CREATE INDEX "finepayment_tenant_id_paid_at" ON "fine_payments" ("tenant_id", "paid_at");
