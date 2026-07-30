// Package api wires configuration and domain packages into an HTTP handler.
package api

import (
	"net/http"

	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/httpx"
)

// Deps holds everything the routes need. Fields are added as domains land.
type Deps struct {
	Config *config.API
}

// New builds the API handler with logging, panic recovery, and the error envelope.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", httpx.NotFoundHandler())
	mux.HandleFunc("GET /health", handleHealth)

	return httpx.Chain(mux,
		httpx.WithRequestID,
		httpx.LogRequests,
		httpx.Recover,
	)
}

type healthResponse struct {
	Status string `json:"status"`
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
