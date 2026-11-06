package enrichment

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"treewiki/wiki_backend/internal/platform/database"
	"treewiki/wiki_backend/internal/repositories"
)

// TREEWIKI_TEST_DATABASE_URL must refer to an empty, disposable database.
func TestImportAndDetailIntegration(t *testing.T) {
	address := os.Getenv("TREEWIKI_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("set TREEWIKI_TEST_DATABASE_URL to an empty disposable PostgreSQL database")
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
	var release int64
	err = pool.QueryRow(ctx, `INSERT INTO catalog_releases(dataset_key,title,doi,license,citation,active) VALUES('test','test','test','test','test',true) RETURNING id`).Scan(&release)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO catalog_taxa(release_id,taxon_id,parent_id,scientific_name,rank,status) VALUES
($1,'parent',NULL,'Mangifera','genus','accepted'),($1,'test','parent','Mangifera indica','species','accepted')`, release)
	if err != nil {
		t.Fatal(err)
	}
	failing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			http.Error(w, "upstream failure", http.StatusBadRequest)
			return
		}
		switch r.URL.Query().Get("action") {
		case "wbsearchentities":
			fmt.Fprint(w, `{"search":[{"id":"Q1"}]}`)
		case "wbgetentities":
			if r.URL.Query().Get("ids") == "Q9" {
				fmt.Fprint(w, `{"entities":{"Q9":{"id":"Q9","claims":{"P225":[{"mainsnak":{"datavalue":{"value":"Mangifera"}}}]}}}}`)
				return
			}
			fmt.Fprint(w, `{"entities":{"Q1":{"id":"Q1","claims":{"P225":[{"mainsnak":{"datavalue":{"value":"Mangifera indica"}}}],"P105":[{"mainsnak":{"datavalue":{"value":{"id":"Q7432"}}}}],"P171":[{"mainsnak":{"datavalue":{"value":{"id":"Q9"}}}}],"P18":[{"mainsnak":{"datavalue":{"value":"a.jpg"}}}]},"sitelinks":{"enwiki":{"title":"Mango"}}}}}`)
		case "query":
			if r.URL.Query().Get("prop") == "imageinfo" {
				fmt.Fprint(w, `{"query":{"pages":[{"imageinfo":[{"url":"https://upload.wikimedia.org/a.jpg","descriptionurl":"https://commons.wikimedia.org/wiki/File:a.jpg","extmetadata":{"Artist":{"value":"Jane"},"LicenseShortName":{"value":"CC BY 4.0"},"LicenseUrl":{"value":"https://creativecommons.org/licenses/by/4.0/"}}}]}]}}`)
				return
			}
			fmt.Fprint(w, `{"query":{"pages":[{"extract":"Plant text","fullurl":"https://en.wikipedia.org/wiki/Mango","revisions":[{"revid":42}]}]}}`)
		}
	}))
	defer server.Close()
	client := newAPI("test")
	client.wikidata = server.URL
	client.commons = server.URL
	client.wikipedia = server.URL + "/%s"
	client.interval = 0
	item := species{ID: "test", Name: "Mangifera indica", Parent: "Mangifera"}
	for n := 0; n < 2; n++ {
		status, descriptions, images, err := process(ctx, client, pool, release, item)
		if err != nil || status != "matched" || descriptions != 1 || images != 1 {
			t.Fatalf("import %d: %s %d %d %v", n, status, descriptions, images, err)
		}
	}
	detail, err := repositories.NewPlantRepository(pool).Detail(ctx, "test")
	if err != nil || len(detail.Descriptions) != 1 || len(detail.Images) != 1 {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	failing = true
	if _, _, _, err = process(ctx, client, pool, release, item); err == nil {
		t.Fatal("expected upstream failure")
	}
	detail, err = repositories.NewPlantRepository(pool).Detail(ctx, "test")
	if err != nil || len(detail.Descriptions) != 1 || len(detail.Images) != 1 {
		t.Fatalf("failed refresh removed content: %+v %v", detail, err)
	}
	_, err = pool.Exec(ctx, `UPDATE catalog_releases SET active=false WHERE id=$1`, release)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO catalog_releases(dataset_key,title,doi,license,citation,active) VALUES('new','new','new','new','new',true)`)
	if err != nil {
		t.Fatal(err)
	}
	var next int64
	if err = pool.QueryRow(ctx, "SELECT id FROM catalog_releases WHERE active").Scan(&next); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO catalog_taxa(release_id,taxon_id,parent_id,scientific_name,rank,status) VALUES
($1,'parent',NULL,'Mangifera','genus','accepted'),($1,'test','parent','Mangifera indica','species','accepted')`, next)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = repositories.NewPlantRepository(pool).Detail(ctx, "test")
	if err != nil || len(detail.Descriptions) != 0 || len(detail.Images) != 0 {
		t.Fatalf("old release content exposed: %+v %v", detail, err)
	}
}
