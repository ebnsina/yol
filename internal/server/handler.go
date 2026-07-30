package server

import (
	"net/http"
	"time"

	"github.com/ebnsina/yol/internal/auth"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
)

// Handler exposes the server endpoints.
type Handler struct {
	svc      *Service
	orgs     *org.Service
	required httpx.Middleware
}

// NewHandler builds the server endpoints.
func NewHandler(svc *Service, orgs *org.Service, required httpx.Middleware) *Handler {
	return &Handler{svc: svc, orgs: orgs, required: required}
}

// Routes registers the server endpoints. All of them are scoped to an organization, so a
// server is only ever reachable through one the caller belongs to.
func (h *Handler) Routes(mux *http.ServeMux) {
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, h.required(fn))
	}
	route("GET /v1/organizations/{slug}/servers", h.list)
	route("POST /v1/organizations/{slug}/servers", h.connect)
	route("GET /v1/organizations/{slug}/servers/{id}", h.show)
	route("DELETE /v1/organizations/{slug}/servers/{id}", h.delete)
	route("GET /v1/organizations/{slug}/servers/{id}/events", h.events)
	route("GET /v1/organizations/{slug}/servers/{id}/resources", h.resources)
}

type connectRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	SSHPort  int    `json:"sshPort"`
	SSHUser  string `json:"sshUser"`
	Mode     Mode   `json:"mode"`
	Key      string `json:"key"`
	Password string `json:"password"`
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	var req connectRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	created, err := h.svc.Connect(r.Context(), m, session.User.ID, ConnectInput{
		Name:     req.Name,
		Host:     req.Host,
		Port:     req.SSHPort,
		User:     req.SSHUser,
		Mode:     req.Mode,
		Key:      req.Key,
		Password: req.Password,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"server": created})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	servers, err := h.svc.List(r.Context(), m, session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"servers": servers})
}

func (h *Handler) show(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	found, err := h.svc.Get(r.Context(), m, session.User.ID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"server": found})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), m, session.User.ID, id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// events returns progress since a moment, so a client watching a setup can poll for what is
// new rather than refetching the whole history.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	since := time.Time{}
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.Fail(w, r, httpx.InvalidInput("We could not read that request. Please try again.").WithCause(err))
			return
		}
		since = parsed
	}

	events, err := h.svc.Events(r.Context(), m, session.User.ID, id, since)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) resources(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	resources, err := h.svc.Resources(r.Context(), m, session.User.ID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"resources": resources})
}

// member resolves the caller's standing in the organization named in the path.
func (h *Handler) member(w http.ResponseWriter, r *http.Request) (*org.Membership, *auth.Session, bool) {
	session := auth.SessionFrom(r.Context())
	m, err := h.orgs.Resolve(r.Context(), session.User.ID, r.PathValue("slug"))
	if err != nil {
		httpx.Fail(w, r, err)
		return nil, nil, false
	}
	return m, session, true
}

// pathUUID reads an identifier, reporting not found for a malformed one so the response
// cannot be used to probe the shape of identifiers.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.Fail(w, r, httpx.NotFound("server").WithCause(err))
		return uuid.Nil, false
	}
	return id, true
}
