-- Modify "member_tiers" table
ALTER TABLE "member_tiers" ADD COLUMN "enrollment_period_months" bigint NULL, ADD COLUMN "max_age_years" bigint NULL, ADD COLUMN "min_age_years" bigint NULL, ADD COLUMN "graduated_tier_id" uuid NULL;
-- Modify "members" table
ALTER TABLE "members" ADD COLUMN "birth_date" timestamptz NULL;
-- Create "acquisition_budgets" table
CREATE TABLE "acquisition_budgets" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "fiscal_year" bigint NOT NULL, "total_amount" numeric(18,4) NOT NULL, "allocated" numeric(18,4) NOT NULL, "spent" numeric(18,4) NOT NULL, "status" character varying NOT NULL DEFAULT 'OPEN', "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "acquisitionbudget_tenant_id" to table: "acquisition_budgets"
CREATE INDEX "acquisitionbudget_tenant_id" ON "acquisition_budgets" ("tenant_id");
-- Create index "acquisitionbudget_tenant_id_fiscal_year" to table: "acquisition_budgets"
CREATE INDEX "acquisitionbudget_tenant_id_fiscal_year" ON "acquisition_budgets" ("tenant_id", "fiscal_year");
-- Create index "acquisitionbudget_tenant_id_name" to table: "acquisition_budgets"
CREATE INDEX "acquisitionbudget_tenant_id_name" ON "acquisition_budgets" ("tenant_id", "name");
-- Create "acquisition_funds" table
CREATE TABLE "acquisition_funds" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "budget_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NULL, "allocated_amount" numeric(18,4) NOT NULL, "spent" numeric(18,4) NOT NULL, "description" character varying NULL, PRIMARY KEY ("id"));
-- Create index "acquisitionfund_tenant_id" to table: "acquisition_funds"
CREATE INDEX "acquisitionfund_tenant_id" ON "acquisition_funds" ("tenant_id");
-- Create index "acquisitionfund_tenant_id_budget_id" to table: "acquisition_funds"
CREATE INDEX "acquisitionfund_tenant_id_budget_id" ON "acquisition_funds" ("tenant_id", "budget_id");
-- Create index "acquisitionfund_tenant_id_code" to table: "acquisition_funds"
CREATE INDEX "acquisitionfund_tenant_id_code" ON "acquisition_funds" ("tenant_id", "code");
-- Create "acquisition_invoices" table
CREATE TABLE "acquisition_invoices" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "vendor_id" uuid NOT NULL, "po_id" uuid NULL, "invoice_no" character varying NULL, "reference_id" character varying NULL, "treasury_invoice_id" uuid NULL, "invoice_date" timestamptz NULL, "amount" numeric(18,4) NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "acquisitioninvoice_tenant_id" to table: "acquisition_invoices"
CREATE INDEX "acquisitioninvoice_tenant_id" ON "acquisition_invoices" ("tenant_id");
-- Create index "acquisitioninvoice_tenant_id_po_id" to table: "acquisition_invoices"
CREATE INDEX "acquisitioninvoice_tenant_id_po_id" ON "acquisition_invoices" ("tenant_id", "po_id");
-- Create index "acquisitioninvoice_tenant_id_status" to table: "acquisition_invoices"
CREATE INDEX "acquisitioninvoice_tenant_id_status" ON "acquisition_invoices" ("tenant_id", "status");
-- Create index "acquisitioninvoice_tenant_id_vendor_id" to table: "acquisition_invoices"
CREATE INDEX "acquisitioninvoice_tenant_id_vendor_id" ON "acquisition_invoices" ("tenant_id", "vendor_id");
-- Create "authorized_values" table
CREATE TABLE "authorized_values" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "category" character varying NOT NULL, "value" character varying NOT NULL, "label" character varying NULL, "description" character varying NULL, "is_system" boolean NOT NULL DEFAULT false, "display_order" bigint NOT NULL DEFAULT 0, "is_active" boolean NOT NULL DEFAULT true, PRIMARY KEY ("id"));
-- Create index "authorizedvalue_tenant_id" to table: "authorized_values"
CREATE INDEX "authorizedvalue_tenant_id" ON "authorized_values" ("tenant_id");
-- Create index "authorizedvalue_tenant_id_category_display_order" to table: "authorized_values"
CREATE INDEX "authorizedvalue_tenant_id_category_display_order" ON "authorized_values" ("tenant_id", "category", "display_order");
-- Create index "authorizedvalue_tenant_id_category_value" to table: "authorized_values"
CREATE UNIQUE INDEX "authorizedvalue_tenant_id_category_value" ON "authorized_values" ("tenant_id", "category", "value");
-- Create "circulation_rules" table
CREATE TABLE "circulation_rules" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "branch_id" uuid NULL, "tier_id" uuid NULL, "item_format" character varying NULL, "loan_period_days" bigint NOT NULL DEFAULT 14, "loan_period_hours" bigint NOT NULL DEFAULT 0, "is_hourly" boolean NOT NULL DEFAULT false, "max_renewals" bigint NOT NULL DEFAULT 2, "holdable" boolean NOT NULL DEFAULT true, "fine_per_day" numeric(10,4) NOT NULL, "grace_days" bigint NOT NULL DEFAULT 0, "max_fine_cap" numeric(18,4) NOT NULL, "cap_fine_at_replacement_price" boolean NOT NULL DEFAULT false, "rental_charge" numeric(18,4) NOT NULL, "replacement_cost" numeric(18,4) NOT NULL, "processing_fee" numeric(18,4) NOT NULL, "due_date_mode" character varying NOT NULL DEFAULT 'DAYS', "label" character varying NULL, PRIMARY KEY ("id"));
-- Create index "circulationrule_tenant_id" to table: "circulation_rules"
CREATE INDEX "circulationrule_tenant_id" ON "circulation_rules" ("tenant_id");
-- Create index "circulationrule_tenant_id_branch_id_tier_id_item_format" to table: "circulation_rules"
CREATE INDEX "circulationrule_tenant_id_branch_id_tier_id_item_format" ON "circulation_rules" ("tenant_id", "branch_id", "tier_id", "item_format");
-- Create "library_holidays" table
CREATE TABLE "library_holidays" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "branch_id" uuid NULL, "holiday_date" timestamptz NOT NULL, "description" character varying NULL, "is_recurring" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "libraryholiday_tenant_id" to table: "library_holidays"
CREATE INDEX "libraryholiday_tenant_id" ON "library_holidays" ("tenant_id");
-- Create index "libraryholiday_tenant_id_branch_id_holiday_date" to table: "library_holidays"
CREATE INDEX "libraryholiday_tenant_id_branch_id_holiday_date" ON "library_holidays" ("tenant_id", "branch_id", "holiday_date");
-- Create "member_notification_prefs" table
CREATE TABLE "member_notification_prefs" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "member_id" uuid NOT NULL, "event_type" character varying NOT NULL, "channel" character varying NOT NULL, "is_enabled" boolean NOT NULL DEFAULT true, PRIMARY KEY ("id"));
-- Create index "membernotificationpref_tenant_id" to table: "member_notification_prefs"
CREATE INDEX "membernotificationpref_tenant_id" ON "member_notification_prefs" ("tenant_id");
-- Create index "membernotificationpref_tenant_id_member_id_event_type_channel" to table: "member_notification_prefs"
CREATE UNIQUE INDEX "membernotificationpref_tenant_id_member_id_event_type_channel" ON "member_notification_prefs" ("tenant_id", "member_id", "event_type", "channel");
-- Create "purchase_orders" table
CREATE TABLE "purchase_orders" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "po_number" character varying NULL, "vendor_id" uuid NOT NULL, "fund_id" uuid NULL, "status" character varying NOT NULL DEFAULT 'DRAFT', "order_date" timestamptz NULL, "expected_date" timestamptz NULL, "notes" character varying NULL, "subtotal" numeric(18,4) NOT NULL, "tax" numeric(18,4) NOT NULL, "total" numeric(18,4) NOT NULL, "currency_code" character varying NOT NULL DEFAULT 'KES', PRIMARY KEY ("id"));
-- Create index "purchaseorder_tenant_id" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id" ON "purchase_orders" ("tenant_id");
-- Create index "purchaseorder_tenant_id_po_number" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_po_number" ON "purchase_orders" ("tenant_id", "po_number");
-- Create index "purchaseorder_tenant_id_status" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_status" ON "purchase_orders" ("tenant_id", "status");
-- Create index "purchaseorder_tenant_id_vendor_id" to table: "purchase_orders"
CREATE INDEX "purchaseorder_tenant_id_vendor_id" ON "purchase_orders" ("tenant_id", "vendor_id");
-- Create "purchase_order_lines" table
CREATE TABLE "purchase_order_lines" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "po_id" uuid NOT NULL, "bib_record_id" uuid NULL, "title" character varying NULL, "isbn" character varying NULL, "author" character varying NULL, "unit_price" numeric(18,4) NOT NULL, "quantity" bigint NOT NULL DEFAULT 1, "received_qty" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'PENDING', "notes" character varying NULL, PRIMARY KEY ("id"));
-- Create index "purchaseorderline_tenant_id" to table: "purchase_order_lines"
CREATE INDEX "purchaseorderline_tenant_id" ON "purchase_order_lines" ("tenant_id");
-- Create index "purchaseorderline_tenant_id_po_id" to table: "purchase_order_lines"
CREATE INDEX "purchaseorderline_tenant_id_po_id" ON "purchase_order_lines" ("tenant_id", "po_id");
-- Create "recall_requests" table
CREATE TABLE "recall_requests" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "loan_id" uuid NOT NULL, "hold_id" uuid NULL, "requested_by_member_id" uuid NOT NULL, "new_due_at" timestamptz NOT NULL, "notify_sent_at" timestamptz NULL, "status" character varying NOT NULL DEFAULT 'PENDING', PRIMARY KEY ("id"));
-- Create index "recallrequest_tenant_id" to table: "recall_requests"
CREATE INDEX "recallrequest_tenant_id" ON "recall_requests" ("tenant_id");
-- Create index "recallrequest_tenant_id_loan_id_status" to table: "recall_requests"
CREATE INDEX "recallrequest_tenant_id_loan_id_status" ON "recall_requests" ("tenant_id", "loan_id", "status");
-- Create "vendors" table
CREATE TABLE "vendors" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NULL, "contact_name" character varying NULL, "contact_email" character varying NULL, "contact_phone" character varying NULL, "address" character varying NULL, "website" character varying NULL, "account_number" character varying NULL, "payment_terms" character varying NOT NULL DEFAULT 'NET_30', "notes" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, PRIMARY KEY ("id"));
-- Create index "vendor_tenant_id" to table: "vendors"
CREATE INDEX "vendor_tenant_id" ON "vendors" ("tenant_id");
-- Create index "vendor_tenant_id_code" to table: "vendors"
CREATE UNIQUE INDEX "vendor_tenant_id_code" ON "vendors" ("tenant_id", "code");
-- Create index "vendor_tenant_id_name" to table: "vendors"
CREATE INDEX "vendor_tenant_id_name" ON "vendors" ("tenant_id", "name");
