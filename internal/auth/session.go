package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/config"
)

// CookieName is the browser session cookie. Native clients send the same token as a
// bearer credential instead.
const CookieName = "yol_session"

const sessionTokenBytes = 32

// NewSessionToken returns a token for the client and the hash to store. Only the hash is
// persisted, so a database leak cannot be replayed as a login.
func NewSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken derives the stored form of a session token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// TokenFromRequest reads the session token from the cookie or the Authorization header,
// so browsers and native clients share one session mechanism.
func TokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

// SetCookie issues the session cookie. HttpOnly keeps the token away from scripts, so an
// injected script cannot steal a session.
func SetCookie(w http.ResponseWriter, cfg *config.API, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Domain:   cfg.CookieDomain,
		Expires:  expires,
		HttpOnly: true,
		Secure:   cfg.Env != config.EnvDevelopment,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the session cookie on sign out.
func ClearCookie(w http.ResponseWriter, cfg *config.API) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Domain:   cfg.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cfg.Env != config.EnvDevelopment,
		SameSite: http.SameSiteLaxMode,
	})
}
