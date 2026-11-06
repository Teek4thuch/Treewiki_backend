# Migrations

Goose migrations are embedded in the CLI. Run `go run ./cmd/treewiki migrate up` with `MIGRATION_DATABASE_URL`. The API never runs migrations.

Migrations `00001`–`00004` remain in source because they may have been applied already. New migration `00005_catalog.sql` deletes their pilot tables and review function, then creates release, taxon, and vernacular name tables. Its down step removes the catalog tables; it cannot restore deleted pilot data.

Run `go run ./cmd/treewiki database grant-runtime` after migration to grant the API role only `SELECT` on catalog tables. Keep the owner credential out of the API process.
