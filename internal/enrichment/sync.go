package enrichment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct {
	Limit   int
	After   string
	Refresh bool
}
type Result struct {
	ReleaseID    int64  `json:"release_id"`
	Processed    int    `json:"processed"`
	Matched      int    `json:"matched"`
	Descriptions int    `json:"descriptions"`
	Images       int    `json:"images"`
	Missing      int    `json:"missing"`
	Ambiguous    int    `json:"ambiguous"`
	Failed       int    `json:"failed"`
	LastID       string `json:"last_id"`
}
type species struct{ ID, Name, Parent string }

func Sync(ctx context.Context, databaseURL, userAgent string, options Options) (Result, error) {
	if databaseURL == "" {
		return Result{}, errors.New("MIGRATION_DATABASE_URL is required")
	}
	if !strings.Contains(userAgent, "@") && !strings.Contains(userAgent, "https://") {
		return Result{}, errors.New("WIKIMEDIA_USER_AGENT must identify the application and include a contact URL or email")
	}
	if strings.Contains(userAgent, "example.com") {
		return Result{}, errors.New("WIKIMEDIA_USER_AGENT must use a real contact")
	}
	if options.Limit < 1 || options.Limit > 1000 {
		return Result{}, errors.New("limit must be between 1 and 1000")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return Result{}, err
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(829514117)").Scan(&locked); err != nil {
		return Result{}, err
	}
	if !locked {
		return Result{}, errors.New("another enrichment sync is running")
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(829514117)")
	var result Result
	if err = conn.QueryRow(ctx, "SELECT id FROM catalog_releases WHERE active").Scan(&result.ReleaseID); err != nil {
		return result, fmt.Errorf("active catalogue: %w", err)
	}
	rows, err := conn.Query(ctx, `SELECT t.taxon_id,t.scientific_name,COALESCE(parent.scientific_name,'')
FROM catalog_taxa t
LEFT JOIN catalog_taxa parent ON parent.release_id=t.release_id AND parent.taxon_id=t.parent_id
LEFT JOIN enrichment_progress p ON p.release_id=t.release_id AND p.taxon_id=t.taxon_id
WHERE t.release_id=$1 AND t.rank='species' AND t.status='accepted' AND t.taxon_id>$2
AND EXISTS (SELECT 1 FROM catalog_names n WHERE n.release_id=t.release_id AND n.taxon_id=t.taxon_id)
AND ($3 OR p.taxon_id IS NULL OR p.status='failed')
ORDER BY t.taxon_id LIMIT $4`, result.ReleaseID, options.After, options.Refresh, options.Limit)
	if err != nil {
		return result, err
	}
	var batch []species
	for rows.Next() {
		var item species
		if err = rows.Scan(&item.ID, &item.Name, &item.Parent); err != nil {
			rows.Close()
			return result, err
		}
		batch = append(batch, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	client := newAPI(userAgent)
	for _, item := range batch {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		status, descriptions, images, processErr := process(ctx, client, pool, result.ReleaseID, item)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if processErr != nil {
			status = "failed"
		}
		detail := ""
		if processErr != nil {
			detail = processErr.Error()
			if len(detail) > 2000 {
				detail = detail[:2000]
			}
		}
		_, err = conn.Exec(ctx, `INSERT INTO enrichment_progress(release_id,taxon_id,status,detail) VALUES($1,$2,$3,$4)
ON CONFLICT(release_id,taxon_id) DO UPDATE SET status=excluded.status,detail=excluded.detail,attempted_at=now()`, result.ReleaseID, item.ID, status, detail)
		if err != nil {
			return result, err
		}
		result.Processed++
		result.LastID = item.ID
		result.Descriptions += descriptions
		result.Images += images
		switch status {
		case "matched":
			result.Matched++
		case "missing":
			result.Missing++
		case "ambiguous":
			result.Ambiguous++
		case "failed":
			result.Failed++
		}
	}
	return result, nil
}

func process(ctx context.Context, client *api, pool *pgxpool.Pool, releaseID int64, item species) (string, int, int, error) {
	e, status, err := client.match(ctx, item.Name, item.Parent)
	if err != nil {
		return "", 0, 0, err
	}
	if status != "matched" {
		// A successful recheck invalidates any earlier match for this release.
		_, err = pool.Exec(ctx, "DELETE FROM enrichment_matches WHERE release_id=$1 AND taxon_id=$2", releaseID, item.ID)
		return status, 0, 0, err
	}
	var descriptions []description
	for _, lang := range []string{"th", "en"} {
		link, ok := e.Sitelinks[lang+"wiki"]
		if !ok || link.Title == "" {
			continue
		}
		d, err := client.article(ctx, lang, link.Title)
		if err != nil {
			return "", 0, 0, err
		}
		if d != nil {
			descriptions = append(descriptions, *d)
		}
	}
	var images []image
	if filenames := stringsOf(e, "P18"); len(filenames) > 0 {
		im, err := client.photo(ctx, filenames[0])
		if err != nil {
			return "", 0, 0, err
		}
		if im != nil {
			images = append(images, *im)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	defer tx.Rollback(context.Background())
	var active bool
	if err = tx.QueryRow(ctx, "SELECT active FROM catalog_releases WHERE id=$1 FOR SHARE", releaseID).Scan(&active); err != nil {
		return "", 0, 0, err
	}
	if !active {
		return "", 0, 0, errors.New("catalogue release changed; restart the import")
	}
	_, err = tx.Exec(ctx, `INSERT INTO enrichment_matches(release_id,taxon_id,wikidata_id,match_method,source_url)
VALUES($1,$2,$3,'scientific_name_rank_parent',$4) ON CONFLICT(release_id,taxon_id)
DO UPDATE SET wikidata_id=excluded.wikidata_id,match_method=excluded.match_method,source_url=excluded.source_url,checked_at=now()`, releaseID, item.ID, e.ID, "https://www.wikidata.org/wiki/"+e.ID)
	if err != nil {
		return "", 0, 0, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM enrichment_descriptions WHERE release_id=$1 AND taxon_id=$2", releaseID, item.ID); err != nil {
		return "", 0, 0, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM enrichment_images WHERE release_id=$1 AND taxon_id=$2", releaseID, item.ID); err != nil {
		return "", 0, 0, err
	}
	for _, d := range descriptions {
		_, err = tx.Exec(ctx, `INSERT INTO enrichment_descriptions(release_id,taxon_id,language,content,source_url,revision_id,license,attribution)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, releaseID, item.ID, d.Language, d.Content, d.SourceURL, d.RevisionID, d.License, d.Attribution)
		if err != nil {
			return "", 0, 0, err
		}
	}
	for _, im := range images {
		_, err = tx.Exec(ctx, `INSERT INTO enrichment_images(release_id,taxon_id,image_url,source_url,creator,license,license_url,attribution)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, releaseID, item.ID, im.ImageURL, im.SourceURL, im.Creator, im.License, im.LicenseURL, im.Attribution)
		if err != nil {
			return "", 0, 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", 0, 0, err
	}
	return "matched", len(descriptions), len(images), nil
}
