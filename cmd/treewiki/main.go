package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"treewiki/wiki_backend/internal/cli"
	"treewiki/wiki_backend/internal/config"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "could not read .env configuration")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, config.Load(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "treewiki:", err)
		os.Exit(1)
	}
}
