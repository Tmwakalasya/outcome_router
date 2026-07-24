package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/outcome-router/outcome-router/internal/api"
	"github.com/outcome-router/outcome-router/internal/config"
	"github.com/outcome-router/outcome-router/internal/provider"
	"github.com/outcome-router/outcome-router/internal/routing"
	"github.com/outcome-router/outcome-router/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	configPath := os.Getenv("OUTCOME_ROUTER_CONFIG")
	if configPath == "" {
		configPath = filepath.Join("config", "demo.json")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	var repository store.Repository
	var closeRepository func() error
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		postgresStore, postgresErr := store.NewPostgresStore(context.Background(), databaseURL)
		if postgresErr != nil {
			logger.Error("open postgres audit store", "error", postgresErr)
			os.Exit(1)
		}
		repository = postgresStore
		closeRepository = postgresStore.Close
		logger.Info("using postgres audit store")
	} else {
		fileStore, fileErr := store.NewFileStore(cfg.DataDirectory)
		if fileErr != nil {
			logger.Error("open file audit store", "error", fileErr)
			os.Exit(1)
		}
		repository = fileStore
		closeRepository = func() error { return nil }
		logger.Info("using local append-only audit store")
	}
	defer func() {
		if err := closeRepository(); err != nil {
			logger.Error("close audit store", "error", err)
		}
	}()
	persistedPolicies, err := repository.LoadPolicies()
	if err != nil {
		logger.Error("load persisted policies", "error", err)
		os.Exit(1)
	}
	initialPolicies := append(cfg.Policies, persistedPolicies...)
	registry := routing.NewRegistry(cfg.Catalog, initialPolicies, repository)
	providerClient := provider.NewClient(nil)
	engine := &routing.Engine{Catalog: cfg.Catalog, Health: providerClient}
	server := api.NewServer(cfg, registry, engine, providerClient, repository, logger)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		logger.Info("outcome router listening", "address", cfg.ListenAddress)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("router stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
}
