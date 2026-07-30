package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Service holds the account and session rules. Every rule lives here rather than in any
// client, so a future mobile or command line client behaves identically.
type Service struct {
	pool *db.Pool
	cfg  *config.API
}

// NewService builds the authentication service.
func NewService(pool *db.Pool, cfg *config.API) *Service {
	return &Service{pool: pool, cfg: cfg}
}

// User is the account as clients see it.
type User struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	EmailVerified bool      `json:"emailVerified"`
}

// Session is an authenticated session with the account attached.
type Session struct {
	User      User
	ExpiresAt time.Time
}

// SignupInput is a new account request.
type SignupInput struct {
	Email    string
	Password string
	Name     string
	Client   ClientInfo
}

// LoginInput is a sign in request.
type LoginInput struct {
	Email    string
	Password string
	Client   ClientInfo
}

// ClientInfo describes the caller, recorded against the session.
type ClientInfo struct {
	UserAgent string
	IP        string
}

// Credential is a new session handed to the caller. Token is returned once and never stored.
type Credential struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

// Signup validates the request, creates the account, and starts a session.
func (s *Service) Signup(ctx context.Context, in SignupInput) (*Credential, error) {
	in.Email = NormalizeEmail(in.Email)
	if err := firstError(ValidateName(in.Name), ValidateEmail(in.Email), ValidatePassword(in.Password)); err != nil {
		return nil, err
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	var cred *Credential
	err = s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		user, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			ID:           uuid.New(),
			Email:        in.Email,
			Name:         strings.TrimSpace(in.Name),
			PasswordHash: hash,
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("An account already uses that email address. Try signing in instead.").
					WithField("email", "This email address is already registered.")
			}
			return httpx.Internal(err)
		}
		cred, err = s.startSession(ctx, q, toUser(user), in.Client)
		return err
	})
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// Login verifies credentials and starts a session.
func (s *Service) Login(ctx context.Context, in LoginInput) (*Credential, error) {
	in.Email = NormalizeEmail(in.Email)
	if in.Email == "" || in.Password == "" {
		return nil, httpx.CredentialsFailed()
	}

	var cred *Credential
	err := s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		user, err := q.GetUserByEmail(ctx, in.Email)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Same failure as a wrong password, so this cannot enumerate accounts.
				return httpx.CredentialsFailed().WithCause(err)
			}
			return httpx.Internal(err)
		}

		ok, err := VerifyPassword(in.Password, user.PasswordHash)
		if err != nil {
			return httpx.Internal(err)
		}
		if !ok {
			return httpx.CredentialsFailed()
		}

		cred, err = s.startSession(ctx, q, toUser(user), in.Client)
		return err
	})
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// Logout ends the given session. An unknown token is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		if err := sqlc.New(tx).DeleteSession(ctx, HashToken(token)); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
}

// Authenticate resolves a token to a session, or reports that signing in is required.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, httpx.NotAuthenticated()
	}

	var out *Session
	err := s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetSessionWithUser(ctx, HashToken(token))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotAuthenticated().WithCause(err)
			}
			return httpx.Internal(err)
		}
		if err := q.TouchSession(ctx, row.Session.TokenHash); err != nil {
			return httpx.Internal(err)
		}
		out = &Session{
			User: User{
				ID:            row.UserID,
				Email:         row.Email,
				Name:          row.Name,
				EmailVerified: row.EmailVerifiedAt.Valid,
			},
			ExpiresAt: row.Session.ExpiresAt.Time,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// startSession issues a token and stores only its hash.
func (s *Service) startSession(ctx context.Context, q *sqlc.Queries, user User, client ClientInfo) (*Credential, error) {
	token, hash, err := NewSessionToken()
	if err != nil {
		return nil, httpx.Internal(err)
	}
	expires := time.Now().Add(s.cfg.SessionTTL)

	if _, err := q.CreateSession(ctx, sqlc.CreateSessionParams{
		TokenHash: hash,
		UserID:    user.ID,
		UserAgent: truncate(client.UserAgent, 400),
		Ip:        parseIP(client.IP),
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	}); err != nil {
		return nil, httpx.Internal(err)
	}

	return &Credential{Token: token, ExpiresAt: expires, User: user}, nil
}

func toUser(u sqlc.User) User {
	return User{ID: u.ID, Email: u.Email, Name: u.Name, EmailVerified: u.EmailVerifiedAt.Valid}
}

// parseIP tolerates a missing or unparseable address rather than failing the request.
func parseIP(s string) *netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ClientFrom reads the caller details recorded against a session.
func ClientFrom(r *http.Request) ClientInfo {
	return ClientInfo{UserAgent: r.UserAgent(), IP: clientIP(r)}
}

// clientIP takes the socket address, ignoring forwarding headers because those are
// caller-supplied and trusting them would let anyone forge an address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstError(errs ...*httpx.Error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
