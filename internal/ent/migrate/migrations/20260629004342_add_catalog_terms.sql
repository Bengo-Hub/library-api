-- Create "catalog_terms" table
CREATE TABLE "catalog_terms" ("id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "tenant_id" uuid NOT NULL, "kind" character varying NOT NULL, "value" character varying NOT NULL, PRIMARY KEY ("id"));
-- Create index "catalogterm_tenant_id" to table: "catalog_terms"
CREATE INDEX "catalogterm_tenant_id" ON "catalog_terms" ("tenant_id");
-- Create index "catalogterm_tenant_id_kind" to table: "catalog_terms"
CREATE INDEX "catalogterm_tenant_id_kind" ON "catalog_terms" ("tenant_id", "kind");
-- Create index "catalogterm_tenant_id_kind_value" to table: "catalog_terms"
CREATE UNIQUE INDEX "catalogterm_tenant_id_kind_value" ON "catalog_terms" ("tenant_id", "kind", "value");
