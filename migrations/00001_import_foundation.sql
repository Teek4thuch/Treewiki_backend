-- +goose Up
CREATE TABLE sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code text NOT NULL UNIQUE CHECK (code <> ''),
    name text NOT NULL,
    publisher text NOT NULL,
    homepage_url text NOT NULL,
    terms_url text,
    allowed_hosts text[] NOT NULL,
    adapter_code text NOT NULL,
    adapter_config jsonb NOT NULL CHECK (jsonb_typeof(adapter_config) = 'object'),
    access_status text NOT NULL DEFAULT 'unknown' CHECK (access_status IN ('unknown','approved','denied')),
    reuse_status text NOT NULL DEFAULT 'unknown' CHECK (reuse_status IN ('unknown','approved','denied')),
    storage_policy text NOT NULL DEFAULT 'unknown' CHECK (storage_policy IN ('unknown','metadata_only','full_json')),
    approval_notes text,
    approved_by_operator text,
    approved_at timestamptz,
    import_enabled boolean NOT NULL DEFAULT false,
    schedule_enabled boolean NOT NULL DEFAULT false,
    schedule_expression text NOT NULL DEFAULT '0 3 1 * *',
    schedule_timezone text NOT NULL DEFAULT 'Asia/Bangkok',
    next_run_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT import_enabled OR (access_status = 'approved' AND reuse_status = 'approved'
        AND storage_policy = 'full_json' AND approved_at IS NOT NULL AND approved_by_operator IS NOT NULL)),
    CHECK (NOT schedule_enabled OR (import_enabled AND next_run_at IS NOT NULL)),
    CHECK (schedule_enabled OR next_run_at IS NULL)
);
CREATE INDEX sources_due_idx ON sources (next_run_at, id) WHERE schedule_enabled AND import_enabled;

CREATE TABLE import_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id bigint NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    trigger_type text NOT NULL CHECK (trigger_type IN ('manual','scheduled','retry','recovery')),
    requested_by_operator text,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','partial','failed','cancelled')),
    started_at timestamptz,
    finished_at timestamptz,
    checkpoint jsonb NOT NULL CHECK (jsonb_typeof(checkpoint) = 'object'),
    counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(counts) = 'object'),
    error_summary text,
    retry_of_run_id bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, source_id),
    FOREIGN KEY (retry_of_run_id, source_id) REFERENCES import_runs(id, source_id) ON DELETE RESTRICT,
    CHECK (retry_of_run_id IS NULL OR retry_of_run_id <> id),
    CHECK ((status IN ('succeeded','partial','failed','cancelled')) = (finished_at IS NOT NULL)),
    CHECK (status <> 'running' OR started_at IS NOT NULL)
);
CREATE UNIQUE INDEX import_runs_one_active_source_idx ON import_runs (source_id) WHERE status IN ('queued','running');
CREATE INDEX import_runs_source_history_idx ON import_runs (source_id, id DESC);
CREATE INDEX import_runs_pending_idx ON import_runs (id) WHERE status IN ('queued','running');
CREATE INDEX import_runs_retry_idx ON import_runs (retry_of_run_id, source_id) WHERE retry_of_run_id IS NOT NULL;

CREATE TABLE source_records (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id bigint NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    record_key text NOT NULL CHECK (record_key <> ''),
    external_id text NOT NULL,
    canonical_url text NOT NULL,
    record_type text NOT NULL CHECK (record_type = 'taxon'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, record_key),
    UNIQUE (id, source_id)
);

CREATE TABLE source_snapshots (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id bigint NOT NULL,
    source_record_id bigint NOT NULL,
    import_run_id bigint NOT NULL,
    retrieved_at timestamptz NOT NULL,
    source_published_at timestamptz,
    source_version text,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    raw_payload jsonb NOT NULL CHECK (jsonb_typeof(raw_payload) = 'object'),
    normalized_payload jsonb NOT NULL CHECK (jsonb_typeof(normalized_payload) = 'object'),
    license text NOT NULL CHECK (license <> ''),
    attribution text NOT NULL CHECK (attribution <> ''),
    rights_metadata jsonb NOT NULL CHECK (jsonb_typeof(rights_metadata) = 'object'),
    extractor_version text NOT NULL,
    evidence_status text NOT NULL CHECK (evidence_status = 'captured'),
    FOREIGN KEY (source_record_id, source_id) REFERENCES source_records(id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (import_run_id, source_id) REFERENCES import_runs(id, source_id) ON DELETE RESTRICT,
    UNIQUE (import_run_id, source_record_id)
);
CREATE INDEX source_snapshots_record_history_idx ON source_snapshots (source_record_id, id DESC);
CREATE INDEX source_snapshots_source_idx ON source_snapshots (source_id);

-- Append-only even for the operator role; rights removal needs an explicit later migration/process.
-- +goose StatementBegin
CREATE FUNCTION reject_snapshot_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'source snapshots are append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER source_snapshots_immutable BEFORE UPDATE OR DELETE ON source_snapshots
FOR EACH ROW EXECUTE FUNCTION reject_snapshot_mutation();
CREATE TRIGGER source_snapshots_no_truncate BEFORE TRUNCATE ON source_snapshots
FOR EACH STATEMENT EXECUTE FUNCTION reject_snapshot_mutation();

-- +goose Down
DROP TABLE source_snapshots;
DROP FUNCTION reject_snapshot_mutation();
DROP TABLE source_records;
DROP TABLE import_runs;
DROP TABLE sources;
