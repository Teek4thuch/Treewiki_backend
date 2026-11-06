-- +goose Up
-- Public reads use this separate catalog table. Import staging never writes it.
CREATE TABLE published_plants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug text NOT NULL UNIQUE CHECK (btrim(slug) <> ''),
    scientific_name text NOT NULL CHECK (btrim(scientific_name) <> ''),
    source_snapshot_id bigint NOT NULL REFERENCES source_snapshots(id) ON DELETE RESTRICT,
    published_by_operator text NOT NULL CHECK (btrim(published_by_operator) <> ''),
    published_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX published_plants_source_snapshot_idx ON published_plants (source_snapshot_id);

-- +goose Down
DROP TABLE published_plants;
