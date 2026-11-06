package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"treewiki/wiki_backend/internal/catalog"
	"treewiki/wiki_backend/internal/config"
	"treewiki/wiki_backend/internal/enrichment"
	"treewiki/wiki_backend/internal/platform/database"
)

const Usage = `TreeWiki catalog (run from wiki_backend)

  migrate up|status
  database grant-runtime
  catalog sync
  enrichment sync [--limit N] [--after TAXON_ID] [--refresh]

MIGRATION_DATABASE_URL performs migrations and imports. DATABASE_URL is read-only.
`

func Run(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		_, err := io.WriteString(out, Usage)
		return err
	}
	if len(args) >= 2 && args[0] == "enrichment" && args[1] == "sync" {
		flags := flag.NewFlagSet("enrichment sync", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		limit := flags.Int("limit", 100, "maximum number of species")
		after := flags.String("after", "", "start after a catalogue ID")
		refresh := flags.Bool("refresh", false, "recheck processed species")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *limit < 1 || *limit > 1000 {
			return errors.New("limit must be between 1 and 1000 and no positional arguments are accepted")
		}
		result, err := enrichment.Sync(ctx, cfg.MigrationDatabaseURL, cfg.WikimediaUserAgent, enrichment.Options{Limit: *limit, After: *after, Refresh: *refresh})
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if len(args) != 2 {
		return errors.New("expected a command and subcommand; use --help")
	}
	command := args[0] + " " + args[1]
	var result any
	switch command {
	case "migrate up", "migrate status":
		if cfg.MigrationDatabaseURL == "" {
			return errors.New("MIGRATION_DATABASE_URL is required")
		}
		version, err := database.Migrate(ctx, cfg.MigrationDatabaseURL, args[1])
		if err != nil {
			return err
		}
		result = map[string]any{"schema_version": version}
	case "database grant-runtime":
		if err := database.GrantRuntime(ctx, cfg.MigrationDatabaseURL, cfg.DatabaseURL); err != nil {
			return err
		}
		result = map[string]string{"status": "runtime_role_ready"}
	case "catalog sync":
		value, err := catalog.Sync(ctx, cfg.MigrationDatabaseURL, cfg.GBIFUserAgent)
		if err != nil {
			return err
		}
		result = value
	default:
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
