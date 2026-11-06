-- +goose Up
ALTER TABLE published_plants
    ADD COLUMN review_note text CHECK (review_note IS NULL OR btrim(review_note) <> '');
CREATE UNIQUE INDEX published_plants_scientific_name_idx ON published_plants (scientific_name);
CREATE UNIQUE INDEX published_plants_snapshot_idx ON published_plants (source_snapshot_id);

-- The application role can execute this narrow operation without receiving
-- INSERT privilege on the public read table. Operator approval is checked by
-- the HTTP layer; this function verifies the source evidence again.
-- +goose StatementBegin
CREATE FUNCTION public.publish_plant(
    p_slug text,
    p_source_snapshot_id bigint,
    p_operator text,
    p_review_note text
) RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = ''
AS $$
DECLARE
    v_scientific_name text;
    v_plant_id bigint;
BEGIN
    IF p_slug IS NULL OR length(p_slug) > 80
       OR p_slug !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
       OR p_source_snapshot_id IS NULL OR p_source_snapshot_id <= 0
       OR p_operator IS NULL OR btrim(p_operator) = '' OR length(p_operator) > 100
       OR p_review_note IS NULL OR btrim(p_review_note) = '' OR length(p_review_note) > 1000 THEN
        RAISE EXCEPTION 'invalid publication request' USING ERRCODE = '22023';
    END IF;

    SELECT s.normalized_payload->>'canonical_name'
      INTO v_scientific_name
    FROM public.source_snapshots AS s
    JOIN public.source_records AS r ON r.id = s.source_record_id AND r.source_id = s.source_id
    JOIN public.sources AS source ON source.id = s.source_id
    WHERE s.id = p_source_snapshot_id
      AND s.id = (SELECT max(latest.id) FROM public.source_snapshots AS latest
                  WHERE latest.source_record_id = s.source_record_id)
      AND source.code = 'gbif-backbone'
      AND source.adapter_code = 'gbif-backbone-v1'
      AND source.adapter_config->>'dataset_key' = 'd7dddbf4-2cf0-4f39-9b2a-bb099caae36c'
      AND source.access_status = 'approved'
      AND source.reuse_status = 'approved'
      AND source.storage_policy = 'full_json'
      AND source.approved_at IS NOT NULL
      AND source.approved_by_operator IS NOT NULL
      AND r.record_type = 'taxon'
      AND s.evidence_status = 'captured'
      AND s.normalized_payload->>'rank' = 'SPECIES'
      AND s.normalized_payload->>'dataset_key' = 'd7dddbf4-2cf0-4f39-9b2a-bb099caae36c'
      AND s.normalized_payload->>'taxonomic_status' = 'ACCEPTED'
      AND s.normalized_payload->'classification'->>'kingdom' = 'Plantae'
      AND r.external_id = s.normalized_payload->>'external_id'
      AND trim(trailing '/' FROM replace(s.license, 'http://', 'https://')) IN (
          'https://creativecommons.org/licenses/by/4.0',
          'https://creativecommons.org/licenses/by/4.0/legalcode'
      );

    IF NOT FOUND OR v_scientific_name IS NULL OR btrim(v_scientific_name) = '' THEN
        RAISE EXCEPTION 'source snapshot is not eligible for publication' USING ERRCODE = '22023';
    END IF;

    INSERT INTO public.published_plants
        (slug, scientific_name, source_snapshot_id, published_by_operator, review_note)
    VALUES (p_slug, v_scientific_name, p_source_snapshot_id, p_operator, p_review_note)
    RETURNING id INTO v_plant_id;
    RETURN v_plant_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION public.publish_plant(text, bigint, text, text) FROM PUBLIC;

-- +goose Down
DROP FUNCTION public.publish_plant(text, bigint, text, text);
DROP INDEX published_plants_snapshot_idx;
DROP INDEX published_plants_scientific_name_idx;
ALTER TABLE published_plants DROP COLUMN review_note;
