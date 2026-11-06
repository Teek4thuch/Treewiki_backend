package config

import "os"

type Config struct {
	HTTPAddr             string
	DatabaseURL          string
	MigrationDatabaseURL string
	GBIFUserAgent        string
	WikimediaUserAgent   string
}

func Load() Config {
	return Config{
		HTTPAddr:             envOrDefault("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		MigrationDatabaseURL: os.Getenv("MIGRATION_DATABASE_URL"),
		GBIFUserAgent:        envOrDefault("GBIF_USER_AGENT", "TreeWiki/1.0 (catalog sync)"),
		WikimediaUserAgent:   os.Getenv("WIKIMEDIA_USER_AGENT"),
	}
}
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
