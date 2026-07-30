package deploy

import (
	"io"
	"net/http"
	"strconv"

	"github.com/ebnsina/yol/internal/github"
	"github.com/ebnsina/yol/internal/httpx"
)

// The webhook address is reachable by anybody, so it carries no session and trusts nothing except
// the signature GitHub makes with the secret only it and we hold.

// maxDeliveryBytes bounds what is read from a delivery. GitHub's own limit is smaller than this, and
// reading an unbounded body from an address anybody can post to is not something to allow.
const maxDeliveryBytes = 5 << 20

// GitHubRoutes registers the endpoints that concern where code comes from.
func (h *Handler) GitHubRoutes(mux *http.ServeMux) {
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, h.required(fn))
	}
	const base = "/v1/organizations/{slug}/github"

	route("GET "+base, h.showCode)
	route("POST "+base+"/installations", h.connectInstallation)
	route("GET "+base+"/installations/{installation}/repositories", h.listRepositories)
	route("PUT /v1/organizations/{slug}/projects/{project}/repository", h.connectRepository)

	// Posted to by GitHub, so no session and no organization in the address.
	mux.HandleFunc("POST /v1/github/deliveries", h.delivery)
}

// showCode tells a client where to grant access and what has already been granted.
func (h *Handler) showCode(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	installations, err := h.projects.ListInstallations(r.Context(), m, session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"installUrl":    h.projects.InstallURL(),
		"installations": installations,
	})
}

type connectInstallationRequest struct {
	InstallationID string `json:"installationId"`
}

// connectInstallation records access granted on GitHub. The identifier arrives by way of the
// browser, so it is confirmed with GitHub before anything is stored.
func (h *Handler) connectInstallation(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	var req connectInstallationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	numeric, err := strconv.ParseInt(req.InstallationID, 10, 64)
	if err != nil {
		httpx.Fail(w, r, httpx.InvalidInput("That does not look like a GitHub installation.").
			WithField("installationId", "This should be the number GitHub sent back.").WithCause(err))
		return
	}

	installation, err := h.projects.ConnectInstallation(r.Context(), m, session.User.ID, numeric)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"installation": installation})
}

func (h *Handler) listRepositories(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	repositories, err := h.projects.ListRepositories(r.Context(), m, session.User.ID,
		r.PathValue("installation"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

type connectRepositoryRequest struct {
	InstallationID string `json:"installationId"`
	RepositoryID   string `json:"repositoryId"`
	FullName       string `json:"fullName"`
}

func (h *Handler) connectRepository(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "project", "project")
	if !ok {
		return
	}
	var req connectRepositoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	updated, err := h.projects.ConnectRepositoryToProject(r.Context(), m, session.User.ID, projectID,
		ConnectRepository{
			InstallationID: req.InstallationID,
			RepositoryID:   req.RepositoryID,
			FullName:       req.FullName,
		})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"project": updated})
}

// delivery takes a webhook from GitHub.
//
// Answered as accepted as soon as the signature holds, before anything is acted on, because GitHub
// gives a delivery ten seconds and a build takes minutes. What follows happens on its own.
func (h *Handler) delivery(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDeliveryBytes))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := h.projects.VerifyDelivery(body, r.Header.Get(github.SignatureHeader)); err != nil {
		// Nothing is said about why. An address anybody can post to should not help somebody work
		// out what a valid delivery looks like.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	h.projects.Deliver(r.Context(), r.Header.Get(github.EventHeader), body)
	w.WriteHeader(http.StatusAccepted)
}
