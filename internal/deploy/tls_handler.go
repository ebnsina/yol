package deploy

import (
	"log/slog"
	"net/http"

	"github.com/ebnsina/yol/internal/db"
	"github.com/jackc/pgx/v5"
)

// TLSHandler answers the router's question of whether it may obtain a certificate.
//
// Without this, anyone could point a hostname they own at a customer's server and make it request
// certificates on their behalf, until the rate limits of the certificate authority stopped it.
// The router asks here first, and only names somebody added and verified are allowed.
type TLSHandler struct {
	pool *db.Pool
}

// NewTLSHandler builds the permission endpoint.
func NewTLSHandler(pool *db.Pool) *TLSHandler {
	return &TLSHandler{pool: pool}
}

// Routes registers the endpoint. It is reached by the routers on customer servers rather than by
// a person, so it carries no session and returns no body.
func (h *TLSHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tls/allow", h.allow)
}

// allow answers with 200 when a certificate may be obtained, and 403 otherwise. The router reads
// only the status.
func (h *TLSHandler) allow(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("domain")
	if hostname == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var allowed bool
	// Asked before any organization is in scope, so this goes through the dedicated function
	// rather than a scoped query, in the same way sessions and enrollment do.
	err := h.pool.Unscoped(r.Context(), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT is_hostname_allowed($1)`, hostname).Scan(&allowed)
	})
	if err != nil {
		// Refusing on failure is the safe direction: a certificate not obtained can be retried,
		// while one obtained for a name we do not control cannot be taken back.
		slog.Error("could not check whether a hostname is allowed", "hostname", hostname, "error", err)
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if !allowed {
		slog.Warn("refused a certificate for a hostname nobody has added", "hostname", hostname)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
}
