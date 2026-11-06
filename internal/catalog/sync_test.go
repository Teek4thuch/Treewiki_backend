package catalog

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRegisteredDatasetMetadata(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"title":"Catalogue of Life","doi":"10.48580/example","license":"CC_BY_4_0","endpoints":[{"url":"https://api.checklistbank.org/dataset/315557/export.zip?format=ColDP&extended=true"}]}`
		if r.URL.Host == "api.checklistbank.org" {
			body = `{"result":[{"key":316165,"doi":"10.48580/example","license":"cc by","origin":"xrelease","private":false,"version":"2026-08-26 XR"}]}`
		}
		return &http.Response{StatusCode: 200, Body: ioNopCloser(body), Header: http.Header{}}, nil
	})}
	dataset, address, key, err := Metadata(context.Background(), client, "TreeWiki test")
	if err != nil || key != "316165" || dataset.Version != "2026-08-26 XR" || !strings.Contains(address, "format=ColDP") {
		t.Fatalf("metadata: %q %q %v", address, key, err)
	}
}

func TestFailedDownload(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: ioNopCloser("upstream error"), Header: http.Header{}}, nil
	})}
	if _, err := fetch(context.Background(), client, "https://api.checklistbank.org/dataset/1/export.zip", "TreeWiki test"); err == nil {
		t.Fatal("accepted failed download")
	}
}

func TestRangeDownload(t *testing.T) {
	archiveBytes := []byte("small archive fixture")
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodHead {
			return &http.Response{StatusCode: 200, ContentLength: int64(len(archiveBytes)), Body: ioNopCloser(""), Header: http.Header{}}, nil
		}
		if r.Header.Get("Range") != "bytes=0-20" {
			t.Errorf("missing range: %s", r.Header.Get("Range"))
		}
		return &http.Response{StatusCode: 206, ContentLength: int64(len(archiveBytes)), Body: ioNopCloser(string(archiveBytes)), Header: http.Header{"Content-Range": []string{"bytes 0-20/21"}}}, nil
	})}
	output, err := os.CreateTemp(t.TempDir(), "archive-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := download(context.Background(), client, "https://api.checklistbank.org/fixture", "TreeWiki test", output); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(output.Name())
	if err != nil || string(result) != string(archiveBytes) {
		t.Fatalf("download: %q %v", result, err)
	}
}

type closer struct{ *strings.Reader }

func (closer) Close() error           { return nil }
func ioNopCloser(value string) closer { return closer{strings.NewReader(value)} }

func archive(t *testing.T, usage, names string) []*zip.File {
	t.Helper()
	path := t.TempDir() + "/fixture.zip"
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for name, value := range map[string]string{"metadata.yaml": "title: Fixture release\ndoi: 10.48580/fixture\nlicense: cc by\nversion: fixture\n", "NameUsage.tsv": usage, "VernacularName.tsv": names} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader.File
}

func TestTSVFixtureAndValidation(t *testing.T) {
	files := archive(t, "col:ID\tcol:parentID\tcol:scientificName\tcol:rank\tcol:status\tcol:authorship\nroot\t\tPlantae\tkingdom\taccepted\t\nsp\troot\tMangifera indica\tspecies\taccepted\tL.\nsyn\tsp\tMangifera domestica\tspecies\tsynonym\t\n", "col:taxonID\tcol:name\tcol:language\tcol:preferred\nsp\tMango\teng\ttrue\n")
	var count int
	_, err := streamTSV(fileByName(files, "NameUsage.tsv"), []string{"ID", "parentID", "scientificName", "rank", "status"}, func(row map[string]string) error {
		count++
		if row["id"] == "sp" && row["authorship"] != "L." {
			return fmt.Errorf("lost authorship")
		}
		return nil
	})
	if err != nil || count != 3 {
		t.Fatalf("records=%d error=%v", count, err)
	}
	_, err = streamTSV(fileByName(files, "VernacularName.tsv"), []string{"taxonID", "name", "language"}, func(row map[string]string) error {
		if row["name"] != "Mango" {
			t.Fatalf("name: %v", row)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = streamTSV(fileByName(files, "NameUsage.tsv"), []string{"missing"}, func(map[string]string) error { return nil })
	if err == nil {
		t.Fatal("accepted missing required column")
	}
}

func TestRejectsMismatchedArchiveMetadata(t *testing.T) {
	files := archive(t, "col:ID\tcol:parentID\tcol:scientificName\tcol:rank\tcol:status\na\t\tPlantae\tkingdom\taccepted\n", "col:taxonID\tcol:name\n")
	_, err := Import(context.Background(), nil, files, Dataset{DOI: "10.48580/other"}, "1")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("accepted mismatched DOI: %v", err)
	}
}

// TREEWIKI_TEST_DATABASE_URL must point at a disposable PostgreSQL database.
// All catalog tables created here are temporary and limited to one connection.
func TestImportActivationAndRollback(t *testing.T) {
	address := os.Getenv("TREEWIKI_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("set TREEWIKI_TEST_DATABASE_URL for PostgreSQL integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(address)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, statement := range []string{
		`CREATE TEMP TABLE catalog_releases(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,dataset_key text,title text,doi text,license text,citation text,active boolean DEFAULT false,taxon_count bigint DEFAULT 0,species_count bigint DEFAULT 0)`,
		`CREATE TEMP TABLE catalog_taxa(release_id bigint,taxon_id text,parent_id text,accepted_taxon_id text,scientific_name text,authorship text,rank text,status text,PRIMARY KEY(release_id,taxon_id))`,
		`CREATE TEMP TABLE catalog_names(release_id bigint,taxon_id text,name text,language text,preferred boolean,PRIMARY KEY(release_id,taxon_id,language,name))`,
	} {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	dataset := Dataset{Title: "Fixture release", DOI: "10.48580/fixture", License: "CC_BY_4_0"}
	good := archive(t, "col:ID\tcol:parentID\tcol:kingdom\tcol:scientificName\tcol:rank\tcol:status\na\t\tPlantae\tPlantae\tkingdom\taccepted\nb\ta\tPlantae\tMangifera\tgenus\taccepted\nc\tb\tPlantae\tMangifera indica\tspecies\taccepted\nd\tc\tPlantae\tMangifera domestica\tspecies\tsynonym\n", "col:taxonID\tcol:name\tcol:language\nc\tMango\teng\nc\t"+strings.Repeat("x", 3000)+"\teng\n")
	result, err := Import(ctx, pool, good, dataset, "release-one")
	if err != nil || result.Species != 1 || result.Synonyms != 1 || result.Names != 1 || result.SkippedNames != 1 {
		t.Fatalf("import: %+v %v", result, err)
	}
	bad := archive(t, "col:ID\tcol:parentID\tcol:kingdom\tcol:scientificName\tcol:rank\tcol:status\na\t\tPlantae\tPlantae\tkingdom\taccepted\nb\tmissing\tPlantae\tBroken plant\tspecies\taccepted\n", "col:taxonID\tcol:name\tcol:language\n")
	if _, err := Import(ctx, pool, bad, dataset, "release-two"); err == nil {
		t.Fatal("accepted malformed parent")
	}
	var active int64
	if err := pool.QueryRow(ctx, "SELECT id FROM catalog_releases WHERE active").Scan(&active); err != nil || active != result.ReleaseID {
		t.Fatalf("lost active release: %d %v", active, err)
	}
	interrupted, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Import(interrupted, pool, good, dataset, "interrupted"); err == nil {
		t.Fatal("accepted interrupted import")
	}
	second, err := Import(ctx, pool, good, dataset, "release-two")
	if err != nil || second.Species != 1 {
		t.Fatalf("repeat: %+v %v", second, err)
	}
	var public int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM catalog_taxa t JOIN catalog_releases r ON r.id=t.release_id AND r.active WHERE t.rank='species' AND t.status='accepted'").Scan(&public); err != nil || public != 1 {
		t.Fatalf("duplicate public species: %d %v", public, err)
	}
	orphan := archive(t, "col:ID\tcol:parentID\tcol:kingdom\tcol:scientificName\tcol:rank\tcol:status\na\t\tPlantae\tPlantae\tkingdom\taccepted\nc\tb\tPlantae\tMangifera indica\tspecies\taccepted\n", "col:taxonID\tcol:name\n")
	repaired, err := importWithResolver(ctx, pool, orphan, dataset, "release-three", func(_ context.Context, id string) (parentRecord, error) {
		if id != "b" {
			return parentRecord{}, fmt.Errorf("unexpected parent %s", id)
		}
		value := parentRecord{ID: "b", ParentID: "a", Status: "accepted"}
		value.Name.ScientificName = "Mangifera"
		value.Name.Rank = "genus"
		return value, nil
	})
	if err != nil || repaired.RecoveredParents != 1 || repaired.Species != 1 {
		t.Fatalf("parent recovery: %+v %v", repaired, err)
	}
}
