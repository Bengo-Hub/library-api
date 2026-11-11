-- Modify "library_roles" table
ALTER TABLE "library_roles" ADD COLUMN "tenant_id" uuid NULL;
-- Drop index "libraryrole_name" from table: "library_roles" (role names are now unique per tenant, not across all tenants; the generator does not emit index drops)
DROP INDEX IF EXISTS "libraryrole_name";
-- Create index "libraryrole_name_global" to table: "library_roles"
CREATE UNIQUE INDEX "libraryrole_name_global" ON "library_roles" ("name") WHERE (tenant_id IS NULL);
-- Create index "libraryrole_tenant_id_name" to table: "library_roles"
CREATE UNIQUE INDEX "libraryrole_tenant_id_name" ON "library_roles" ("tenant_id", "name") WHERE (tenant_id IS NOT NULL);
