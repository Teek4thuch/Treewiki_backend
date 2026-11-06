package repositories_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"treewiki/wiki_backend/internal/platform/database"
	"treewiki/wiki_backend/internal/repositories"
)

// TREEWIKI_TEST_DATABASE_URL must point to an empty disposable database.
func TestEnrichedCatalogIntegration(t *testing.T) {
	address := os.Getenv("TREEWIKI_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("set TREEWIKI_TEST_DATABASE_URL to an empty disposable database")
	}
	ctx := context.Background()
	if _, err := database.Migrate(ctx, address, "up"); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var old, active int64
	if err = pool.QueryRow(ctx, `INSERT INTO catalog_releases(dataset_key,title,doi,license,citation) VALUES('old','old','old','old','old') RETURNING id`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO catalog_releases(dataset_key,title,doi,license,citation,active) VALUES('active','active','active','active','active',true) RETURNING id`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	add := func(release int64, id, name string, match bool) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO catalog_taxa(release_id,taxon_id,scientific_name,rank,status) VALUES($1,$2,$3,'species','accepted')`, release, id, name)
		if err != nil {
			t.Fatal(err)
		}
		if match {
			_, err = pool.Exec(ctx, `INSERT INTO enrichment_matches(release_id,taxon_id,wikidata_id,match_method,source_url) VALUES($1,$2,'Q1','test','https://example.com')`, release, id)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	add(active, "image", "Image plant", true)
	add(active, "empty", "No content", true)
	add(active, "old-only", "Old release plant", false)
	add(old, "old-only", "Old release plant", true)
	addImage := func(release int64, id string) {
		t.Helper()
		_, err = pool.Exec(ctx, `INSERT INTO enrichment_images(release_id,taxon_id,image_url,source_url,creator,license,license_url,attribution) VALUES($1,$2,'https://upload.wikimedia.org/a','https://commons.wikimedia.org/a','creator','license','https://creativecommons.org/licenses/by/4.0/','creator')`, release, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	addImage(active, "image")
	addImage(old, "old-only")
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("desc-%02d", i)
		add(active, id, fmt.Sprintf("Description %02d", i), true)
		content := "Plant text"
		if i == 0 || i == 2 {
			content = strings.Repeat("word ", 101)
		}
		if i == 1 {
			content = "\n\t" + strings.Repeat("word ", 100) + "\n"
		}
		_, err = pool.Exec(ctx, `INSERT INTO enrichment_descriptions(release_id,taxon_id,language,content,source_url,revision_id,license,attribution) VALUES($1,$2,'en',$3,'https://en.wikipedia.org/wiki/A',42,'https://creativecommons.org/licenses/by-sa/4.0/','contributors')`, active, id, content)
		if err != nil {
			t.Fatal(err)
		}
	}
	addImage(active, "desc-00")
	addImage(active, "desc-01")
	repo := repositories.NewPlantRepository(pool)
	first, err := repo.List(ctx, "", "", true)
	if err != nil || len(first.Taxa) != 50 || first.NextCursor == nil {
		t.Fatalf("first page: %d %v", len(first.Taxa), err)
	}
	second, err := repo.List(ctx, "", *first.NextCursor, true)
	if err != nil || len(second.Taxa) != 2 || second.NextCursor != nil {
		t.Fatalf("second page: %+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, taxon := range append(first.Taxa, second.Taxa...) {
		if seen[taxon.ID] {
			t.Fatalf("duplicate %s", taxon.ID)
		}
		seen[taxon.ID] = true
	}
	if !seen["image"] || seen["empty"] || seen["old-only"] {
		t.Fatalf("incorrect membership: %v", seen)
	}
	if first.Taxa[0].ID != "desc-00" || first.Taxa[1].ID != "desc-01" || first.Taxa[2].ID != "desc-02" || first.Taxa[3].ID != "image" {
		t.Fatalf("unexpected rank order: %s, %s, %s, %s", first.Taxa[0].ID, first.Taxa[1].ID, first.Taxa[2].ID, first.Taxa[3].ID)
	}
	if len(first.Taxa[0].Descriptions) != 1 || len(first.Taxa[0].Images) != 1 {
		t.Fatal("missing previews")
	}
	found, err := repo.List(ctx, "Image", "", true)
	if err != nil || len(found.Taxa) != 1 || found.Taxa[0].ID != "image" {
		t.Fatalf("search: %+v %v", found, err)
	}
	all, err := repo.List(ctx, "Old release", "", false)
	if err != nil || len(all.Taxa) != 1 || all.Taxa[0].ID != "old-only" {
		t.Fatalf("unfiltered: %+v %v", all, err)
	}
}
