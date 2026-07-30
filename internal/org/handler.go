package org

import (
	"net/http"
	"net/url"

	"github.com/ebnsina/yol/internal/auth"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
)

// Handler exposes the organization endpoints.
type Handler struct {
	svc      *Service
	cfg      *config.API
	required httpx.Middleware
	optional httpx.Middleware
}

// NewHandler builds the organization endpoints. required rejects unauthenticated callers;
// optional attaches a session when one is present.
func NewHandler(svc *Service, cfg *config.API, required, optional httpx.Middleware) *Handler {
	return &Handler{svc: svc, cfg: cfg, required: required, optional: optional}
}

// Routes registers the organization endpoints.
func (h *Handler) Routes(mux *http.ServeMux) {
	get := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, h.required(fn))
	}
	get("GET /v1/organizations", h.list)
	get("POST /v1/organizations", h.create)
	get("GET /v1/organizations/{slug}", h.show)
	get("PATCH /v1/organizations/{slug}", h.rename)
	get("GET /v1/organizations/{slug}/members", h.listMembers)
	get("PATCH /v1/organizations/{slug}/members/{userId}", h.changeRole)
	get("DELETE /v1/organizations/{slug}/members/{userId}", h.removeMember)
	get("GET /v1/organizations/{slug}/invitations", h.listInvitations)
	get("POST /v1/organizations/{slug}/invitations", h.invite)
	get("DELETE /v1/organizations/{slug}/invitations/{id}", h.revoke)
	get("POST /v1/invitations/{token}/accept", h.accept)

	// Readable while signed out, so an invitee can see who invited them before signing in.
	mux.Handle("GET /v1/invitations/{token}", h.optional(http.HandlerFunc(h.previewInvitation)))
}

type nameRequest struct {
	Name string `json:"name"`
}

type inviteRequest struct {
	Email string `json:"email"`
	Role  Role   `json:"role"`
}

type roleRequest struct {
	Role Role `json:"role"`
}

// invitationResponse includes the link, since the API does not send email yet.
type invitationResponse struct {
	Invitation Invitation `json:"invitation"`
	InviteURL  string     `json:"inviteUrl"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	session := auth.SessionFrom(r.Context())
	orgs, err := h.svc.ListForUser(r.Context(), session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"organizations": orgs})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	session := auth.SessionFrom(r.Context())

	created, err := h.svc.Create(r.Context(), session.User.ID, req.Name)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"organization": created})
}

func (h *Handler) show(w http.ResponseWriter, r *http.Request) {
	m, _, ok := h.member(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"organization": Organization{
		ID: m.OrgID, Name: m.Name, Slug: m.Slug,
		Role: m.Role, Permissions: PermissionsFor(m.Role),
	}})
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	var req nameRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	updated, err := h.svc.Rename(r.Context(), m, session.User.ID, req.Name)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"organization": updated})
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	members, err := h.svc.ListMembers(r.Context(), m, session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"members": members})
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	targetID, ok := h.pathUUID(w, r, "userId", "member")
	if !ok {
		return
	}
	var req roleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	member, err := h.svc.ChangeRole(r.Context(), m, session.User.ID, targetID, req.Role)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"member": member})
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	targetID, ok := h.pathUUID(w, r, "userId", "member")
	if !ok {
		return
	}
	if err := h.svc.RemoveMember(r.Context(), m, session.User.ID, targetID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	invites, err := h.svc.ListInvitations(r.Context(), m, session.User.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"invitations": invites})
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	var req inviteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	invitation, token, err := h.svc.Invite(r.Context(), m, session.User.ID, req.Email, req.Role)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, invitationResponse{
		Invitation: *invitation,
		InviteURL:  h.inviteURL(token),
	})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "id", "invitation")
	if !ok {
		return
	}
	if err := h.svc.Revoke(r.Context(), m, session.User.ID, id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) previewInvitation(w http.ResponseWriter, r *http.Request) {
	// The preview reports whether the invitation matches whoever is signed in, if anyone.
	var email string
	if session := auth.SessionFrom(r.Context()); session != nil {
		email = session.User.Email
	}

	preview, err := h.svc.PreviewInvitation(r.Context(), r.PathValue("token"), email)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"invitation": preview})
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	session := auth.SessionFrom(r.Context())
	joined, err := h.svc.Accept(r.Context(), r.PathValue("token"), session.User.ID, session.User.Email)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"organization": joined})
}

// member resolves the caller's standing in the organization named in the path.
func (h *Handler) member(w http.ResponseWriter, r *http.Request) (*Membership, *auth.Session, bool) {
	session := auth.SessionFrom(r.Context())
	m, err := h.svc.Resolve(r.Context(), session.User.ID, r.PathValue("slug"))
	if err != nil {
		httpx.Fail(w, r, err)
		return nil, nil, false
	}
	return m, session, true
}

// pathUUID reads an identifier from the path, reporting not found for a malformed one so
// the response cannot be used to probe the shape of identifiers.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, name, noun string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.Fail(w, r, httpx.NotFound(noun).WithCause(err))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) inviteURL(token string) string {
	u := *h.cfg.WebOrigin
	u.Path = "/invitations/" + url.PathEscape(token)
	return u.String()
}
