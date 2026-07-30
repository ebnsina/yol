package auth

import (
	"context"
	"net/http"

	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/httpx"
)

type contextKey string

const sessionKey contextKey = "session"

// Handler exposes the authentication endpoints.
type Handler struct {
	svc *Service
	cfg *config.API
}

// NewHandler builds the authentication endpoints.
func NewHandler(svc *Service, cfg *config.API) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

// Routes registers the authentication endpoints on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/signup", h.signup)
	mux.HandleFunc("POST /v1/auth/login", h.login)
	mux.HandleFunc("POST /v1/auth/logout", h.logout)
	mux.Handle("GET /v1/auth/me", h.Required(http.HandlerFunc(h.me)))
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// sessionResponse returns the account, and the token only for clients without cookies.
type sessionResponse struct {
	User  User   `json:"user"`
	Token string `json:"token,omitempty"`
}

func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	cred, err := h.svc.Signup(r.Context(), SignupInput{
		Email:    req.Email,
		Password: req.Password,
		Name:     req.Name,
		Client:   ClientFrom(r),
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.respondWithSession(w, r, cred, http.StatusCreated)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	cred, err := h.svc.Login(r.Context(), LoginInput{
		Email:    req.Email,
		Password: req.Password,
		Client:   ClientFrom(r),
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.respondWithSession(w, r, cred, http.StatusOK)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), TokenFromRequest(r)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ClearCookie(w, h.cfg)
	httpx.NoContent(w)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, sessionResponse{User: SessionFrom(r.Context()).User})
}

// respondWithSession sets the cookie for browsers and returns the token to other clients.
// Browsers get an httpOnly cookie so an injected script cannot read the token.
func (h *Handler) respondWithSession(w http.ResponseWriter, r *http.Request, cred *Credential, status int) {
	SetCookie(w, h.cfg, cred.Token, cred.ExpiresAt)

	body := sessionResponse{User: cred.User}
	if !wantsCookie(r) {
		body.Token = cred.Token
	}
	httpx.JSON(w, status, body)
}

// wantsCookie reports whether the caller is a browser, which is told to use the cookie.
func wantsCookie(r *http.Request) bool {
	return r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != ""
}

// Required rejects unauthenticated requests and puts the session in the context.
func (h *Handler) Required(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := h.svc.Authenticate(r.Context(), TokenFromRequest(r))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), session)))
	})
}

// WithSession attaches a session to the context.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}

// SessionFrom returns the session, or nil when the request is not authenticated.
func SessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey).(*Session)
	return s
}
