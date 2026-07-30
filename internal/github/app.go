// Package github is how the control plane reaches a customer's code. It holds no repository
// contents and stores no credential that could read one: everything is done with tokens minted for
// a single installation and valid for an hour.
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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Provider is how this source of code is recorded against a project.
const Provider = "github"

const (
	apiBase = "https://api.github.com"
	// Sent so a future change in the API does not change what we get back without us asking.
	apiVersion = "2022-11-28"

	requestTimeout = 20 * time.Second

	// The longest GitHub accepts is ten minutes. Kept under that, and backdated, because the two
	// machines' clocks are not the same and a token from the future is refused.
	appTokenLife = 9 * time.Minute
	clockSkew    = 60 * time.Second

	// An installation token lasts an hour. It is dropped early so one is never handed out with
	// only seconds left on it, which would fail partway through a build.
	tokenMargin = 5 * time.Minute
)

// App talks to GitHub as the application itself.
type App struct {
	appID string
	slug  string
	key   *rsa.PrivateKey

	client *http.Client
	// Where requests go. Set only by tests, so they never reach the real GitHub.
	baseOverride string

	// Installation tokens are reused until they are nearly expired, so opening a screen that lists
	// repositories does not mint one every time.
	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// NewApp builds the client. The key is parsed here so a bad one stops the process at startup
// rather than at the first deploy somebody attempts.
func NewApp(appID, slug string, privateKeyPEM []byte) (*App, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &App{
		appID:  appID,
		slug:   slug,
		key:    key,
		client: &http.Client{Timeout: requestTimeout},
		tokens: make(map[int64]cachedToken),
	}, nil
}

// InstallURL is where somebody is sent to give us access to their repositories.
func (a *App) InstallURL() string {
	return "https://github.com/apps/" + a.slug + "/installations/new"
}

// parsePrivateKey accepts either of the two encodings GitHub has handed out over the years.
func parsePrivateKey(encoded []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, errors.New("github: the private key is not in PEM form")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: read the private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github: the private key is %T, and GitHub signs with RSA", parsed)
	}
	return key, nil
}

// appToken proves we are the application. Built fresh each time rather than kept: it is cheap to
// make, and one that has been sitting around is one that might have expired.
func (a *App) appToken() (string, error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		// Backdated, because GitHub refuses a token issued in the future and clocks differ.
		"iat": now.Add(-clockSkew).Unix(),
		"exp": now.Add(appTokenLife).Unix(),
		"iss": a.appID,
	}

	encodedHeader, err := encodeSegment(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := encodeSegment(claims)
	if err != nil {
		return "", err
	}

	signing := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signing))

	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign the request: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeSegment(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("github: build the request: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// InstallationToken returns a token that can read the repositories of one installation.
//
// Scoped down to reading contents and metadata, whatever the installation was granted, so a token
// that leaks from a build cannot be used to write to somebody's repository.
func (a *App) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	cached, ok := a.tokens[installationID]
	a.mu.Unlock()

	if ok && time.Until(cached.expiresAt) > tokenMargin {
		return cached.token, nil
	}

	appToken, err := a.appToken()
	if err != nil {
		return "", err
	}

	body := map[string]any{
		"permissions": map[string]string{"contents": "read", "metadata": "read"},
	}
	var answer struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	if err := a.call(ctx, http.MethodPost, path, appToken, body, &answer); err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("github: no token was returned for this installation")
	}

	a.mu.Lock()
	a.tokens[installationID] = cachedToken{token: answer.Token, expiresAt: answer.ExpiresAt}
	a.mu.Unlock()

	return answer.Token, nil
}

// Repository is one repository an installation can reach.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
}

// Repositories lists what an installation gives us access to, which is what somebody chooses from
// when connecting a project. Only what they granted appears here.
func (a *App) Repositories(ctx context.Context, installationID int64) ([]Repository, error) {
	token, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}

	out := []Repository{}
	// Paged through rather than taking the first hundred, because somebody with more than that
	// would find their repository simply missing with no way to tell why.
	for page := 1; page <= 20; page++ {
		var answer struct {
			TotalCount   int `json:"total_count"`
			Repositories []struct {
				ID            int64  `json:"id"`
				FullName      string `json:"full_name"`
				Private       bool   `json:"private"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page)
		if err := a.call(ctx, http.MethodGet, path, token, nil, &answer); err != nil {
			return nil, err
		}

		for _, repo := range answer.Repositories {
			out = append(out, Repository{
				ID:            repo.ID,
				FullName:      repo.FullName,
				Private:       repo.Private,
				DefaultBranch: repo.DefaultBranch,
			})
		}
		if len(answer.Repositories) < 100 || len(out) >= answer.TotalCount {
			break
		}
	}
	return out, nil
}

// Installation is who gave us access.
type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

// Installation reads one, which is how a callback from GitHub is checked rather than trusted.
func (a *App) Installation(ctx context.Context, installationID int64) (*Installation, error) {
	appToken, err := a.appToken()
	if err != nil {
		return nil, err
	}

	var answer struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	path := fmt.Sprintf("/app/installations/%d", installationID)
	if err := a.call(ctx, http.MethodGet, path, appToken, nil, &answer); err != nil {
		return nil, err
	}
	return &Installation{ID: answer.ID, Account: answer.Account.Login}, nil
}

// SourceURL is where one commit of a repository can be fetched as an archive. Handed to the agent
// with a token, so the code goes straight from GitHub to the customer's server and never through us.
func SourceURL(fullName, commitSHA string) string {
	return apiBase + "/repos/" + fullName + "/tarball/" + commitSHA
}

// call makes one request and reads the answer.
func (a *App) call(ctx context.Context, method, path, token string, body, into any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: build the request: %w", err)
		}
		payload = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequestWithContext(ctx, method, a.base()+path, payload)
	if err != nil {
		return fmt.Errorf("github: build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("github: reach GitHub: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		// The message GitHub sends explains far more than the status alone, and is worth keeping
		// for the log even though it is never shown to anybody.
		explanation, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("github: %s %s answered %d: %s",
			method, path, res.StatusCode, strings.TrimSpace(string(explanation)))
	}

	if into == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return fmt.Errorf("github: read the answer: %w", err)
	}
	return nil
}

// base is where requests go.
func (a *App) base() string {
	if a.baseOverride != "" {
		return a.baseOverride
	}
	return apiBase
}
