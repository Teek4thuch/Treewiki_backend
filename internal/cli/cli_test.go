package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"treewiki/wiki_backend/internal/config"
)

func TestCLIExposesOnlyCatalogCommands(t *testing.T) {
	var output bytes.Buffer
	if err := Run(context.Background(), config.Config{}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "catalog sync") || strings.Contains(output.String(), "approve") {
		t.Fatalf("unexpected help: %s", output.String())
	}
	for _, args := range [][]string{{"source", "approve"}, {"import", "enqueue"}, {"catalog", "sync", "extra"}} {
		if err := Run(context.Background(), config.Config{}, args, &output); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := Run(context.Background(), config.Config{}, []string{"catalog", "sync"}, &output); err == nil || !strings.Contains(err.Error(), "MIGRATION_DATABASE_URL") {
		t.Fatalf("missing owner role check: %v", err)
	}
}
