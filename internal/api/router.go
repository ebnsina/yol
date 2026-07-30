// Package api wires configuration and domain packages into an HTTP handler.
package api

import (
	"net/http"

	"github.com/ebnsina/yol/internal/auth"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/httpx"
)

// Deps holds everything the routes need. Fields are added as domains land.
type Deps struct {
	Config *config.API
	DB     *db.Pool
}

// New builds the API handler with logging, panic recovery, and the error envelope.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", httpx.NotFoundHandler())
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /ready", readyHandler(d.DB))

	authSvc := auth.NewService(d.DB, d.Config)
	auth.NewHandler(authSvc, d.Config).Routes(mux)

	return httpx.Chain(mux,
		httpx.WithRequestID,
		httpx.LogRequests,
		httpx.Recover,
		httpx.CORS(d.Config.WebOrigin.String()),
	)
}

type statusResponse struct {
	Status string `json:"status"`
}

// handleHealth reports that the process is running, without touching dependencies.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// readyHandler reports whether the process can serve traffic.
func readyHandler(pool *db.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			httpx.Fail(w, r, httpx.Internal(err))
			return
		}
		httpx.JSON(w, http.StatusOK, statusResponse{Status: "ready"})
	}
}
