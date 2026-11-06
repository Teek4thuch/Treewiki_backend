package catalog

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const datasetURL = "https://api.gbif.org/v1/dataset/7ddf754f-d193-4cc9-b351-99906754a03b"
const maxArchiveBytes int64 = 8 << 30

type Result struct {
	ReleaseID        int64  `json:"release_id"`
	DatasetKey       string `json:"dataset_key"`
	Taxa             int64  `json:"taxa"`
	Species          int64  `json:"species"`
	Synonyms         int64  `json:"synonyms"`
	Names            int64  `json:"names"`
	RecoveredParents int64  `json:"recovered_parents"`
	SkippedNames     int64  `json:"skipped_names"`
	Unchanged        bool   `json:"unchanged"`
}

type Dataset struct {
	Title     string `json:"title"`
	DOI       string `json:"doi"`
	License   string `json:"license"`
	Version   string `json:"-"`
	Endpoints []struct {
		URL string `json:"url"`
	} `json:"endpoints"`
}

func client() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return errors.New("unsupported download redirect")
		}
		switch req.URL.Hostname() {
		case "api.checklistbank.org", "download.checklistbank.org":
			return nil
		default:
			return errors.New("download redirected to an unexpected host")
		}
	}}
}

func fetch(ctx context.Context, httpClient *http.Client, address, userAgent string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func Metadata(ctx context.Context, httpClient *http.Client, userAgent string) (Dataset, string, string, error) {
	resp, err := fetch(ctx, httpClient, datasetURL, userAgent)
	if err != nil {
		return Dataset{}, "", "", err
	}
	defer resp.Body.Close()
	var dataset Dataset
	metadata, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return Dataset{}, "", "", fmt.Errorf("read GBIF metadata: %w", err)
	}
	if len(metadata) > 32<<20 {
		return Dataset{}, "", "", errors.New("GBIF metadata exceeds 32 MiB")
	}
	if err := json.Unmarshal(metadata, &dataset); err != nil {
		return Dataset{}, "", "", fmt.Errorf("decode %d bytes of GBIF metadata: %w", len(metadata), err)
	}
	license := strings.TrimSuffix(strings.Replace(dataset.License, "http://", "https://", 1), "/")
	if license != "CC_BY_4_0" && license != "https://creativecommons.org/licenses/by/4.0" && license != "https://creativecommons.org/licenses/by/4.0/legalcode" {
		return Dataset{}, "", "", errors.New("unsupported or missing dataset license")
	}
	if dataset.Title == "" || dataset.DOI == "" {
		return Dataset{}, "", "", errors.New("dataset title or DOI is missing")
	}
	registeredKey := ""
	for _, endpoint := range dataset.Endpoints {
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() != "api.checklistbank.org" {
			continue
		}
		if !strings.HasSuffix(u.Path, "/export.zip") || !strings.EqualFold(u.Query().Get("format"), "ColDP") {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "dataset" || parts[1] == "" {
			continue
		}
		if number, err := strconv.ParseInt(parts[1], 10, 64); err != nil || number <= 0 {
			continue
		}
		registeredKey = parts[1]
		break
	}
	if registeredKey == "" {
		return Dataset{}, "", "", errors.New("GBIF did not register a ChecklistBank ColDP export")
	}
	lookupURL := "https://api.checklistbank.org/dataset?q=" + url.QueryEscape(dataset.DOI) + "&limit=10"
	lookup, err := fetch(ctx, httpClient, lookupURL, userAgent)
	if err != nil {
		return Dataset{}, "", "", fmt.Errorf("ChecklistBank DOI lookup: %w", err)
	}
	defer lookup.Body.Close()
	var matches struct {
		Result []struct {
			Key     int64  `json:"key"`
			DOI     string `json:"doi"`
			License string `json:"license"`
			Origin  string `json:"origin"`
			Private bool   `json:"private"`
			Version string `json:"version"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(lookup.Body, 4<<20)).Decode(&matches); err != nil {
		return Dataset{}, "", "", err
	}
	for _, match := range matches.Result {
		if match.DOI != dataset.DOI || match.Key <= 0 || match.Origin != "xrelease" || match.Private || !strings.EqualFold(match.License, "cc by") {
			continue
		}
		key := strconv.FormatInt(match.Key, 10)
		if key != registeredKey {
			slog.Warn("GBIF endpoint differs from its registered DOI release", "endpoint_key", registeredKey, "doi_key", key)
		}
		dataset.Version = match.Version
		return dataset, "https://api.checklistbank.org/dataset/" + key + "/export.zip?format=ColDP&extended=true", key, nil
	}
	return Dataset{}, "", "", errors.New("no public ChecklistBank Extended Release matches the GBIF DOI")
}

// Sync fetches the GBIF-registered release and replaces the active catalog only
// after parsing and validating a complete Plantae subtree.
func Sync(ctx context.Context, ownerURL, userAgent string) (Result, error) {
	if ownerURL == "" {
		return Result{}, errors.New("MIGRATION_DATABASE_URL is required")
	}
	httpClient := client()
	var dataset Dataset
	var address, key string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		dataset, address, key, err = Metadata(ctx, httpClient, userAgent)
		if err == nil || ctx.Err() != nil {
			break
		}
		if attempt < 2 {
			slog.Warn("metadata request failed; retrying", "attempt", attempt+1, "error", err)
			select {
			case <-time.After(time.Duration(attempt+1) * time.Second):
			case <-ctx.Done():
			}
		}
	}
	if err != nil {
		return Result{}, fmt.Errorf("GBIF registration: %w", err)
	}
	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		return Result{}, err
	}
	defer pool.Close()
	var existing Result
	err = pool.QueryRow(ctx, `SELECT r.id, r.taxon_count, r.species_count,
 (SELECT count(*) FROM catalog_taxa t WHERE t.release_id=r.id AND t.accepted_taxon_id IS NOT NULL),
 (SELECT count(*) FROM catalog_names n WHERE n.release_id=r.id)
 FROM catalog_releases r WHERE r.active AND r.dataset_key=$1`, key).
		Scan(&existing.ReleaseID, &existing.Taxa, &existing.Species, &existing.Synonyms, &existing.Names)
	if err == nil {
		existing.DatasetKey = key
		existing.Unchanged = true
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	cachePath := filepath.Join(os.TempDir(), "treewiki-catalog-"+key+".zip")
	reader, err := zip.OpenReader(cachePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			if removeErr := os.Remove(cachePath); removeErr != nil {
				return Result{}, removeErr
			}
		}
		archive, createErr := os.CreateTemp("", "treewiki-catalog-*.zip")
		if createErr != nil {
			return Result{}, createErr
		}
		defer os.Remove(archive.Name())
		if downloadErr := download(ctx, httpClient, address, userAgent, archive); downloadErr != nil {
			archive.Close()
			return Result{}, fmt.Errorf("ChecklistBank archive download: %w", downloadErr)
		}
		if closeErr := archive.Close(); closeErr != nil {
			return Result{}, closeErr
		}
		if err := os.Rename(archive.Name(), cachePath); err != nil {
			return Result{}, err
		}
		reader, err = zip.OpenReader(cachePath)
	}
	if err != nil {
		return Result{}, fmt.Errorf("invalid ColDP ZIP: %w", err)
	}
	defer reader.Close()
	result, err := importWithResolver(ctx, pool, reader.File, dataset, key, func(ctx context.Context, id string) (parentRecord, error) {
		return fetchParent(ctx, httpClient, key, id, userAgent)
	})
	if err != nil {
		return Result{}, fmt.Errorf("catalog import failed (archive retained at %s): %w", cachePath, err)
	}
	reader.Close()
	_ = os.Remove(cachePath)
	return result, nil
}

// ChecklistBank supports byte ranges. Parallel bounded requests avoid a stalled
// full-file response and each chunk is checked against the advertised size.
func download(ctx context.Context, httpClient *http.Client, address, userAgent string, output *os.File) error {
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		return err
	}
	head.Header.Set("User-Agent", userAgent)
	response, err := httpClient.Do(head)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	size := response.ContentLength
	if size <= 0 || size > maxArchiveBytes {
		return fmt.Errorf("invalid archive size %d", size)
	}
	if err := output.Truncate(size); err != nil {
		return err
	}
	const chunkSize int64 = 32 << 20
	chunks := (size + chunkSize - 1) / chunkSize
	slog.Info("downloading catalog archive", "bytes", size, "chunks", chunks)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int64)
	firstError := make(chan error, 1)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				start := index * chunkSize
				end := start + chunkSize - 1
				if end >= size {
					end = size - 1
				}
				if err := downloadRange(workCtx, httpClient, address, userAgent, output, start, end, size); err != nil {
					select {
					case firstError <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
feed:
	for index := int64(0); index < chunks; index++ {
		select {
		case jobs <- index:
		case <-workCtx.Done():
			break feed
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-firstError:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slog.Info("catalog archive downloaded", "bytes", size)
	return nil
}

func downloadRange(ctx context.Context, httpClient *http.Client, address, userAgent string, output *os.File, start, end, size int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("range %d-%d returned HTTP %d", start, end, response.StatusCode)
	}
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	if response.Header.Get("Content-Range") != expected {
		return fmt.Errorf("range %d-%d returned invalid Content-Range", start, end)
	}
	written, err := io.CopyN(io.NewOffsetWriter(output, start), response.Body, end-start+1)
	if err != nil {
		return fmt.Errorf("range %d-%d transferred %d bytes: %w", start, end, written, err)
	}
	return nil
}

func fileByName(files []*zip.File, name string) *zip.File {
	for _, file := range files {
		if strings.EqualFold(path.Base(file.Name), name) {
			return file
		}
	}
	return nil
}

type parentRecord struct {
	DatasetKey int64  `json:"datasetKey"`
	ID         string `json:"id"`
	ParentID   string `json:"parentId"`
	Status     string `json:"status"`
	Name       struct {
		ScientificName string `json:"scientificName"`
		Authorship     string `json:"authorship"`
		Rank           string `json:"rank"`
	} `json:"name"`
}

func fetchParent(ctx context.Context, httpClient *http.Client, key, id, userAgent string) (parentRecord, error) {
	address := "https://api.checklistbank.org/dataset/" + key + "/taxon/" + url.PathEscape(id)
	response, err := fetch(ctx, httpClient, address, userAgent)
	if err != nil {
		return parentRecord{}, err
	}
	defer response.Body.Close()
	var record parentRecord
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&record); err != nil {
		return record, err
	}
	number, _ := strconv.ParseInt(key, 10, 64)
	if record.DatasetKey != number || record.ID != id || record.Name.ScientificName == "" || record.Name.Rank == "" ||
		(record.Status != "accepted" && record.Status != "provisionally accepted") {
		return parentRecord{}, fmt.Errorf("invalid ChecklistBank parent %s", id)
	}
	return record, nil
}

func repairParents(ctx context.Context, tx pgx.Tx, resolver func(context.Context, string) (parentRecord, error)) (int64, error) {
	var recovered int64
	for depth := 0; depth < 32; depth++ {
		rows, err := tx.Query(ctx, `SELECT DISTINCT child.parent_id FROM stage_taxa child
LEFT JOIN stage_taxa parent ON parent.id=child.parent_id
WHERE child.parent_id IS NOT NULL AND parent.id IS NULL AND child.kingdom='Plantae'
ORDER BY child.parent_id LIMIT 5001`)
		if err != nil {
			return recovered, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return recovered, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return recovered, err
		}
		rows.Close()
		if len(ids) == 0 {
			return recovered, nil
		}
		if len(ids) > 5000 {
			return recovered, errors.New("more than 5000 missing Plantae parent IDs")
		}
		type found struct {
			record parentRecord
			err    error
		}
		jobs := make(chan string)
		results := make(chan found, len(ids))
		var workers sync.WaitGroup
		for i := 0; i < 8; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for id := range jobs {
					record, err := resolver(ctx, id)
					results <- found{record, err}
				}
			}()
		}
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)
		workers.Wait()
		close(results)
		for result := range results {
			if result.err != nil {
				return recovered, fmt.Errorf("resolve missing archive parent: %w", result.err)
			}
			record := result.record
			if record.ParentID == "" && record.Name.Rank != "kingdom" {
				return recovered, fmt.Errorf("parent %s has no higher taxon", record.ID)
			}
			_, err := tx.Exec(ctx, `INSERT INTO stage_taxa(id,parent_id,kingdom,scientific_name,authorship,rank,status)
VALUES($1,$2,'Plantae',$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, record.ID, null(record.ParentID), record.Name.ScientificName, null(record.Name.Authorship), strings.ToLower(record.Name.Rank), strings.ToLower(record.Status))
			if err != nil {
				return recovered, err
			}
			recovered++
		}
	}
	return recovered, errors.New("missing Plantae parent chain exceeds 32 levels")
}

func streamTSV(file *zip.File, required []string, each func(map[string]string) error) (int64, error) {
	input, err := file.Open()
	if err != nil {
		return 0, err
	}
	defer input.Close()
	reader := csv.NewReader(input)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	reader.LazyQuotes = true
	headers, err := reader.Read()
	if err != nil {
		return 0, err
	}
	columns := make(map[string]int, len(headers))
	for i, header := range headers {
		column := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header, "\ufeff")))
		column = strings.TrimPrefix(column, "col:")
		columns[column] = i
	}
	for _, name := range required {
		if _, ok := columns[strings.ToLower(name)]; !ok {
			return 0, fmt.Errorf("%s lacks %s column", file.Name, name)
		}
	}
	var count int64
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, err
		}
		value := make(map[string]string, len(columns))
		for name, index := range columns {
			if index < len(row) {
				value[name] = strings.TrimSpace(row[index])
			}
		}
		if err := each(value); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func Import(ctx context.Context, pool *pgxpool.Pool, files []*zip.File, dataset Dataset, key string) (Result, error) {
	return importWithResolver(ctx, pool, files, dataset, key, nil)
}

func importWithResolver(ctx context.Context, pool *pgxpool.Pool, files []*zip.File, dataset Dataset, key string, resolver func(context.Context, string) (parentRecord, error)) (Result, error) {
	metadataFile := fileByName(files, "metadata.yaml")
	if metadataFile == nil {
		return Result{}, errors.New("ColDP archive has no metadata.yaml")
	}
	metadataInput, err := metadataFile.Open()
	if err != nil {
		return Result{}, err
	}
	var archiveMetadata struct{ DOI, License, Version string }
	scanner := bufio.NewScanner(metadataInput)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "'\"")
		switch name {
		case "doi":
			archiveMetadata.DOI = value
		case "license":
			archiveMetadata.License = value
		case "version":
			archiveMetadata.Version = value
		}
	}
	if err := scanner.Err(); err != nil {
		metadataInput.Close()
		return Result{}, fmt.Errorf("invalid ColDP metadata: %w", err)
	}
	if err := metadataInput.Close(); err != nil {
		return Result{}, err
	}
	if archiveMetadata.DOI != dataset.DOI || !strings.EqualFold(archiveMetadata.License, "cc by") ||
		(dataset.Version != "" && archiveMetadata.Version != dataset.Version) {
		return Result{}, fmt.Errorf("ColDP DOI, license, or version does not match GBIF registration")
	}
	usage := fileByName(files, "NameUsage.tsv")
	if usage == nil {
		return Result{}, errors.New("ColDP archive has no NameUsage.tsv")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE stage_taxa (
id text PRIMARY KEY, parent_id text, kingdom text, scientific_name text NOT NULL, authorship text,
rank text NOT NULL, status text NOT NULL) ON COMMIT DROP`)
	if err != nil {
		return Result{}, err
	}
	batch := make([][]any, 0, 5000)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"stage_taxa"}, []string{"id", "parent_id", "kingdom", "scientific_name", "authorship", "rank", "status"}, pgx.CopyFromRows(batch))
		batch = batch[:0]
		return err
	}
	count, err := streamTSV(usage, []string{"ID", "parentID", "scientificName", "rank", "status"}, func(row map[string]string) error {
		id, name := row["id"], row["scientificname"]
		if id == "" || name == "" {
			return errors.New("taxon ID or scientific name is empty")
		}
		batch = append(batch, []any{id, null(row["parentid"]), row["kingdom"], name, null(row["authorship"]), strings.ToLower(row["rank"]), strings.ToLower(row["status"])})
		if len(batch) == cap(batch) {
			return flush()
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if err := flush(); err != nil {
		return Result{}, err
	}
	if count == 0 {
		return Result{}, errors.New("empty taxonomy archive")
	}
	slog.Info("taxonomy archive staged", "records", count)
	if _, err := tx.Exec(ctx, "CREATE INDEX stage_taxa_parent_idx ON stage_taxa (parent_id)"); err != nil {
		return Result{}, err
	}
	var recovered int64
	if resolver != nil {
		recovered, err = repairParents(ctx, tx, resolver)
		if err != nil {
			return Result{}, err
		}
		if recovered > 0 {
			slog.Info("recovered missing parents from same ChecklistBank release", "taxa", recovered)
		}
	}
	var missingParents int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM stage_taxa child LEFT JOIN stage_taxa parent ON parent.id=child.parent_id
WHERE child.parent_id IS NOT NULL AND parent.id IS NULL AND child.kingdom='Plantae'`).Scan(&missingParents); err != nil {
		return Result{}, err
	}
	if missingParents != 0 {
		var examples string
		_ = tx.QueryRow(ctx, `SELECT string_agg(id||'->'||parent_id||' ('||status||')', ', ') FROM (
SELECT child.id,child.parent_id,child.status FROM stage_taxa child
LEFT JOIN stage_taxa parent ON parent.id=child.parent_id
WHERE child.parent_id IS NOT NULL AND parent.id IS NULL AND child.kingdom='Plantae'
ORDER BY child.id LIMIT 8) sample`).Scan(&examples)
		return Result{}, fmt.Errorf("archive has %d missing Plantae parent links: %s", missingParents, examples)
	}
	var root string
	err = tx.QueryRow(ctx, `SELECT id FROM stage_taxa WHERE scientific_name='Plantae' AND rank='kingdom' AND status='accepted'`).Scan(&root)
	if err != nil {
		return Result{}, fmt.Errorf("accepted Plantae root is missing: %w", err)
	}
	var previous int64
	previousErr := tx.QueryRow(ctx, "SELECT id FROM catalog_releases WHERE active").Scan(&previous)
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		return Result{}, previousErr
	}
	citation := dataset.Title + " (" + archiveMetadata.Version + "). https://doi.org/" + dataset.DOI
	var releaseID int64
	if err := tx.QueryRow(ctx, `INSERT INTO catalog_releases (dataset_key,title,doi,license,citation)
VALUES ($1,$2,$3,$4,$5) RETURNING id`, key, dataset.Title, dataset.DOI, dataset.License, citation).Scan(&releaseID); err != nil {
		return Result{}, err
	}
	_, err = tx.Exec(ctx, `WITH RECURSIVE plant AS (
 SELECT id FROM stage_taxa WHERE id=$2
 UNION
 SELECT child.id FROM stage_taxa child JOIN plant parent ON child.parent_id=parent.id
 WHERE child.status IN ('accepted','provisionally accepted')
)
INSERT INTO catalog_taxa (release_id,taxon_id,parent_id,scientific_name,authorship,rank,status)
SELECT $1, s.id, CASE WHEN s.id=$2 THEN NULL ELSE s.parent_id END,
s.scientific_name,s.authorship,s.rank,s.status FROM stage_taxa s JOIN plant p ON p.id=s.id`, releaseID, root)
	if err != nil {
		return Result{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO catalog_taxa
(release_id,taxon_id,accepted_taxon_id,scientific_name,authorship,rank,status)
SELECT $1,s.id,s.parent_id,s.scientific_name,s.authorship,s.rank,s.status
FROM stage_taxa s JOIN catalog_taxa accepted ON accepted.release_id=$1 AND accepted.taxon_id=s.parent_id
WHERE s.status IN ('synonym','ambiguous synonym','misapplied')`, releaseID)
	if err != nil {
		return Result{}, err
	}
	var taxa, species, synonyms int64
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE rank='species' AND status='accepted')
 ,count(*) FILTER (WHERE accepted_taxon_id IS NOT NULL)
FROM catalog_taxa WHERE release_id=$1`, releaseID).Scan(&taxa, &species, &synonyms); err != nil {
		return Result{}, err
	}
	if species == 0 {
		return Result{}, errors.New("Plantae subtree has no accepted species")
	}
	slog.Info("Plantae hierarchy staged", "taxa", taxa, "species", species, "synonyms", synonyms)
	var dangling int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM catalog_taxa c
LEFT JOIN catalog_taxa p ON p.release_id=c.release_id AND p.taxon_id=COALESCE(c.parent_id,c.accepted_taxon_id)
WHERE c.release_id=$1 AND COALESCE(c.parent_id,c.accepted_taxon_id) IS NOT NULL AND p.taxon_id IS NULL`, releaseID).Scan(&dangling); err != nil {
		return Result{}, err
	}
	if dangling != 0 {
		return Result{}, fmt.Errorf("%d catalog records have missing parents", dangling)
	}
	var skippedNames int64
	if names := fileByName(files, "VernacularName.tsv"); names != nil {
		if _, err := tx.Exec(ctx, "CREATE TEMP TABLE stage_names (taxon_id text, name text, language text, preferred boolean) ON COMMIT DROP"); err != nil {
			return Result{}, err
		}
		batch = make([][]any, 0, 5000)
		flushNames := func() error {
			if len(batch) == 0 {
				return nil
			}
			_, err := tx.CopyFrom(ctx, pgx.Identifier{"stage_names"}, []string{"taxon_id", "name", "language", "preferred"}, pgx.CopyFromRows(batch))
			batch = batch[:0]
			return err
		}
		_, err := streamTSV(names, []string{"taxonID", "name"}, func(row map[string]string) error {
			if row["taxonid"] == "" || row["name"] == "" {
				return nil
			}
			if len(row["name"]) > 2048 || len(row["language"]) > 64 {
				skippedNames++
				return nil
			}
			batch = append(batch, []any{row["taxonid"], row["name"], row["language"], strings.EqualFold(row["preferred"], "true")})
			if len(batch) == cap(batch) {
				return flushNames()
			}
			return nil
		})
		if err != nil {
			return Result{}, err
		}
		if err := flushNames(); err != nil {
			return Result{}, err
		}
		slog.Info("vernacular names staged", "skipped_oversized", skippedNames)
		if skippedNames > 0 {
			slog.Warn("omitted oversized vernacular values", "count", skippedNames)
		}
		_, err = tx.Exec(ctx, `INSERT INTO catalog_names (release_id,taxon_id,name,language,preferred)
SELECT $1,n.taxon_id,n.name,n.language,bool_or(n.preferred)
FROM stage_names n JOIN catalog_taxa t ON t.release_id=$1 AND t.taxon_id=n.taxon_id
GROUP BY n.taxon_id,n.name,n.language`, releaseID)
		if err != nil {
			return Result{}, err
		}
	}
	var nameCount int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM catalog_names WHERE release_id=$1", releaseID).Scan(&nameCount); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE catalog_releases SET active=false WHERE active"); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE catalog_releases SET active=true,taxon_count=$2,species_count=$3 WHERE id=$1", releaseID, taxa, species); err != nil {
		return Result{}, err
	}
	if previousErr == nil {
		if _, err := tx.Exec(ctx, "DELETE FROM catalog_releases WHERE id NOT IN ($1,$2)", releaseID, previous); err != nil {
			return Result{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, "DELETE FROM catalog_releases WHERE id<>$1", releaseID); err != nil {
			return Result{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{ReleaseID: releaseID, DatasetKey: key, Taxa: taxa, Species: species, Synonyms: synonyms, Names: nameCount, RecoveredParents: recovered, SkippedNames: skippedNames}, nil
}

func null(value string) any {
	if value == "" {
		return nil
	}
	return value
}
