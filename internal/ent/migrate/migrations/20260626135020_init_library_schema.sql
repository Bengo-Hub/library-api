-- Create "audit_logs" table
CREATE TABLE "audit_logs" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "user_id" character varying NULL, "aggregate_type" character varying NOT NULL, "aggregate_id" character varying NULL, "action" character varying NOT NULL, "changes" jsonb NULL, "ip_address" character varying NULL, PRIMARY KEY ("id"));
-- Create index "auditlog_tenant_id" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id" ON "audit_logs" ("tenant_id");
-- Create index "auditlog_tenant_id_aggregate_type_aggregate_id" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id_aggregate_type_aggregate_id" ON "audit_logs" ("tenant_id", "aggregate_type", "aggregate_id");
-- Create index "auditlog_tenant_id_created_at" to table: "audit_logs"
CREATE INDEX "auditlog_tenant_id_created_at" ON "audit_logs" ("tenant_id", "created_at");
-- Create "authors" table
CREATE TABLE "authors" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "sort_name" character varying NULL, "biography" text NULL, PRIMARY KEY ("id"));
-- Create index "author_tenant_id" to table: "authors"
CREATE INDEX "author_tenant_id" ON "authors" ("tenant_id");
-- Create index "author_tenant_id_name" to table: "authors"
CREATE INDEX "author_tenant_id_name" ON "authors" ("tenant_id", "name");
-- Create "bib_records" table
CREATE TABLE "bib_records" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "title" character varying NOT NULL, "subtitle" character varying NULL, "isbn10" character varying NULL, "isbn13" character varying NULL, "issn" character varying NULL, "lccn" character varying NULL, "edition" character varying NULL, "language" character varying NOT NULL DEFAULT 'en', "ddc_classification" character varying NULL, "lc_call_number" character varying NULL, "publication_year" bigint NULL, "page_count" bigint NULL, "publisher_name" character varying NULL, "publisher_id" uuid NULL, "primary_subject_id" uuid NULL, "collection_id" uuid NULL, "format" character varying NOT NULL DEFAULT 'PHYSICAL', "record_status" character varying NOT NULL DEFAULT 'ACTIVE', "summary" text NULL, "cover_image_url" character varying NULL, "authors" jsonb NULL, "dublin_core" jsonb NULL, "marc" jsonb NULL, "default_loan_policy_id" uuid NULL, PRIMARY KEY ("id"));
-- Create index "bibrecord_tenant_id" to table: "bib_records"
CREATE INDEX "bibrecord_tenant_id" ON "bib_records" ("tenant_id");
-- Create index "bibrecord_tenant_id_format" to table: "bib_records"
CREATE INDEX "bibrecord_tenant_id_format" ON "bib_records" ("tenant_id", "format");
-- Create index "bibrecord_tenant_id_isbn10" to table: "bib_records"
CREATE INDEX "bibrecord_tenant_id_isbn10" ON "bib_records" ("tenant_id", "isbn10");
-- Create index "bibrecord_tenant_id_isbn13" to table: "bib_records"
CREATE INDEX "bibrecord_tenant_id_isbn13" ON "bib_records" ("tenant_id", "isbn13");
-- Create index "bibrecord_tenant_id_title" to table: "bib_records"
CREATE INDEX "bibrecord_tenant_id_title" ON "bib_records" ("tenant_id", "title");
-- Create "book_copies" table
CREATE TABLE "book_copies" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "bib_record_id" uuid NOT NULL, "branch_id" uuid NOT NULL, "barcode" character varying NOT NULL, "accession_no" character varying NULL, "call_number" character varying NULL, "shelf_location" character varying NULL, "status" character varying NOT NULL DEFAULT 'AVAILABLE', "condition" character varying NOT NULL DEFAULT 'good', "is_reference_only" boolean NOT NULL DEFAULT false, "acquisition_cost" numeric(18,4) NULL, "acquisition_date" timestamptz NULL, "loan_policy_id" uuid NULL, PRIMARY KEY ("id"));
-- Create index "bookcopy_tenant_id" to table: "book_copies"
CREATE INDEX "bookcopy_tenant_id" ON "book_copies" ("tenant_id");
-- Create index "bookcopy_tenant_id_barcode" to table: "book_copies"
CREATE UNIQUE INDEX "bookcopy_tenant_id_barcode" ON "book_copies" ("tenant_id", "barcode");
-- Create index "bookcopy_tenant_id_bib_record_id" to table: "book_copies"
CREATE INDEX "bookcopy_tenant_id_bib_record_id" ON "book_copies" ("tenant_id", "bib_record_id");
-- Create index "bookcopy_tenant_id_branch_id_status" to table: "book_copies"
CREATE INDEX "bookcopy_tenant_id_branch_id_status" ON "book_copies" ("tenant_id", "branch_id", "status");
-- Create "branches" table
CREATE TABLE "branches" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NOT NULL, "address" character varying NULL, "latitude" double precision NULL, "longitude" double precision NULL, "outlet_id" uuid NULL, "opening_hours" jsonb NULL, "is_default" boolean NOT NULL DEFAULT false, "is_active" boolean NOT NULL DEFAULT true, PRIMARY KEY ("id"));
-- Create index "branch_tenant_id" to table: "branches"
CREATE INDEX "branch_tenant_id" ON "branches" ("tenant_id");
-- Create index "branch_tenant_id_code" to table: "branches"
CREATE UNIQUE INDEX "branch_tenant_id_code" ON "branches" ("tenant_id", "code");
-- Create "collections" table
CREATE TABLE "collections" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NULL, "parent_id" uuid NULL, "is_reference_only" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "collection_tenant_id" to table: "collections"
CREATE INDEX "collection_tenant_id" ON "collections" ("tenant_id");
-- Create index "collection_tenant_id_name" to table: "collections"
CREATE INDEX "collection_tenant_id_name" ON "collections" ("tenant_id", "name");
-- Create index "collection_tenant_id_parent_id" to table: "collections"
CREATE INDEX "collection_tenant_id_parent_id" ON "collections" ("tenant_id", "parent_id");
-- Create "document_sequences" table
CREATE TABLE "document_sequences" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "kind" character varying NOT NULL, "prefix" character varying NULL, "next_value" bigint NOT NULL DEFAULT 1, "pad_width" bigint NOT NULL DEFAULT 5, PRIMARY KEY ("id"));
-- Create index "documentsequence_tenant_id" to table: "document_sequences"
CREATE INDEX "documentsequence_tenant_id" ON "document_sequences" ("tenant_id");
-- Create index "documentsequence_tenant_id_kind" to table: "document_sequences"
CREATE UNIQUE INDEX "documentsequence_tenant_id_kind" ON "document_sequences" ("tenant_id", "kind");
-- Create "ebooks" table
CREATE TABLE "ebooks" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "bib_record_id" uuid NOT NULL, "file_url" character varying NOT NULL, "format" character varying NOT NULL DEFAULT 'PDF', "drm_policy" character varying NOT NULL DEFAULT 'WATERMARK', "lending_model" character varying NOT NULL DEFAULT 'CONTROLLED_DIGITAL', "max_concurrent_loans" bigint NOT NULL DEFAULT 1, "loan_duration_days" bigint NOT NULL DEFAULT 14, "is_purchasable" boolean NOT NULL DEFAULT false, "price" numeric(18,4) NOT NULL, "file_size" bigint NULL, "checksum" character varying NULL, PRIMARY KEY ("id"));
-- Create index "ebook_tenant_id" to table: "ebooks"
CREATE INDEX "ebook_tenant_id" ON "ebooks" ("tenant_id");
-- Create index "ebook_tenant_id_bib_record_id" to table: "ebooks"
CREATE INDEX "ebook_tenant_id_bib_record_id" ON "ebooks" ("tenant_id", "bib_record_id");
-- Create "ebook_loans" table
CREATE TABLE "ebook_loans" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "ebook_id" uuid NOT NULL, "member_id" uuid NOT NULL, "mode" character varying NOT NULL DEFAULT 'ONLINE_READ', "issued_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, "returned_at" timestamptz NULL, "access_token" character varying NULL, "last_read_position" jsonb NULL, PRIMARY KEY ("id"));
-- Create index "ebookloan_tenant_id" to table: "ebook_loans"
CREATE INDEX "ebookloan_tenant_id" ON "ebook_loans" ("tenant_id");
-- Create index "ebookloan_tenant_id_ebook_id_returned_at" to table: "ebook_loans"
CREATE INDEX "ebookloan_tenant_id_ebook_id_returned_at" ON "ebook_loans" ("tenant_id", "ebook_id", "returned_at");
-- Create index "ebookloan_tenant_id_member_id" to table: "ebook_loans"
CREATE INDEX "ebookloan_tenant_id_member_id" ON "ebook_loans" ("tenant_id", "member_id");
-- Create "ebook_purchases" table
CREATE TABLE "ebook_purchases" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "ebook_id" uuid NOT NULL, "member_id" uuid NOT NULL, "treasury_intent_id" character varying NULL, "amount" numeric(18,4) NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "download_token" character varying NULL, "download_count" bigint NOT NULL DEFAULT 0, "purchased_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "ebookpurchase_tenant_id" to table: "ebook_purchases"
CREATE INDEX "ebookpurchase_tenant_id" ON "ebook_purchases" ("tenant_id");
-- Create index "ebookpurchase_tenant_id_member_id" to table: "ebook_purchases"
CREATE INDEX "ebookpurchase_tenant_id_member_id" ON "ebook_purchases" ("tenant_id", "member_id");
-- Create index "ebookpurchase_tenant_id_treasury_intent_id" to table: "ebook_purchases"
CREATE INDEX "ebookpurchase_tenant_id_treasury_intent_id" ON "ebook_purchases" ("tenant_id", "treasury_intent_id");
-- Create "fines" table
CREATE TABLE "fines" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "member_id" uuid NOT NULL, "loan_id" uuid NULL, "reason" character varying NOT NULL DEFAULT 'OVERDUE', "description" character varying NULL, "amount" numeric(18,4) NOT NULL, "amount_paid" numeric(18,4) NOT NULL, "status" character varying NOT NULL DEFAULT 'UNPAID', "treasury_intent_id" character varying NULL, "waived_by" character varying NULL, "assessed_at" timestamptz NULL, "paid_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "fine_tenant_id" to table: "fines"
CREATE INDEX "fine_tenant_id" ON "fines" ("tenant_id");
-- Create index "fine_tenant_id_member_id_status" to table: "fines"
CREATE INDEX "fine_tenant_id_member_id_status" ON "fines" ("tenant_id", "member_id", "status");
-- Create index "fine_tenant_id_treasury_intent_id" to table: "fines"
CREATE INDEX "fine_tenant_id_treasury_intent_id" ON "fines" ("tenant_id", "treasury_intent_id");
-- Create "holds" table
CREATE TABLE "holds" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "bib_record_id" uuid NOT NULL, "member_id" uuid NOT NULL, "branch_id" uuid NOT NULL, "copy_id" uuid NULL, "queue_position" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'WAITING', "placed_at" timestamptz NOT NULL, "ready_at" timestamptz NULL, "expires_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "hold_tenant_id" to table: "holds"
CREATE INDEX "hold_tenant_id" ON "holds" ("tenant_id");
-- Create index "hold_tenant_id_bib_record_id_status" to table: "holds"
CREATE INDEX "hold_tenant_id_bib_record_id_status" ON "holds" ("tenant_id", "bib_record_id", "status");
-- Create index "hold_tenant_id_member_id_status" to table: "holds"
CREATE INDEX "hold_tenant_id_member_id_status" ON "holds" ("tenant_id", "member_id", "status");
-- Create "library_roles" table
CREATE TABLE "library_roles" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "name" character varying NOT NULL, "description" character varying NULL, "permissions" jsonb NULL, "is_system" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "libraryrole_name" to table: "library_roles"
CREATE UNIQUE INDEX "libraryrole_name" ON "library_roles" ("name");
-- Create "library_users" table
CREATE TABLE "library_users" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "user_id" character varying NOT NULL, "email" character varying NULL, "display_name" character varying NULL, "roles" jsonb NULL, "is_active" boolean NOT NULL DEFAULT true, PRIMARY KEY ("id"));
-- Create index "libraryuser_tenant_id" to table: "library_users"
CREATE INDEX "libraryuser_tenant_id" ON "library_users" ("tenant_id");
-- Create index "libraryuser_tenant_id_email" to table: "library_users"
CREATE INDEX "libraryuser_tenant_id_email" ON "library_users" ("tenant_id", "email");
-- Create index "libraryuser_tenant_id_user_id" to table: "library_users"
CREATE UNIQUE INDEX "libraryuser_tenant_id_user_id" ON "library_users" ("tenant_id", "user_id");
-- Create "loans" table
CREATE TABLE "loans" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "loan_no" character varying NULL, "copy_id" uuid NOT NULL, "member_id" uuid NOT NULL, "branch_id" uuid NOT NULL, "checkout_at" timestamptz NOT NULL, "due_at" timestamptz NOT NULL, "returned_at" timestamptz NULL, "renewals_count" bigint NOT NULL DEFAULT 0, "status" character varying NOT NULL DEFAULT 'ACTIVE', "in_house" boolean NOT NULL DEFAULT false, "checked_out_by" character varying NULL, "returned_by" character varying NULL, PRIMARY KEY ("id"));
-- Create index "loan_tenant_id" to table: "loans"
CREATE INDEX "loan_tenant_id" ON "loans" ("tenant_id");
-- Create index "loan_tenant_id_copy_id_status" to table: "loans"
CREATE INDEX "loan_tenant_id_copy_id_status" ON "loans" ("tenant_id", "copy_id", "status");
-- Create index "loan_tenant_id_member_id_status" to table: "loans"
CREATE INDEX "loan_tenant_id_member_id_status" ON "loans" ("tenant_id", "member_id", "status");
-- Create index "loan_tenant_id_status_due_at" to table: "loans"
CREATE INDEX "loan_tenant_id_status_due_at" ON "loans" ("tenant_id", "status", "due_at");
-- Create "loan_policies" table
CREATE TABLE "loan_policies" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "loan_period_days" bigint NOT NULL DEFAULT 14, "max_renewals" bigint NOT NULL DEFAULT 2, "holdable" boolean NOT NULL DEFAULT true, "fine_per_day" numeric(10,4) NOT NULL, "grace_days" bigint NOT NULL DEFAULT 0, "is_default" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "loanpolicy_tenant_id" to table: "loan_policies"
CREATE INDEX "loanpolicy_tenant_id" ON "loan_policies" ("tenant_id");
-- Create index "loanpolicy_tenant_id_name" to table: "loan_policies"
CREATE UNIQUE INDEX "loanpolicy_tenant_id_name" ON "loan_policies" ("tenant_id", "name");
-- Create "members" table
CREATE TABLE "members" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "membership_no" character varying NOT NULL, "user_id" uuid NULL, "crm_contact_id" uuid NULL, "tier_id" uuid NOT NULL, "home_branch_id" uuid NULL, "display_name" character varying NULL, "contact_phone" character varying NULL, "contact_email" character varying NULL, "status" character varying NOT NULL DEFAULT 'ACTIVE', "is_walk_in" boolean NOT NULL DEFAULT false, "joined_at" timestamptz NULL, "expires_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "member_tenant_id" to table: "members"
CREATE INDEX "member_tenant_id" ON "members" ("tenant_id");
-- Create index "member_tenant_id_crm_contact_id" to table: "members"
CREATE INDEX "member_tenant_id_crm_contact_id" ON "members" ("tenant_id", "crm_contact_id");
-- Create index "member_tenant_id_membership_no" to table: "members"
CREATE UNIQUE INDEX "member_tenant_id_membership_no" ON "members" ("tenant_id", "membership_no");
-- Create index "member_tenant_id_status" to table: "members"
CREATE INDEX "member_tenant_id_status" ON "members" ("tenant_id", "status");
-- Create index "member_tenant_id_user_id" to table: "members"
CREATE INDEX "member_tenant_id_user_id" ON "members" ("tenant_id", "user_id");
-- Create "member_tiers" table
CREATE TABLE "member_tiers" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "max_concurrent_loans" bigint NOT NULL DEFAULT 3, "loan_period_days" bigint NOT NULL DEFAULT 14, "max_renewals" bigint NOT NULL DEFAULT 2, "hold_limit" bigint NOT NULL DEFAULT 5, "ebook_concurrent_limit" bigint NOT NULL DEFAULT 3, "daily_fine_rate" numeric(10,4) NOT NULL, "max_fine_before_block" numeric(18,4) NOT NULL, "annual_fee" numeric(18,4) NOT NULL, "is_default" boolean NOT NULL DEFAULT false, PRIMARY KEY ("id"));
-- Create index "membertier_tenant_id" to table: "member_tiers"
CREATE INDEX "membertier_tenant_id" ON "member_tiers" ("tenant_id");
-- Create index "membertier_tenant_id_name" to table: "member_tiers"
CREATE UNIQUE INDEX "membertier_tenant_id_name" ON "member_tiers" ("tenant_id", "name");
-- Create "membership_fees" table
CREATE TABLE "membership_fees" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "member_id" uuid NOT NULL, "period_start" timestamptz NOT NULL, "period_end" timestamptz NOT NULL, "amount" numeric(18,4) NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "treasury_intent_id" character varying NULL, "paid_at" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "membershipfee_tenant_id" to table: "membership_fees"
CREATE INDEX "membershipfee_tenant_id" ON "membership_fees" ("tenant_id");
-- Create index "membershipfee_tenant_id_member_id_status" to table: "membership_fees"
CREATE INDEX "membershipfee_tenant_id_member_id_status" ON "membership_fees" ("tenant_id", "member_id", "status");
-- Create index "membershipfee_tenant_id_treasury_intent_id" to table: "membership_fees"
CREATE INDEX "membershipfee_tenant_id_treasury_intent_id" ON "membership_fees" ("tenant_id", "treasury_intent_id");
-- Create "outbox_events" table
CREATE TABLE "outbox_events" ("id" uuid NOT NULL, "tenant_id" uuid NOT NULL, "aggregate_type" character varying NOT NULL, "aggregate_id" character varying NOT NULL, "event_type" character varying NOT NULL, "payload" jsonb NOT NULL, "status" character varying NOT NULL DEFAULT 'PENDING', "attempts" bigint NOT NULL DEFAULT 0, "last_attempt_at" timestamptz NULL, "published_at" timestamptz NULL, "error_message" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "outboxevent_created_at" to table: "outbox_events"
CREATE INDEX "outboxevent_created_at" ON "outbox_events" ("created_at");
-- Create index "outboxevent_status" to table: "outbox_events"
CREATE INDEX "outboxevent_status" ON "outbox_events" ("status");
-- Create index "outboxevent_tenant_id_status" to table: "outbox_events"
CREATE INDEX "outboxevent_tenant_id_status" ON "outbox_events" ("tenant_id", "status");
-- Create "publishers" table
CREATE TABLE "publishers" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "place" character varying NULL, PRIMARY KEY ("id"));
-- Create index "publisher_tenant_id" to table: "publishers"
CREATE INDEX "publisher_tenant_id" ON "publishers" ("tenant_id");
-- Create index "publisher_tenant_id_name" to table: "publishers"
CREATE INDEX "publisher_tenant_id_name" ON "publishers" ("tenant_id", "name");
-- Create "service_configs" table
CREATE TABLE "service_configs" ("id" uuid NOT NULL, "tenant_id" uuid NULL, "config_key" character varying NOT NULL, "config_value" text NOT NULL, "config_type" character varying NOT NULL DEFAULT 'string', "description" character varying NULL, "is_secret" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "serviceconfig_config_key" to table: "service_configs"
CREATE INDEX "serviceconfig_config_key" ON "service_configs" ("config_key");
-- Create index "serviceconfig_tenant_id_config_key" to table: "service_configs"
CREATE UNIQUE INDEX "serviceconfig_tenant_id_config_key" ON "service_configs" ("tenant_id", "config_key");
-- Create "subjects" table
CREATE TABLE "subjects" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "name" character varying NOT NULL, "code" character varying NULL, "scheme" character varying NOT NULL DEFAULT 'LOCAL', "parent_id" uuid NULL, PRIMARY KEY ("id"));
-- Create index "subject_tenant_id" to table: "subjects"
CREATE INDEX "subject_tenant_id" ON "subjects" ("tenant_id");
-- Create index "subject_tenant_id_name" to table: "subjects"
CREATE INDEX "subject_tenant_id_name" ON "subjects" ("tenant_id", "name");
-- Create index "subject_tenant_id_parent_id" to table: "subjects"
CREATE INDEX "subject_tenant_id_parent_id" ON "subjects" ("tenant_id", "parent_id");
-- Create "tenants" table
CREATE TABLE "tenants" ("id" uuid NOT NULL, "slug" character varying NOT NULL, "name" character varying NULL, "region" character varying NULL, "is_active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "tenant_slug" to table: "tenants"
CREATE UNIQUE INDEX "tenant_slug" ON "tenants" ("slug");
