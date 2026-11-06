package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(databaseURL) == "" { return nil, errors.New("DATABASE_URL is required") }
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Do not expose connection credentials in a configuration error.
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil { cfg.ConnConfig.RuntimeParams = map[string]string{} }
	cfg.ConnConfig.RuntimeParams["search_path"] = "public"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.MaxConns = 10
	return pgxpool.NewWithConfig(ctx, cfg)
}
