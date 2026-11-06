-- +goose Up
CREATE TABLE enrichment_matches (
    release_id bigint NOT NULL,
    taxon_id text NOT NULL,
    wikidata_id text NOT NULL,
    match_method text NOT NULL,
    source_url text NOT NULL,
    checked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, taxon_id)
);
CREATE TABLE enrichment_descriptions (
    release_id bigint NOT NULL,
    taxon_id text NOT NULL,
    language text NOT NULL,
    content text NOT NULL,
    source_url text NOT NULL,
    revision_id bigint NOT NULL,
    license text NOT NULL,
    attribution text NOT NULL,
    retrieved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, taxon_id, language),
    FOREIGN KEY (release_id, taxon_id) REFERENCES enrichment_matches ON DELETE CASCADE
);
CREATE TABLE enrichment_images (
    release_id bigint NOT NULL,
    taxon_id text NOT NULL,
    image_url text NOT NULL,
    source_url text NOT NULL,
    creator text NOT NULL,
    license text NOT NULL,
    license_url text NOT NULL,
    attribution text NOT NULL,
    retrieved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, taxon_id, source_url),
    FOREIGN KEY (release_id, taxon_id) REFERENCES enrichment_matches ON DELETE CASCADE
);
CREATE TABLE enrichment_progress (
    release_id bigint NOT NULL,
    taxon_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('matched', 'missing', 'ambiguous', 'failed')),
    detail text NOT NULL DEFAULT '',
    attempted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, taxon_id)
);

-- +goose Down
DROP TABLE enrichment_progress;
DROP TABLE enrichment_images;
DROP TABLE enrichment_descriptions;
DROP TABLE enrichment_matches;
