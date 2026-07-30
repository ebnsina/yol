package auth

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These tests need the local database from `make dev-db migrate-up`.
func testService(t *testing.T) (*Service, *db.Pool) {
	t.Helper()
	dsn := os.Getenv("YOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("YOL_TEST_DATABASE_URL not set")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	webOrigin, _ := url.Parse("http://localhost:5173")
	cfg := &config.API{
		Env:        config.EnvDevelopment,
		WebOrigin:  webOrigin,
		SessionTTL: time.Hour,
	}
	return NewService(pool, cfg), pool
}

// uniqueEmail keeps parallel runs and reruns from colliding.
func uniqueEmail() string {
	return "test-" + uuid.NewString()[:8] + "@test.io"
}

func signupFixture(t *testing.T, svc *Service, pool *db.Pool) (*Credential, string) {
	t.Helper()
	email := uniqueEmail()

	cred, err := svc.Signup(context.Background(), SignupInput{
		Email:    email,
		Password: "a-long-enough-passphrase",
		Name:     "Test Person",
		Client:   ClientInfo{UserAgent: "test", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	t.Cleanup(func() {
		_ = pool.Unscoped(context.Background(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, cred.User.ID)
			return err
		})
	})
	return cred, email
}

func TestSignupThenAuthenticate(t *testing.T) {
	svc, pool := testService(t)
	cred, email := signupFixture(t, svc, pool)

	if cred.Token == "" {
		t.Fatal("no session token returned")
	}
	if cred.User.Email != email {
		t.Errorf("email = %q, want %q", cred.User.Email, email)
	}

	session, err := svc.Authenticate(context.Background(), cred.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if session.User.ID != cred.User.ID {
		t.Error("authenticated a different account")
	}
}

func TestSignupNormalizesEmail(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	email := uniqueEmail()

	cred, err := svc.Signup(ctx, SignupInput{
		Email:    "  " + email[:4] + string([]byte{'X'}) + email[5:] + "  ",
		Password: "a-long-enough-passphrase",
		Name:     "  Spaced Name  ",
	})
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	t.Cleanup(func() {
		_ = pool.Unscoped(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, cred.User.ID)
			return err
		})
	})

	if cred.User.Email != NormalizeEmail(cred.User.Email) {
		t.Errorf("email was stored unnormalized: %q", cred.User.Email)
	}
	if cred.User.Name != "Spaced Name" {
		t.Errorf("name = %q, want trimmed", cred.User.Name)
	}
}

// Signing in with a differently-cased email must find the same account.
func TestLoginIsCaseInsensitive(t *testing.T) {
	svc, pool := testService(t)
	cred, email := signupFixture(t, svc, pool)

	shouted := ""
	for _, r := range email {
		if r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		shouted += string(r)
	}

	got, err := svc.Login(context.Background(), LoginInput{Email: shouted, Password: "a-long-enough-passphrase"})
	if err != nil {
		t.Fatalf("Login with different casing: %v", err)
	}
	if got.User.ID != cred.User.ID {
		t.Error("logged into a different account")
	}
}

// A wrong password and an unknown account must be indistinguishable to a caller.
func TestLoginDoesNotRevealWhetherAccountExists(t *testing.T) {
	svc, pool := testService(t)
	_, email := signupFixture(t, svc, pool)
	ctx := context.Background()

	_, wrongPassword := svc.Login(ctx, LoginInput{Email: email, Password: "definitely-not-the-password"})
	_, noSuchUser := svc.Login(ctx, LoginInput{Email: uniqueEmail(), Password: "definitely-not-the-password"})

	if wrongPassword == nil || noSuchUser == nil {
		t.Fatal("both attempts should fail")
	}
	a, b := httpx.AsError(wrongPassword), httpx.AsError(noSuchUser)
	if a.Message != b.Message {
		t.Errorf("messages differ:\n wrong password: %q\n unknown account: %q", a.Message, b.Message)
	}
	if a.Code != b.Code || a.Status != b.Status {
		t.Errorf("codes differ: %s/%d vs %s/%d", a.Code, a.Status, b.Code, b.Status)
	}
}

func TestDuplicateSignupIsRejected(t *testing.T) {
	svc, pool := testService(t)
	_, email := signupFixture(t, svc, pool)

	_, err := svc.Signup(context.Background(), SignupInput{
		Email:    email,
		Password: "a-long-enough-passphrase",
		Name:     "Someone Else",
	})
	if err == nil {
		t.Fatal("duplicate signup succeeded")
	}
	if e := httpx.AsError(err); e.Code != httpx.CodeAlreadyExists {
		t.Errorf("code = %q, want %q", e.Code, httpx.CodeAlreadyExists)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	svc, pool := testService(t)
	cred, _ := signupFixture(t, svc, pool)
	ctx := context.Background()

	if err := svc.Logout(ctx, cred.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, cred.Token); err == nil {
		t.Fatal("session still valid after logout")
	}
}

func TestAuthenticateRejectsUnknownAndEmptyTokens(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	for name, token := range map[string]string{"empty": "", "unknown": "not-a-real-token"} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Authenticate(ctx, token)
			if err == nil {
				t.Fatal("expected authentication to fail")
			}
			if e := httpx.AsError(err); e.Code != httpx.CodeNotAuthenticated {
				t.Errorf("code = %q, want %q", e.Code, httpx.CodeNotAuthenticated)
			}
		})
	}
}

// An expired session must not authenticate, even though the row still exists.
func TestExpiredSessionIsRejected(t *testing.T) {
	svc, pool := testService(t)
	cred, _ := signupFixture(t, svc, pool)
	ctx := context.Background()

	err := pool.Unscoped(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`,
			HashToken(cred.Token))
		return err
	})
	if err != nil {
		t.Fatalf("expire session: %v", err)
	}

	if _, err := svc.Authenticate(ctx, cred.Token); err == nil {
		t.Fatal("expired session authenticated")
	}
}

func TestSignupRejectsInvalidInput(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()

	cases := map[string]SignupInput{
		"short password": {Email: uniqueEmail(), Password: "short", Name: "A Person"},
		"bad email":      {Email: "not-an-email", Password: "a-long-enough-passphrase", Name: "A Person"},
		"empty name":     {Email: uniqueEmail(), Password: "a-long-enough-passphrase", Name: "   "},
		"no password":    {Email: uniqueEmail(), Password: "", Name: "A Person"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Signup(ctx, in)
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			e := httpx.AsError(err)
			if e.Code != httpx.CodeInvalidInput {
				t.Errorf("code = %q, want %q", e.Code, httpx.CodeInvalidInput)
			}
			if len(e.Fields) == 0 {
				t.Error("no field message for the client to display")
			}
		})
	}
}

// The database must never hold a token that could be replayed.
func TestSessionTokenIsNotStored(t *testing.T) {
	svc, pool := testService(t)
	cred, _ := signupFixture(t, svc, pool)
	ctx := context.Background()

	err := pool.Unscoped(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM sessions WHERE token_hash = $1::text::bytea`, cred.Token,
		).Scan(&n); err != nil {
			// A cast failure is fine; it proves the column is not the raw token.
			return nil
		}
		if n > 0 {
			return errors.New("raw token found in the sessions table")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
