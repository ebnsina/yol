package deploy

import (
	"net/http"

	"github.com/ebnsina/yol/internal/auth"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
)

// Handler exposes the project endpoints. Everything is reached through an organization the caller
// belongs to, so nothing can be addressed from outside one.
type Handler struct {
	projects *Projects
	orgs     *org.Service
	required httpx.Middleware
}

// NewHandler builds the project endpoints.
func NewHandler(projects *Projects, orgs *org.Service, required httpx.Middleware) *Handler {
	return &Handler{projects: projects, orgs: orgs, required: required}
}

// Routes registers the project endpoints.
func (h *Handler) Routes(mux *http.ServeMux) {
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, h.required(fn))
	}
	const base = "/v1/organizations/{slug}/projects"

	route("GET "+base, h.list)
	route("POST "+base, h.create)
	route("GET "+base+"/{project}", h.show)
	route("DELETE "+base+"/{project}", h.delete)

	route("PATCH "+base+"/{project}/environments/{environment}", h.updateEnvironment)
	route("PATCH "+base+"/{project}/services/{service}", h.updateService)

	// Values are write-only. There is deliberately no endpoint that returns one.
	route("GET "+base+"/{project}/environments/{environment}/variables", h.listVariables)
	route("PUT "+base+"/{project}/environments/{environment}/variables/{name}", h.setVariable)
	route("DELETE "+base+"/{project}/environments/{environment}/variables/{name}", h.deleteVariable)
}

type createProjectRequest struct {
	Name string `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	var req createProjectRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	created, err := h.projects.Create(r.Context(), m, session.User.ID, req.Name)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"project": created})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	projects, err := h.projects.List(r.Context(), m, session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (h *Handler) show(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "project", "project")
	if !ok {
		return
	}

	found, err := h.projects.Get(r.Context(), m, session.User.ID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"project": found})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "project", "project")
	if !ok {
		return
	}

	if err := h.projects.Delete(r.Context(), m, session.User.ID, id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type updateEnvironmentRequest struct {
	Branch   *string `json:"branch"`
	ServerID *string `json:"serverId"`
}

func (h *Handler) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	envID, ok := h.pathID(w, r, "environment", "environment")
	if !ok {
		return
	}
	var req updateEnvironmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	changes := EnvironmentChanges{Branch: req.Branch}
	if req.ServerID != nil {
		serverID, err := uuid.Parse(*req.ServerID)
		if err != nil {
			httpx.Fail(w, r, httpx.NotFound("server").WithCause(err))
			return
		}
		changes.ServerID = &serverID
	}

	updated, err := h.projects.UpdateEnvironment(r.Context(), m, session.User.ID, envID, changes)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"environment": updated})
}

type updateServiceRequest struct {
	HealthPath       *string `json:"healthPath"`
	HealthPort       *int    `json:"healthPort"`
	MemoryLimitBytes *int64  `json:"memoryLimitBytes"`
}

func (h *Handler) updateService(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	serviceID, ok := h.pathID(w, r, "service", "service")
	if !ok {
		return
	}
	var req updateServiceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	updated, err := h.projects.UpdateService(r.Context(), m, session.User.ID, serviceID, ServiceChanges{
		HealthPath:       req.HealthPath,
		HealthPort:       req.HealthPort,
		MemoryLimitBytes: req.MemoryLimitBytes,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"service": updated})
}

func (h *Handler) listVariables(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	envID, ok := h.pathID(w, r, "environment", "environment")
	if !ok {
		return
	}

	variables, err := h.projects.ListVariables(r.Context(), m, session.User.ID, envID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"variables": variables})
}

type setVariableRequest struct {
	Value string `json:"value"`
}

func (h *Handler) setVariable(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	envID, ok := h.pathID(w, r, "environment", "environment")
	if !ok {
		return
	}
	var req setVariableRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	err := h.projects.SetVariable(r.Context(), m, session.User.ID, envID, r.PathValue("name"), req.Value)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteVariable(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	envID, ok := h.pathID(w, r, "environment", "environment")
	if !ok {
		return
	}

	err := h.projects.DeleteVariable(r.Context(), m, session.User.ID, envID, r.PathValue("name"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// member resolves the caller's place in the organization named in the address.
func (h *Handler) member(w http.ResponseWriter, r *http.Request) (*org.Membership, *auth.Session, bool) {
	session := auth.SessionFrom(r.Context())
	m, err := h.orgs.Resolve(r.Context(), session.User.ID, r.PathValue("slug"))
	if err != nil {
		httpx.Fail(w, r, err)
		return nil, nil, false
	}
	return m, session, true
}

// pathID reads an identifier, reporting not found for a malformed one so a response cannot be used
// to probe the shape of identifiers.
func (h *Handler) pathID(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.Fail(w, r, httpx.NotFound(what).WithCause(err))
		return uuid.Nil, false
	}
	return id, true
}
