// Command agent runs on a customer's server. It dials out to the control plane, so the
// customer opens no inbound port, and it keeps working from what it has on disk when the
// control plane cannot be reached.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ebnsina/yol/internal/agent"
	"github.com/ebnsina/yol/internal/config"
)

func main() {
	cfg := config.MustLoadAgent()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With("service", "agent", "version", agent.Version))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := agent.New(cfg, agent.NewCollector(cfg.DockerHost))
	if err := a.Run(ctx); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
	slog.Info("agent stopped")
}
