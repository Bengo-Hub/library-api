-- Modify "bib_records" table
ALTER TABLE "bib_records" ADD COLUMN "publication_place" character varying NULL, ADD COLUMN "cover_back_image_url" character varying NULL, ADD COLUMN "subjects" jsonb NULL, ADD COLUMN "other_isbns" jsonb NULL;
