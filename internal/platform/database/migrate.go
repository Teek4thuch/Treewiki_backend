package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"treewiki/wiki_backend/migrations"
)

// Migrate is called only by the operator CLI, never by the running API.
func Migrate(ctx context.Context, databaseURL, command string) (int64, error) {
	if strings.TrimSpace(databaseURL) == "" { return 0, errors.New("MIGRATION_DATABASE_URL is required") }
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil { return 0, errors.New("invalid MIGRATION_DATABASE_URL") }
	if cfg.RuntimeParams == nil { cfg.RuntimeParams = map[string]string{} }
	cfg.RuntimeParams["search_path"] = "public"
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil { return 0, err }
	switch command {
	case "up":
		if err := goose.UpContext(ctx, db, "."); err != nil { return 0, err }
	case "status":
	default:
		return 0, fmt.Errorf("unsupported migration command %q", command)
	}
	return goose.GetDBVersionContext(ctx, db)
}
