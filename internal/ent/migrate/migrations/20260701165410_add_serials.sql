-- Create "serial_issues" table
CREATE TABLE "serial_issues" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "subscription_id" uuid NOT NULL, "volume" character varying NULL, "issue_no" character varying NULL, "expected_date" timestamptz NOT NULL, "received_date" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'EXPECTED', "copy_id" uuid NULL, "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "serialissue_tenant_id" to table: "serial_issues"
CREATE INDEX "serialissue_tenant_id" ON "serial_issues" ("tenant_id");
-- Create index "serialissue_tenant_id_expected_date" to table: "serial_issues"
CREATE INDEX "serialissue_tenant_id_expected_date" ON "serial_issues" ("tenant_id", "expected_date");
-- Create index "serialissue_tenant_id_status" to table: "serial_issues"
CREATE INDEX "serialissue_tenant_id_status" ON "serial_issues" ("tenant_id", "status");
-- Create index "serialissue_tenant_id_subscription_id" to table: "serial_issues"
CREATE INDEX "serialissue_tenant_id_subscription_id" ON "serial_issues" ("tenant_id", "subscription_id");
-- Create "serial_routing_lists" table
CREATE TABLE "serial_routing_lists" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "subscription_id" uuid NOT NULL, "member_id" uuid NOT NULL, "position" bigint NOT NULL DEFAULT 0, PRIMARY KEY ("id"));
-- Create index "serialroutinglist_tenant_id" to table: "serial_routing_lists"
CREATE INDEX "serialroutinglist_tenant_id" ON "serial_routing_lists" ("tenant_id");
-- Create index "serialroutinglist_tenant_id_subscription_id_member_id" to table: "serial_routing_lists"
CREATE UNIQUE INDEX "serialroutinglist_tenant_id_subscription_id_member_id" ON "serial_routing_lists" ("tenant_id", "subscription_id", "member_id");
-- Create index "serialroutinglist_tenant_id_subscription_id_position" to table: "serial_routing_lists"
CREATE INDEX "serialroutinglist_tenant_id_subscription_id_position" ON "serial_routing_lists" ("tenant_id", "subscription_id", "position");
-- Create "serial_subscriptions" table
CREATE TABLE "serial_subscriptions" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "bib_record_id" uuid NOT NULL, "vendor_id" uuid NULL, "fund_id" uuid NULL, "start_date" timestamptz NOT NULL, "end_date" timestamptz NULL, "frequency" character varying NOT NULL, "price" numeric(18,4) NOT NULL, "currency_code" character varying NOT NULL DEFAULT 'KES', "status" character varying NOT NULL DEFAULT 'ACTIVE', "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "serialsubscription_tenant_id" to table: "serial_subscriptions"
CREATE INDEX "serialsubscription_tenant_id" ON "serial_subscriptions" ("tenant_id");
-- Create index "serialsubscription_tenant_id_bib_record_id" to table: "serial_subscriptions"
CREATE INDEX "serialsubscription_tenant_id_bib_record_id" ON "serial_subscriptions" ("tenant_id", "bib_record_id");
-- Create index "serialsubscription_tenant_id_status" to table: "serial_subscriptions"
CREATE INDEX "serialsubscription_tenant_id_status" ON "serial_subscriptions" ("tenant_id", "status");
