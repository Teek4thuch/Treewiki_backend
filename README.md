# TreeWiki backend

The API reads the active PostgreSQL catalog through a read-only `DATABASE_URL` role. The CLI uses `MIGRATION_DATABASE_URL` for migration and bulk import.

## Commands

From this directory:

```powershell
go run ./cmd/treewiki migrate up
go run ./cmd/treewiki database grant-runtime
go run ./cmd/treewiki catalog sync
go run ./cmd/server
```

`catalog sync` reads the GBIF dataset registration for Catalogue of Life, resolves the registration DOI to the matching public ChecklistBank Extended Release, downloads its full ColDP ZIP, and streams `NameUsage.tsv` and optional `VernacularName.tsv` into a PostgreSQL transaction. GBIF's stored endpoint can lag its DOI; the DOI selects the exact release. The command validates the CC BY 4.0 license, archive DOI and version, structure, IDs, parent links, Plantae root, and nonzero accepted species count. If archive parent records are absent, it retrieves those IDs from the same ChecklistBank release and revalidates the chain. It activates a new release only when the entire transaction commits. A repeated sync of the same registered release returns `unchanged: true` without downloading again.

The first import can take substantial time and disk space because it stages the whole archive before selecting Plantae. The ZIP has an 8 GiB limit; PostgreSQL also needs room for staging and two plant releases. `Ctrl+C`, a failed download, or an invalid archive leaves the previously active release unchanged. A fully downloaded archive is kept in the system temporary directory after an import error so a corrected command can retry without another large download; a successful sync removes it.

The command reports `recovered_parents` for source parent records fetched from the same release when they are missing from its ZIP. It reports `skipped_names` for malformed vernacular values over 2 KB, which cannot be safely indexed or displayed as common names.

## Wikimedia enrichment

Run `go run ./cmd/treewiki migrate up` and `go run ./cmd/treewiki database grant-runtime` after upgrading. Set `WIKIMEDIA_USER_AGENT` to an application name and a real contact email or HTTPS contact URL; do not use the example placeholder. The importer uses `MIGRATION_DATABASE_URL`; the API uses its read-only role.

Run `go run ./cmd/treewiki enrichment sync --limit 10` for an initial batch. It selects active, accepted species with at least one common name directly from PostgreSQL; the CSV export is not an input. The command reports counts and the last catalogue ID. Repeat the command to process more species. Unprocessed and previously failed species are selected automatically; use `--after ID` to start after an ID, or `--refresh` to recheck already processed species. The maximum batch is 1,000. Failures and ambiguous results are recorded in `enrichment_progress`.

The initial matching strategy requires the exact Wikidata taxon name, species rank, and an exact name for the immediate parent taxon. It intentionally skips uncertain matches; it does not assume that a catalogue ID from a different release has the same scope. Enrichment verified against an old release remains stored but is not exposed after a new release becomes active until rechecked.

Descriptions include their article, revision, license, and attribution; images contain a Wikimedia-hosted URL, file page, creator, and license. Image bytes are never downloaded. A successful refresh removes content that no longer exists or no longer has sufficient licensing metadata. A failed upstream request leaves previously imported content intact. API consumers receive `descriptions` and `images` arrays on species detail responses, including source and attribution fields.

## Public endpoints

`GET /api/v1/plants` returns `{"taxa":[...],"next_cursor":"..."}` for accepted species. Pass `q` for name search and the returned cursor for the next 50 records. Pass `enriched=true` to restrict results to species with a description or image in the active release. This list ranks species by one point for an image and one point for a description with more than 100 whitespace-separated words, then sorts ties by scientific name and ID. It also returns description previews and credited image links. `GET /api/v1/plants/:id` returns the full profile. `GET /api/v1/taxa/root`, `GET /api/v1/taxa/:id`, and `GET /api/v1/taxa/:id/children` expose the hierarchy. No review or operator publication endpoint is registered.

## Checks

```powershell
go test ./...
go build ./cmd/server ./cmd/treewiki
```

The public release is the [Catalogue of Life Extended Release registered with GBIF](https://www.gbif.org/dataset/7ddf754f-d193-4cc9-b351-99906754a03b). The [ColDP format](https://github.com/CatalogueOfLife/coldp) defines the source file and parent relationships.
