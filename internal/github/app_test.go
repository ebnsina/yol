package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKey is generated per run rather than committed, so no usable key ever sits in the repository.
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, encoded
}

// standIn answers as GitHub would, so nothing in these tests reaches the real thing.
func standIn(t *testing.T, handler http.HandlerFunc) (*App, *httptest.Server) {
	t.Helper()

	_, encoded := testKey(t)
	app, err := NewApp("123456", "yol", encoded)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	app.baseOverride = server.URL
	return app, server
}

// A key GitHub hands out in either encoding has to be accepted, since which one you get depends on
// when the app was created.
func TestBothKeyEncodingsAreAccepted(t *testing.T) {
	key, pkcs1 := testKey(t)
	if _, err := NewApp("1", "yol", pkcs1); err != nil {
		t.Errorf("the older encoding was refused: %v", err)
	}

	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
	if _, err := NewApp("1", "yol", pkcs8); err != nil {
		t.Errorf("the newer encoding was refused: %v", err)
	}
}

// A bad key must stop the process at startup rather than at the first deploy somebody attempts.
func TestSomethingThatIsNotAKeyIsRefused(t *testing.T) {
	if _, err := NewApp("1", "yol", []byte("not a key at all")); err == nil {
		t.Error("something that is not a key was accepted")
	}
}

// The token proving we are the application has to be one GitHub will accept: signed with our key,
// not issued in the future, and not lasting longer than allowed.
func TestTheTokenProvingWeAreTheApplicationVerifies(t *testing.T) {
	key, encoded := testKey(t)
	app, err := NewApp("123456", "yol", encoded)
	if err != nil {
		t.Fatal(err)
	}

	token, err := app.appToken()
	if err != nil {
		t.Fatalf("build a token: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d parts, want three", len(parts))
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("the signature is not readable: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("the token is not signed with our key: %v", err)
	}

	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	if claims.IssuedAt > now {
		t.Error("the token is issued in the future, which GitHub refuses")
	}
	if claims.ExpiresAt-claims.IssuedAt > 600 {
		t.Errorf("the token lasts %ds, and GitHub allows at most 600", claims.ExpiresAt-claims.IssuedAt)
	}
	if claims.Issuer != "123456" {
		t.Errorf("issuer = %q, want the application's own identifier", claims.Issuer)
	}
}

// A token that could write to somebody's repository is a token we have no business asking for, so
// what is requested is narrowed to reading.
func TestATokenIsAskedForWithReadingOnly(t *testing.T) {
	var asked map[string]any
	app, _ := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_example",
			"expires_at": time.Now().Add(time.Hour),
		})
	})

	token, err := app.InstallationToken(context.Background(), 42)
	if err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	if token != "ghs_example" {
		t.Errorf("token = %q, want what GitHub returned", token)
	}

	permissions, ok := asked["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("no permissions were asked for, so the token carries everything the app has")
	}
	if permissions["contents"] != "read" {
		t.Errorf("contents = %v, want read", permissions["contents"])
	}
	if _, writes := permissions["administration"]; writes {
		t.Error("more than reading was asked for")
	}
}

// Minting one on every screen that lists repositories would waste the rate limit, so a token is
// reused while it is still good for a while.
func TestATokenIsReusedWhileItIsStillGood(t *testing.T) {
	minted := 0
	app, _ := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		minted++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_example",
			"expires_at": time.Now().Add(time.Hour),
		})
	})

	ctx := context.Background()
	for range 3 {
		if _, err := app.InstallationToken(ctx, 42); err != nil {
			t.Fatal(err)
		}
	}
	if minted != 1 {
		t.Errorf("minted %d tokens for three requests, want one", minted)
	}
}

// One with only minutes left is not reused, because a build that outlasts it would fail halfway
// through with no way to tell why.
func TestATokenAboutToExpireIsReplaced(t *testing.T) {
	minted := 0
	app, _ := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		minted++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_example",
			"expires_at": time.Now().Add(30 * time.Second),
		})
	})

	ctx := context.Background()
	for range 2 {
		if _, err := app.InstallationToken(ctx, 42); err != nil {
			t.Fatal(err)
		}
	}
	if minted != 2 {
		t.Errorf("minted %d tokens, want a fresh one rather than the one about to expire", minted)
	}
}

// Somebody with more repositories than fit in one answer must still find theirs, rather than it
// being missing with nothing to explain why.
func TestEveryRepositoryIsListedAndNotJustTheFirstPage(t *testing.T) {
	app, _ := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/app/installations/") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_example", "expires_at": time.Now().Add(time.Hour),
			})
			return
		}

		page := r.URL.Query().Get("page")
		repositories := []map[string]any{}
		count := 100
		if page == "2" {
			count = 5
		}
		for i := range count {
			repositories = append(repositories, map[string]any{
				"id":             int64(1000 + i),
				"full_name":      "owner/repo-" + page + "-" + string(rune('a'+i%26)),
				"default_branch": "main",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 105, "repositories": repositories,
		})
	})

	repositories, err := app.Repositories(context.Background(), 42)
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	if len(repositories) != 105 {
		t.Errorf("listed %d repositories, want all 105", len(repositories))
	}
}

// A failure has to say what GitHub actually objected to, or a broken installation is impossible to
// diagnose from the outside.
func TestAFailureFromGitHubIsExplained(t *testing.T) {
	app, _ := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})

	_, err := app.InstallationToken(context.Background(), 42)
	if err == nil {
		t.Fatal("a refusal was treated as success")
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error = %v, want it to carry what GitHub said", err)
	}
}

// The archive address is what the agent is handed, so it has to name the exact commit rather than a
// branch that may have moved on by the time the build starts.
func TestTheSourceAddressNamesTheExactCommit(t *testing.T) {
	url := SourceURL("owner/repo", "abcdef1234567890")

	if !strings.HasSuffix(url, "/repos/owner/repo/tarball/abcdef1234567890") {
		t.Errorf("url = %q, want it to name the commit", url)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Errorf("url = %q, want the code fetched over a protected connection", url)
	}
}
