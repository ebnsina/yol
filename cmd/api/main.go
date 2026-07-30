// Command api runs the yol control plane.
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/ebnsina/yol/internal/api"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/github"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/jobs"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/ebnsina/yol/internal/server"
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

	box, err := secrets.New(cfg.SecretsKey)
	if err != nil {
		slog.Error("cannot set up encryption", "error", err)
		os.Exit(1)
	}

	// Where code comes from. Parsed now so a bad key stops the process here rather than at the
	// first deploy somebody attempts.
	code, err := github.NewApp(cfg.GitHubAppID, cfg.GitHubAppSlug, cfg.GitHubPrivateKey,
		cfg.GitHubAPIURL.String())
	if err != nil {
		slog.Error("cannot set up access to repositories", "error", err)
		os.Exit(1)
	}

	hub := server.NewHub()
	defer hub.CloseAll()
	streams := server.NewStreams()

	// Specifications are signed so an agent can tell an instruction from us apart from one
	// that merely reached its connection.
	signer, err := proto.NewSigningKey(cfg.AgentSpecKey)
	if err != nil {
		slog.Error("cannot set up specification signing", "error", err)
		os.Exit(1)
	}

	servers := server.NewService(pool, box,
		strings.TrimSuffix(cfg.PublicURL.String(), "/")+"/v1/tls/allow")

	// Workers are registered before the runner starts, so nothing is picked up that this
	// process does not know how to do.
	workers := jobs.NewWorkers()
	server.RegisterWorkers(workers,
		server.NewSurveyor(servers),
		server.NewBootstrapper(servers, hub, signer, cfg.AgentDir, cfg.PublicURL.String()),
	)

	runner, err := jobs.New(pool.Raw(), workers)
	if err != nil {
		slog.Error("cannot set up background jobs", "error", err)
		os.Exit(1)
	}
	if err := runner.Start(ctx); err != nil {
		slog.Error("cannot start background jobs", "error", err)
		os.Exit(1)
	}
	servers.SetEnqueuer(server.NewEnqueuer(runner))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			slog.Error("background jobs did not stop cleanly", "error", err)
		}
	}()

	handler := api.New(api.Deps{
		Config:  cfg,
		DB:      pool,
		Servers: servers,
		Secrets: box,
		Code:    code,
		Hub:     hub,
		Streams: streams,
		Signer:  signer,
	})

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
