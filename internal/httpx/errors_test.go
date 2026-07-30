package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
)

func TestFailNeverLeaksTechnicalDetail(t *testing.T) {
	secret := "pq: relation \"users\" does not exist at /internal/db/query.go:88"
	err := Internal(errors.New(secret))

	rec := httptest.NewRecorder()
	Fail(rec, httptest.NewRequest(http.MethodGet, "/anything", nil), err)

	body := rec.Body.String()
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked the cause: %s", body)
	}
	for _, leak := range []string{"pq:", "relation", ".go:", "internal/db"} {
		if strings.Contains(body, leak) {
			t.Errorf("response contains %q: %s", leak, body)
		}
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestFailCollapsesUnknownErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	Fail(rec, httptest.NewRequest(http.MethodGet, "/anything", nil), errors.New("some raw failure"))

	var got errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInternal)
	}
	if strings.Contains(got.Error.Message, "some raw failure") {
		t.Errorf("message leaked raw error: %q", got.Error.Message)
	}
}

func TestFailIncludesRequestID(t *testing.T) {
	var body string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Fail(w, r, NotFound("project"))
		body = w.(*httptest.ResponseRecorder).Body.String()
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/1", nil))

	id := rec.Header().Get(requestIDHeader)
	if id == "" {
		t.Fatal("no request id header")
	}
	if !strings.Contains(body, id) {
		t.Errorf("body %q does not contain request id %q", body, id)
	}
}

// Every message a user can see must read as a plain sentence, not a code or a symbol.
func TestAllMessagesAreUserFriendly(t *testing.T) {
	all := []*Error{
		InvalidInput("Please enter a valid email address."),
		NotAuthenticated(),
		NotAuthorized(),
		NotFound("server"),
		AlreadyExists("An organization with that name already exists."),
		CredentialsFailed(),
		RateLimited(),
		Conflict("That deployment is already running."),
		Internal(errors.New("cause")),
	}

	jargon := []string{
		"nil", "null", "panic", "goroutine", "sql", "postgres", "pq:", "http ",
		"500", "400", "401", "403", "404", "token", "exception", "stack",
		"undefined", "err=", "error:", "_id", "struct", "json:",
	}

	for _, e := range all {
		t.Run(string(e.Code), func(t *testing.T) {
			m := e.Message
			if m == "" {
				t.Fatal("message is empty")
			}
			if !unicode.IsUpper(rune(m[0])) {
				t.Errorf("message does not start with a capital: %q", m)
			}
			if !strings.HasSuffix(m, ".") {
				t.Errorf("message is not a sentence: %q", m)
			}
			if strings.Contains(m, "_") {
				t.Errorf("message contains an identifier: %q", m)
			}
			lower := strings.ToLower(m)
			for _, j := range jargon {
				if strings.Contains(lower, j) {
					t.Errorf("message contains jargon %q: %q", j, m)
				}
			}
		})
	}
}

// Cross-tenant access must be indistinguishable from a genuinely missing resource.
func TestNotFoundHidesExistence(t *testing.T) {
	e := NotFound("project")
	if e.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", e.Status)
	}
	if e.Code == CodeNotAuthorized {
		t.Error("cross-tenant lookups must not report an authorization failure")
	}
}

func TestWithFieldDoesNotMutateOriginal(t *testing.T) {
	base := InvalidInput("Some details need your attention.")
	first := base.WithField("email", "Please enter a valid email address.")
	second := base.WithField("password", "Please use at least 12 characters.")

	if len(base.Fields) != 0 {
		t.Errorf("base was mutated: %v", base.Fields)
	}
	if _, ok := first.Fields["password"]; ok {
		t.Error("first clone saw the second field")
	}
	if _, ok := second.Fields["email"]; ok {
		t.Error("second clone saw the first field")
	}
}

func TestWithCauseKeepsMessageAndLogsDetail(t *testing.T) {
	cause := errors.New("dial tcp 127.0.0.1:5433: connection refused")
	e := NotFound("server").WithCause(cause)

	if !strings.Contains(e.Error(), "connection refused") {
		t.Error("Error() should include the cause for logging")
	}
	if strings.Contains(e.Message, "connection refused") {
		t.Error("Message must stay free of technical detail")
	}
	if !errors.Is(e, cause) {
		t.Error("cause should be unwrappable")
	}
}

func TestDecodeRejectsBadBodyInPlainLanguage(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	cases := map[string]string{
		"malformed":      `{"name":`,
		"empty":          ``,
		"wrong type":     `{"name": 42}`,
		"unknown field":  `{"nickname": "x"}`,
		"trailing extra": `{"name":"a"}{"name":"b"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			err := Decode(r, &payload{})
			if err == nil {
				t.Fatal("expected an error")
			}
			e := AsError(err)
			if strings.Contains(strings.ToLower(e.Message), "json") {
				t.Errorf("message mentions the format: %q", e.Message)
			}
			if e.Status >= 500 {
				t.Errorf("bad input should not be a server error, got %d", e.Status)
			}
		})
	}
}

func TestRecoverReturnsFriendlyError(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(fmt.Errorf("index out of range [5] with length 3"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "index out of range") {
		t.Errorf("panic detail leaked: %s", rec.Body.String())
	}
}
