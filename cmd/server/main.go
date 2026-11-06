package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"treewiki/wiki_backend/internal/config"
	"treewiki/wiki_backend/internal/controllers"
	"treewiki/wiki_backend/internal/platform/database"
	"treewiki/wiki_backend/internal/repositories"
	"treewiki/wiki_backend/internal/routes"
	"treewiki/wiki_backend/internal/services"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not read .env configuration")
	}
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.ValidateRuntimeRole(ctx, db); err != nil {
		return err
	}
	health := controllers.NewHealthController(services.NewHealthService(repositories.NewHealthRepository(db)))
	plants := controllers.NewPlantController(repositories.NewPlantRepository(db))
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: routes.NewRouter(health, plants), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	errorsCh := make(chan error, 1)
	go func() {
		slog.Info("TreeWiki API listening", "address", cfg.HTTPAddr)
		errorsCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
