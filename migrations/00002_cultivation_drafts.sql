-- +goose Up
CREATE TABLE cultivation_plants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    gbif_source_record_id bigint NOT NULL UNIQUE REFERENCES source_records(id) ON DELETE RESTRICT,
    slug text NOT NULL UNIQUE CHECK (btrim(slug) <> ''),
    scientific_name text NOT NULL UNIQUE CHECK (btrim(scientific_name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cultivation_sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_key text NOT NULL UNIQUE CHECK (btrim(source_key) <> ''),
    title text NOT NULL CHECK (btrim(title) <> ''),
    publisher text NOT NULL CHECK (btrim(publisher) <> ''),
    url text NOT NULL UNIQUE CHECK (url LIKE 'https://%'),
    accessed_on date NOT NULL,
    rights_note text NOT NULL CHECK (btrim(rights_note) <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cultivation_claims (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plant_id bigint NOT NULL REFERENCES cultivation_plants(id) ON DELETE RESTRICT,
    claim_key text NOT NULL CHECK (btrim(claim_key) <> ''),
    topic text NOT NULL CHECK (topic IN ('site','climate','soil','water','propagation','planting','spacing','pruning','problems')),
    text_th text NOT NULL CHECK (btrim(text_th) <> ''),
    text_en text NOT NULL CHECK (btrim(text_en) <> ''),
    context_th text,
    context_en text,
    review_status text NOT NULL DEFAULT 'pending_review' CHECK (review_status = 'pending_review'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (plant_id, claim_key),
    CHECK ((context_th IS NULL) = (context_en IS NULL)),
    CHECK (context_th IS NULL OR (btrim(context_th) <> '' AND btrim(context_en) <> ''))
);
CREATE INDEX cultivation_claims_plant_topic_idx ON cultivation_claims (plant_id, topic);

CREATE TABLE cultivation_claim_sources (
    claim_id bigint NOT NULL REFERENCES cultivation_claims(id) ON DELETE CASCADE,
    source_id bigint NOT NULL REFERENCES cultivation_sources(id) ON DELETE RESTRICT,
    locator text NOT NULL CHECK (btrim(locator) <> ''),
    PRIMARY KEY (claim_id, source_id)
);
CREATE INDEX cultivation_claim_sources_source_idx ON cultivation_claim_sources (source_id);

-- +goose Down
DROP TABLE cultivation_claim_sources;
DROP TABLE cultivation_claims;
DROP TABLE cultivation_sources;
DROP TABLE cultivation_plants;
