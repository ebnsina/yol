// Command api runs the yol control plane.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ebnsina/yol/internal/api"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/httpx"
)

func main() {
	cfg := config.MustLoadAPI()
	setupLogging(cfg.Env)

	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("cannot connect to the database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	handler := api.New(api.Deps{Config: cfg, DB: pool})

	if err := httpx.Serve(ctx, cfg.HTTPAddr, handler, cfg.ShutdownTimeout); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// setupLogging uses readable text locally and JSON where logs are collected.
func setupLogging(env config.Environment) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if env == config.EnvDevelopment {
		opts.Level = slog.LevelDebug
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h).With("service", "api", "env", string(env)))
}
