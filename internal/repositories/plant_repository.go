package repositories

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"treewiki/wiki_backend/internal/models"
)

type PlantRepository struct{ pool *pgxpool.Pool }

func NewPlantRepository(pool *pgxpool.Pool) *PlantRepository { return &PlantRepository{pool: pool} }

func decodeCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	if len(cursor) > 1024 {
		return "", "", errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	var parts []string
	if err = json.Unmarshal(raw, &parts); err != nil || len(parts) != 2 {
		return "", "", errors.New("invalid cursor")
	}
	return parts[0], parts[1], nil
}
func encodeCursor(name, id string) string {
	raw, _ := json.Marshal([]string{strings.ToLower(name), id})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func ValidCursor(cursor string) bool {
	if _, _, err := decodeCursor(cursor); err == nil {
		return true
	}
	_, _, _, err := decodeRankedCursor(cursor)
	return err == nil
}

func decodeRankedCursor(cursor string) (int, string, string, error) {
	if cursor == "" {
		return 3, "", "", nil
	}
	if len(cursor) > 1024 {
		return 0, "", "", errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", "", errors.New("invalid cursor")
	}
	var parts []string
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 3 {
		return 0, "", "", errors.New("invalid cursor")
	}
	score, err := strconv.Atoi(parts[0])
	if err != nil || score < 0 || score > 2 || parts[1] == "" || parts[2] == "" {
		return 0, "", "", errors.New("invalid cursor")
	}
	return score, parts[1], parts[2], nil
}

func encodeRankedCursor(score int, name, id string) string {
	raw, _ := json.Marshal([]string{strconv.Itoa(score), strings.ToLower(name), id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

const namesSQL = `COALESCE((SELECT jsonb_agg(jsonb_build_object('name',n.name,'language',n.language)
ORDER BY n.preferred DESC,n.name) FROM catalog_names n
WHERE n.release_id=t.release_id AND n.taxon_id=t.taxon_id),'[]'::jsonb)`

func scanTaxon(rows pgx.Rows) (models.Taxon, error) {
	var taxon models.Taxon
	var names []byte
	err := rows.Scan(&taxon.ID, &taxon.ParentID, &taxon.ScientificName, &taxon.Authorship, &taxon.Rank, &taxon.Status, &names)
	if err != nil {
		return taxon, err
	}
	err = json.Unmarshal(names, &taxon.CommonNames)
	if taxon.CommonNames == nil {
		taxon.CommonNames = []models.Name{}
	}
	return taxon, err
}

func (r *PlantRepository) List(ctx context.Context, q, cursor string, enriched bool) (models.TaxonPage, error) {
	if enriched {
		return r.listRanked(ctx, q, cursor)
	}
	name, id, err := decodeCursor(cursor)
	if err != nil {
		return models.TaxonPage{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,`+namesSQL+`,
CASE WHEN $4 THEN COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'language',d.language,'content',left(d.content,280),'source_url',d.source_url,
 'revision_id',d.revision_id,'license',d.license,'attribution',d.attribution,
 'retrieved_at',d.retrieved_at,'match_method',m.match_method) ORDER BY d.language)
 FROM enrichment_descriptions d JOIN enrichment_matches m USING(release_id,taxon_id)
 WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id),'[]'::jsonb) ELSE '[]'::jsonb END,
CASE WHEN $4 THEN COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'image_url',i.image_url,'source_url',i.source_url,'creator',i.creator,'license',i.license,
 'license_url',i.license_url,'attribution',i.attribution,'retrieved_at',i.retrieved_at,
 'match_method',m.match_method) ORDER BY i.source_url)
 FROM enrichment_images i JOIN enrichment_matches m USING(release_id,taxon_id)
 WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id),'[]'::jsonb) ELSE '[]'::jsonb END
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active
WHERE t.rank='species' AND t.status='accepted'
AND (NOT $4 OR EXISTS (SELECT 1 FROM enrichment_descriptions d WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id)
 OR EXISTS (SELECT 1 FROM enrichment_images i WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id))
AND ($1='' OR t.scientific_name ILIKE '%'||$1||'%' OR EXISTS (
 SELECT 1 FROM catalog_taxa synonym WHERE synonym.release_id=t.release_id
 AND synonym.accepted_taxon_id=t.taxon_id AND synonym.scientific_name ILIKE '%'||$1||'%') OR EXISTS (
 SELECT 1 FROM catalog_names n WHERE n.release_id=t.release_id AND n.taxon_id=t.taxon_id
 AND n.name ILIKE '%'||$1||'%'))
AND ($2='' OR (lower(t.scientific_name),t.taxon_id)>($2,$3))
ORDER BY lower(t.scientific_name),t.taxon_id LIMIT 51`, q, name, id, enriched)
	if err != nil {
		return models.TaxonPage{}, err
	}
	defer rows.Close()
	page := models.TaxonPage{Taxa: []models.Taxon{}}
	for rows.Next() {
		var taxon models.Taxon
		var names, descriptions, images []byte
		err := rows.Scan(&taxon.ID, &taxon.ParentID, &taxon.ScientificName, &taxon.Authorship, &taxon.Rank, &taxon.Status, &names, &descriptions, &images)
		if err != nil {
			return page, err
		}
		if err = json.Unmarshal(names, &taxon.CommonNames); err != nil {
			return page, err
		}
		if err = json.Unmarshal(descriptions, &taxon.Descriptions); err != nil {
			return page, err
		}
		if err = json.Unmarshal(images, &taxon.Images); err != nil {
			return page, err
		}
		if taxon.CommonNames == nil {
			taxon.CommonNames = []models.Name{}
		}
		page.Taxa = append(page.Taxa, taxon)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Taxa) > 50 {
		page.Taxa = page.Taxa[:50]
		last := page.Taxa[49]
		next := encodeCursor(last.ScientificName, last.ID)
		page.NextCursor = &next
	}
	return page, nil
}

func (r *PlantRepository) listRanked(ctx context.Context, q, cursor string) (models.TaxonPage, error) {
	score, name, id, err := decodeRankedCursor(cursor)
	if err != nil {
		// Older links contain an alphabetical cursor. Find that plant's current
		// score so existing bookmarked pagination links continue to work.
		name, id, err = decodeCursor(cursor)
		if err != nil {
			return models.TaxonPage{}, err
		}
		err = r.pool.QueryRow(ctx, `SELECT
  (EXISTS (SELECT 1 FROM enrichment_images i WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id))::int +
  (EXISTS (SELECT 1 FROM enrichment_descriptions d WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id AND cardinality(array_remove(regexp_split_to_array(d.content, '[[:space:]]+'), ''))>100))::int
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active
WHERE t.taxon_id=$1 AND lower(t.scientific_name)=$2 AND t.rank='species' AND t.status='accepted'`, id, name).Scan(&score)
		if err != nil {
			return models.TaxonPage{}, errors.New("invalid cursor")
		}
	}
	rows, err := r.pool.Query(ctx, `WITH ranked AS (
SELECT t.*,
  (EXISTS (SELECT 1 FROM enrichment_images i WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id))::int +
  (EXISTS (SELECT 1 FROM enrichment_descriptions d WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id AND cardinality(array_remove(regexp_split_to_array(d.content, '[[:space:]]+'), ''))>100))::int AS score
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active
WHERE t.rank='species' AND t.status='accepted'
AND (EXISTS (SELECT 1 FROM enrichment_descriptions d WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id)
 OR EXISTS (SELECT 1 FROM enrichment_images i WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id))
AND ($1='' OR t.scientific_name ILIKE '%'||$1||'%' OR EXISTS (
 SELECT 1 FROM catalog_taxa synonym WHERE synonym.release_id=t.release_id
 AND synonym.accepted_taxon_id=t.taxon_id AND synonym.scientific_name ILIKE '%'||$1||'%') OR EXISTS (
 SELECT 1 FROM catalog_names n WHERE n.release_id=t.release_id AND n.taxon_id=t.taxon_id
 AND n.name ILIKE '%'||$1||'%'))
)
SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,`+namesSQL+`,
COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'language',d.language,'content',left(d.content,280),'source_url',d.source_url,
 'revision_id',d.revision_id,'license',d.license,'attribution',d.attribution,
 'retrieved_at',d.retrieved_at,'match_method',m.match_method) ORDER BY d.language)
 FROM enrichment_descriptions d JOIN enrichment_matches m USING(release_id,taxon_id)
 WHERE d.release_id=t.release_id AND d.taxon_id=t.taxon_id),'[]'::jsonb),
COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'image_url',i.image_url,'source_url',i.source_url,'creator',i.creator,'license',i.license,
 'license_url',i.license_url,'attribution',i.attribution,'retrieved_at',i.retrieved_at,
 'match_method',m.match_method) ORDER BY i.source_url)
 FROM enrichment_images i JOIN enrichment_matches m USING(release_id,taxon_id)
 WHERE i.release_id=t.release_id AND i.taxon_id=t.taxon_id),'[]'::jsonb), t.score
FROM ranked t
WHERE ($2='' OR t.score<$4 OR (t.score=$4 AND (lower(t.scientific_name),t.taxon_id)>($2,$3)))
ORDER BY t.score DESC,lower(t.scientific_name),t.taxon_id LIMIT 51`, q, name, id, score)
	if err != nil {
		return models.TaxonPage{}, err
	}
	defer rows.Close()
	page := models.TaxonPage{Taxa: []models.Taxon{}}
	var scores []int
	for rows.Next() {
		var taxon models.Taxon
		var names, descriptions, images []byte
		var itemScore int
		if err = rows.Scan(&taxon.ID, &taxon.ParentID, &taxon.ScientificName, &taxon.Authorship, &taxon.Rank, &taxon.Status, &names, &descriptions, &images, &itemScore); err != nil {
			return page, err
		}
		if err = json.Unmarshal(names, &taxon.CommonNames); err != nil {
			return page, err
		}
		if err = json.Unmarshal(descriptions, &taxon.Descriptions); err != nil {
			return page, err
		}
		if err = json.Unmarshal(images, &taxon.Images); err != nil {
			return page, err
		}
		if taxon.CommonNames == nil {
			taxon.CommonNames = []models.Name{}
		}
		page.Taxa = append(page.Taxa, taxon)
		scores = append(scores, itemScore)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Taxa) > 50 {
		page.Taxa = page.Taxa[:50]
		last := page.Taxa[49]
		next := encodeRankedCursor(scores[49], last.ScientificName, last.ID)
		page.NextCursor = &next
	}
	return page, nil
}

func (r *PlantRepository) Children(ctx context.Context, parent, cursor string) (models.TaxonPage, error) {
	name, id, err := decodeCursor(cursor)
	if err != nil {
		return models.TaxonPage{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,`+namesSQL+`
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active
WHERE t.parent_id=$1 AND t.status IN ('accepted','provisionally accepted')
AND ($2='' OR (lower(t.scientific_name),t.taxon_id)>($2,$3))
ORDER BY lower(t.scientific_name),t.taxon_id LIMIT 51`, parent, name, id)
	if err != nil {
		return models.TaxonPage{}, err
	}
	defer rows.Close()
	page := models.TaxonPage{Taxa: []models.Taxon{}}
	for rows.Next() {
		taxon, err := scanTaxon(rows)
		if err != nil {
			return page, err
		}
		page.Taxa = append(page.Taxa, taxon)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Taxa) > 50 {
		page.Taxa = page.Taxa[:50]
		last := page.Taxa[49]
		next := encodeCursor(last.ScientificName, last.ID)
		page.NextCursor = &next
	}
	return page, nil
}

func (r *PlantRepository) Root(ctx context.Context) (models.Taxon, error) {
	return r.one(ctx, "t.scientific_name=$1 AND t.rank='kingdom'", "Plantae")
}
func (r *PlantRepository) One(ctx context.Context, id string) (models.Taxon, error) {
	return r.one(ctx, "t.taxon_id=$1", id)
}
func (r *PlantRepository) one(ctx context.Context, predicate, id string) (models.Taxon, error) {
	rows, err := r.pool.Query(ctx, `SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,`+namesSQL+`
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active WHERE `+predicate+` LIMIT 1`, id)
	if err != nil {
		return models.Taxon{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.Taxon{}, err
		}
		return models.Taxon{}, pgx.ErrNoRows
	}
	return scanTaxon(rows)
}

func (r *PlantRepository) Detail(ctx context.Context, id string) (models.PlantDetail, error) {
	taxon, err := r.One(ctx, id)
	if err != nil {
		return models.PlantDetail{}, err
	}
	detail := models.PlantDetail{Taxon: taxon, Classification: []models.Taxon{}, Synonyms: []models.Taxon{}, Descriptions: []models.Description{}, Images: []models.Image{}}
	rows, err := r.pool.Query(ctx, `WITH RECURSIVE ancestors AS (
SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,t.release_id,0 AS depth
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active WHERE t.taxon_id=$1
UNION ALL
SELECT p.taxon_id,p.parent_id,p.scientific_name,p.authorship,p.rank,p.status,p.release_id,a.depth+1
FROM catalog_taxa p JOIN ancestors a ON p.release_id=a.release_id AND p.taxon_id=a.parent_id WHERE a.depth<32)
SELECT a.taxon_id,a.parent_id,a.scientific_name,a.authorship,a.rank,a.status,'[]'::jsonb
FROM ancestors a ORDER BY a.depth DESC`, id)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		item, err := scanTaxon(rows)
		if err != nil {
			rows.Close()
			return detail, err
		}
		detail.Classification = append(detail.Classification, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return detail, err
	}
	rows.Close()
	rows, err = r.pool.Query(ctx, `SELECT t.taxon_id,t.parent_id,t.scientific_name,t.authorship,t.rank,t.status,`+namesSQL+`
FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active
WHERE t.accepted_taxon_id=$1 ORDER BY t.scientific_name LIMIT 100`, id)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		item, err := scanTaxon(rows)
		if err != nil {
			rows.Close()
			return detail, err
		}
		detail.Synonyms = append(detail.Synonyms, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return detail, err
	}
	rows.Close()
	if err := r.pool.QueryRow(ctx, `SELECT title,doi,license,citation FROM catalog_releases WHERE active`).Scan(
		&detail.Source.Title, &detail.Source.DOI, &detail.Source.License, &detail.Source.Citation); err != nil {
		return detail, err
	}
	rows, err = r.pool.Query(ctx, `SELECT d.language,d.content,d.source_url,d.revision_id,d.license,d.attribution,d.retrieved_at,m.match_method
FROM enrichment_descriptions d JOIN enrichment_matches m USING (release_id,taxon_id)
JOIN catalog_releases r ON r.id=d.release_id AND r.active WHERE d.taxon_id=$1 ORDER BY d.language`, id)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		var d models.Description
		if err = rows.Scan(&d.Language, &d.Content, &d.SourceURL, &d.RevisionID, &d.License, &d.Attribution, &d.RetrievedAt, &d.MatchMethod); err != nil {
			rows.Close()
			return detail, err
		}
		detail.Descriptions = append(detail.Descriptions, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return detail, err
	}
	rows.Close()
	rows, err = r.pool.Query(ctx, `SELECT i.image_url,i.source_url,i.creator,i.license,i.license_url,i.attribution,i.retrieved_at,m.match_method
FROM enrichment_images i JOIN enrichment_matches m USING (release_id,taxon_id)
JOIN catalog_releases r ON r.id=i.release_id AND r.active WHERE i.taxon_id=$1 ORDER BY i.source_url`, id)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		var im models.Image
		if err = rows.Scan(&im.ImageURL, &im.SourceURL, &im.Creator, &im.License, &im.LicenseURL, &im.Attribution, &im.RetrievedAt, &im.MatchMethod); err != nil {
			rows.Close()
			return detail, err
		}
		detail.Images = append(detail.Images, im)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return detail, err
	}
	rows.Close()
	return detail, nil
}
