//go:build e2e

// End-to-end checks against a running API, driven the way any client would drive it.
// Run with `make test-e2e`, which starts the database and the API first.
package api_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

const password = "a-long-enough-passphrase"

type client struct {
	t       *testing.T
	baseURL string
}

type response struct {
	Status int
	Body   map[string]any
}

// errorField reads a value out of the error envelope.
func (r response) errorField(key string) string {
	envelope, ok := r.Body["error"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := envelope[key].(string)
	return value
}

func (r response) errorFields() map[string]any {
	envelope, ok := r.Body["error"].(map[string]any)
	if !ok {
		return nil
	}
	fields, _ := envelope["fields"].(map[string]any)
	return fields
}

func newClient(t *testing.T) *client {
	t.Helper()
	base := os.Getenv("YOL_E2E_URL")
	if base == "" {
		t.Skip("YOL_E2E_URL not set")
	}
	return &client{t: t, baseURL: strings.TrimSuffix(base, "/")}
}

func (c *client) do(method, path string, body any, opts ...func(*http.Request)) response {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}

	out := response{Status: res.StatusCode}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Body); err != nil {
			c.t.Fatalf("%s %s returned unparseable body: %s", method, path, raw)
		}
	}
	return out
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// asBrowser makes the request look like it came from the web client.
func asBrowser() func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", "http://localhost:5173") }
}

// uniqueEmail keeps repeated runs against the same database from colliding.
func uniqueEmail(t *testing.T, prefix string) string {
	t.Helper()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate unique address: %v", err)
	}
	return fmt.Sprintf("%s-%x@example.com", prefix, suffix)
}

func (c *client) signup(email, name string) string {
	c.t.Helper()
	res := c.do(http.MethodPost, "/v1/auth/signup", map[string]string{
		"email": email, "password": password, "name": name,
	})
	if res.Status != http.StatusCreated {
		c.t.Fatalf("signup %s: status %d, body %v", email, res.Status, res.Body)
	}
	token, _ := res.Body["token"].(string)
	if token == "" {
		c.t.Fatalf("signup %s returned no token", email)
	}
	return token
}

func (c *client) createOrg(token, name string) map[string]any {
	c.t.Helper()
	res := c.do(http.MethodPost, "/v1/organizations", map[string]string{"name": name}, bearer(token))
	if res.Status != http.StatusCreated {
		c.t.Fatalf("create organization: status %d, body %v", res.Status, res.Body)
	}
	org, _ := res.Body["organization"].(map[string]any)
	return org
}

func TestAccountsAndSessions(t *testing.T) {
	c := newClient(t)
	email := uniqueEmail(t, "person")
	token := c.signup(email, "Test Person")

	t.Run("session resolves to the right account", func(t *testing.T) {
		res := c.do(http.MethodGet, "/v1/auth/me", nil, bearer(token))
		user, _ := res.Body["user"].(map[string]any)
		if user["email"] != email {
			t.Errorf("email = %v, want %q", user["email"], email)
		}
	})

	t.Run("no credentials is rejected", func(t *testing.T) {
		res := c.do(http.MethodGet, "/v1/auth/me", nil)
		if res.Status != http.StatusUnauthorized || res.errorField("code") != "not_authenticated" {
			t.Errorf("status %d code %q", res.Status, res.errorField("code"))
		}
	})

	// The endpoint must not be usable to discover which addresses have accounts.
	t.Run("wrong password and unknown account are indistinguishable", func(t *testing.T) {
		wrong := c.do(http.MethodPost, "/v1/auth/login", map[string]string{"email": email, "password": "not-the-password"})
		missing := c.do(http.MethodPost, "/v1/auth/login", map[string]string{"email": uniqueEmail(t, "nobody"), "password": "not-the-password"})

		if wrong.Status != missing.Status {
			t.Errorf("statuses differ: %d vs %d", wrong.Status, missing.Status)
		}
		if wrong.errorField("message") != missing.errorField("message") {
			t.Errorf("messages differ:\n  %q\n  %q", wrong.errorField("message"), missing.errorField("message"))
		}
	})

	t.Run("duplicate signup is refused with a field message", func(t *testing.T) {
		res := c.do(http.MethodPost, "/v1/auth/signup", map[string]string{
			"email": email, "password": password, "name": "Impostor",
		})
		if res.Status != http.StatusConflict {
			t.Errorf("status = %d, want 409", res.Status)
		}
		if _, ok := res.errorFields()["email"]; !ok {
			t.Error("no message attached to the email field")
		}
	})

	t.Run("signing out invalidates the token", func(t *testing.T) {
		short := c.signup(uniqueEmail(t, "shortlived"), "Short Lived")
		if res := c.do(http.MethodPost, "/v1/auth/logout", nil, bearer(short)); res.Status != http.StatusNoContent {
			t.Fatalf("logout status = %d", res.Status)
		}
		if res := c.do(http.MethodGet, "/v1/auth/me", nil, bearer(short)); res.Status != http.StatusUnauthorized {
			t.Errorf("token still valid after signing out, status %d", res.Status)
		}
	})
}

// Browsers must never receive the token in the body; it belongs in a cookie scripts cannot read.
func TestCredentialDeliveryDependsOnClient(t *testing.T) {
	c := newClient(t)
	email := uniqueEmail(t, "delivery")
	c.signup(email, "Delivery Test")
	creds := map[string]string{"email": email, "password": password}

	browser := c.do(http.MethodPost, "/v1/auth/login", creds, asBrowser())
	if _, present := browser.Body["token"]; present {
		t.Error("browser request received a token in the body")
	}

	other := c.do(http.MethodPost, "/v1/auth/login", creds)
	if token, _ := other.Body["token"].(string); token == "" {
		t.Error("non-browser client received no bearer token")
	}
}

func TestOrganizationsAreIsolated(t *testing.T) {
	c := newClient(t)
	owner := c.signup(uniqueEmail(t, "owner"), "Owner Person")
	outsider := c.signup(uniqueEmail(t, "outsider"), "Outsider Person")

	org := c.createOrg(owner, "Acme Engineering")
	slug, _ := org["slug"].(string)

	if org["role"] != "owner" {
		t.Errorf("role = %v, want owner", org["role"])
	}
	permissions, _ := org["permissions"].(map[string]any)
	if permissions["manageMembers"] != true {
		t.Error("owner was not granted member management")
	}

	// Reporting not found rather than forbidden keeps responses from confirming existence.
	for _, path := range []string{"", "/members", "/invitations"} {
		res := c.do(http.MethodGet, "/v1/organizations/"+slug+path, nil, bearer(outsider))
		if res.Status != http.StatusNotFound || res.errorField("code") != "not_found" {
			t.Errorf("%s: status %d code %q, want 404 not_found", path, res.Status, res.errorField("code"))
		}
	}

	res := c.do(http.MethodPatch, "/v1/organizations/"+slug, map[string]string{"name": "Hijacked"}, bearer(outsider))
	if res.Status != http.StatusNotFound {
		t.Errorf("outsider rename status = %d, want 404", res.Status)
	}

	c.createOrg(outsider, "Other Company")
	listed := c.do(http.MethodGet, "/v1/organizations", nil, bearer(outsider))
	orgs, _ := listed.Body["organizations"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("outsider sees %d organizations, want 1", len(orgs))
	}
	if first, _ := orgs[0].(map[string]any); first["name"] != "Other Company" {
		t.Errorf("outsider sees %v", first["name"])
	}
}

func TestInvitationLifecycle(t *testing.T) {
	c := newClient(t)
	owner := c.signup(uniqueEmail(t, "inviter"), "Inviter Person")
	inviteeEmail := uniqueEmail(t, "invitee")
	invitee := c.signup(inviteeEmail, "Invitee Person")

	org := c.createOrg(owner, "Acme Engineering")
	slug, _ := org["slug"].(string)

	created := c.do(http.MethodPost, "/v1/organizations/"+slug+"/invitations",
		map[string]string{"email": inviteeEmail, "role": "member"}, bearer(owner))
	if created.Status != http.StatusCreated {
		t.Fatalf("invite status = %d, body %v", created.Status, created.Body)
	}
	inviteURL, _ := created.Body["inviteUrl"].(string)
	token := inviteURL[strings.LastIndex(inviteURL, "/")+1:]

	t.Run("readable while signed out, revealing only what is needed", func(t *testing.T) {
		res := c.do(http.MethodGet, "/v1/invitations/"+token, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d", res.Status)
		}
		invitation, _ := res.Body["invitation"].(map[string]any)
		if invitation["needsSignIn"] != true {
			t.Error("preview should report that signing in is needed")
		}
		for key := range invitation {
			switch key {
			case "organizationName", "organizationSlug", "email", "role", "matchesCurrentAccount", "needsSignIn":
			default:
				t.Errorf("preview exposes unexpected field %q", key)
			}
		}
	})

	t.Run("a forwarded link cannot be redeemed by someone else", func(t *testing.T) {
		res := c.do(http.MethodPost, "/v1/invitations/"+token+"/accept", nil, bearer(owner))
		if res.Status != http.StatusForbidden {
			t.Errorf("status = %d, want 403", res.Status)
		}
	})

	t.Run("the invited account accepts, once only", func(t *testing.T) {
		if res := c.do(http.MethodPost, "/v1/invitations/"+token+"/accept", nil, bearer(invitee)); res.Status != http.StatusOK {
			t.Fatalf("accept status = %d, body %v", res.Status, res.Body)
		}
		if res := c.do(http.MethodPost, "/v1/invitations/"+token+"/accept", nil, bearer(invitee)); res.Status != http.StatusNotFound {
			t.Errorf("reuse status = %d, want 404", res.Status)
		}
	})

	t.Run("both people then see the same members", func(t *testing.T) {
		ownerView := c.do(http.MethodGet, "/v1/organizations/"+slug+"/members", nil, bearer(owner))
		memberView := c.do(http.MethodGet, "/v1/organizations/"+slug+"/members", nil, bearer(invitee))

		ownerEmails := memberEmails(t, ownerView)
		memberEmailList := memberEmails(t, memberView)
		if len(ownerEmails) != 2 {
			t.Fatalf("owner sees %d members, want 2", len(ownerEmails))
		}
		if strings.Join(ownerEmails, ",") != strings.Join(memberEmailList, ",") {
			t.Errorf("views differ:\n  owner:  %v\n  member: %v", ownerEmails, memberEmailList)
		}
	})

	// The client is told what it may do, so it never has to work the rules out itself.
	t.Run("a member is told it cannot invite, and is refused if it tries", func(t *testing.T) {
		res := c.do(http.MethodGet, "/v1/organizations/"+slug, nil, bearer(invitee))
		org, _ := res.Body["organization"].(map[string]any)
		permissions, _ := org["permissions"].(map[string]any)
		if permissions["manageMembers"] != false {
			t.Error("member was told it can manage members")
		}
		if permissions["deploy"] != true {
			t.Error("member should be allowed to deploy")
		}

		attempt := c.do(http.MethodPost, "/v1/organizations/"+slug+"/invitations",
			map[string]string{"email": uniqueEmail(t, "third"), "role": "member"}, bearer(invitee))
		if attempt.Status != http.StatusForbidden {
			t.Errorf("member invite status = %d, want 403", attempt.Status)
		}
	})
}

func TestLastOwnerIsProtected(t *testing.T) {
	c := newClient(t)
	owner := c.signup(uniqueEmail(t, "soleowner"), "Sole Owner")
	org := c.createOrg(owner, "Acme Engineering")
	slug, _ := org["slug"].(string)

	members := c.do(http.MethodGet, "/v1/organizations/"+slug+"/members", nil, bearer(owner))
	list, _ := members.Body["members"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected one member, got %d", len(list))
	}
	only, _ := list[0].(map[string]any)
	if only["canBeRemoved"] != false {
		t.Error("the only owner is reported as removable, which the API then refuses")
	}
	userID, _ := only["userId"].(string)

	removed := c.do(http.MethodDelete, "/v1/organizations/"+slug+"/members/"+userID, nil, bearer(owner))
	if removed.Status != http.StatusConflict {
		t.Errorf("remove status = %d, want 409", removed.Status)
	}
	demoted := c.do(http.MethodPatch, "/v1/organizations/"+slug+"/members/"+userID,
		map[string]string{"role": "member"}, bearer(owner))
	if demoted.Status != http.StatusConflict {
		t.Errorf("demote status = %d, want 409", demoted.Status)
	}
}

// Nothing internal may reach a client, whatever goes wrong.
func TestErrorEnvelopeLeaksNothing(t *testing.T) {
	c := newClient(t)
	res := c.do(http.MethodGet, "/v1/nothing-here", nil)

	if res.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Status)
	}
	if res.errorField("message") == "" {
		t.Error("no message for the client to display")
	}
	if res.errorField("requestId") == "" {
		t.Error("no request id, so a user cannot quote one to support")
	}

	encoded, err := json.Marshal(res.Body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	body := strings.ToLower(string(encoded))
	for _, leak := range []string{"postgres", "pq:", "sql", ".go:", "goroutine", "panic", "internal/", "0x"} {
		if strings.Contains(body, leak) {
			t.Errorf("response contains %q: %s", leak, encoded)
		}
	}
}

func TestInvalidInputCarriesFieldMessages(t *testing.T) {
	c := newClient(t)
	owner := c.signup(uniqueEmail(t, "validate"), "Validate Person")

	cases := map[string]struct {
		path, field string
		body        map[string]string
	}{
		"blank organization name": {"/v1/organizations", "name", map[string]string{"name": "   "}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := c.do(http.MethodPost, tc.path, tc.body, bearer(owner))
			if res.Status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.Status)
			}
			if _, ok := res.errorFields()[tc.field]; !ok {
				t.Errorf("no message on field %q: %v", tc.field, res.Body)
			}
		})
	}
}

func memberEmails(t *testing.T, res response) []string {
	t.Helper()
	list, _ := res.Body["members"].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		member, _ := item.(map[string]any)
		email, _ := member["email"].(string)
		out = append(out, email)
	}
	// Sorted so two views can be compared directly.
	slices.Sort(out)
	return out
}
