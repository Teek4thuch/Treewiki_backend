-- +goose Up
DROP FUNCTION IF EXISTS public.publish_plant(text, bigint, text, text);
DROP TABLE IF EXISTS cultivation_claim_sources;
DROP TABLE IF EXISTS cultivation_claims;
DROP TABLE IF EXISTS cultivation_sources;
DROP TABLE IF EXISTS cultivation_plants;
DROP TABLE IF EXISTS published_plants;
DROP TABLE IF EXISTS source_snapshots;
DROP FUNCTION IF EXISTS reject_snapshot_mutation();
DROP TABLE IF EXISTS source_records;
DROP TABLE IF EXISTS import_runs;
DROP TABLE IF EXISTS sources;

CREATE TABLE catalog_releases (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dataset_key text NOT NULL,
    title text NOT NULL,
    doi text NOT NULL,
    license text NOT NULL,
    citation text NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    active boolean NOT NULL DEFAULT false,
    taxon_count bigint NOT NULL DEFAULT 0,
    species_count bigint NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX catalog_one_active_release_idx ON catalog_releases (active) WHERE active;

CREATE TABLE catalog_taxa (
    release_id bigint NOT NULL REFERENCES catalog_releases(id) ON DELETE CASCADE,
    taxon_id text NOT NULL,
    parent_id text,
    accepted_taxon_id text,
    scientific_name text NOT NULL,
    authorship text,
    rank text NOT NULL,
    status text NOT NULL,
    PRIMARY KEY (release_id, taxon_id)
);
CREATE INDEX catalog_taxa_parent_idx ON catalog_taxa (release_id, parent_id, scientific_name);
CREATE INDEX catalog_taxa_accepted_idx ON catalog_taxa (release_id, accepted_taxon_id);
CREATE INDEX catalog_taxa_species_idx ON catalog_taxa (release_id, lower(scientific_name), taxon_id)
    WHERE rank = 'species' AND status = 'accepted';
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX catalog_taxa_name_search_idx ON catalog_taxa USING gin (scientific_name gin_trgm_ops);

CREATE TABLE catalog_names (
    release_id bigint NOT NULL,
    taxon_id text NOT NULL,
    name text NOT NULL,
    language text NOT NULL,
    preferred boolean NOT NULL DEFAULT false,
    PRIMARY KEY (release_id, taxon_id, language, name),
    FOREIGN KEY (release_id, taxon_id) REFERENCES catalog_taxa(release_id, taxon_id) ON DELETE CASCADE
);
CREATE INDEX catalog_names_lookup_idx ON catalog_names (release_id, taxon_id);
CREATE INDEX catalog_names_search_idx ON catalog_names USING gin (name gin_trgm_ops);

-- +goose Down
DROP TABLE catalog_names;
DROP TABLE catalog_taxa;
DROP TABLE catalog_releases;
