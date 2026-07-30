// Package httpx carries the API error contract and JSON transport helpers.
package httpx

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
)

// Code is a stable machine-readable error identifier. Clients branch on it but never
// build display text from it — Error.Message is the only text users see.
type Code string

const (
	CodeInvalidInput      Code = "invalid_input"
	CodeNotAuthenticated  Code = "not_authenticated"
	CodeNotAuthorized     Code = "not_authorized"
	CodeNotFound          Code = "not_found"
	CodeAlreadyExists     Code = "already_exists"
	CodeCredentialsFailed Code = "credentials_failed"
	CodeRateLimited       Code = "rate_limited"
	CodeConflict          Code = "conflict"
	CodeInternal          Code = "internal"
)

// Error is the single source of truth for what a user is told when something fails.
// Message is plain language, written here and rendered verbatim by clients. Cause is
// the technical detail and is logged, never serialized.
type Error struct {
	Code    Code
	Status  int
	Message string
	Fields  map[string]string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// WithCause attaches technical detail for the logs without changing what users see.
func (e *Error) WithCause(err error) *Error {
	clone := *e
	clone.Cause = err
	return &clone
}

// WithField attaches a per-field message for forms, in the same plain language.
func (e *Error) WithField(name, message string) *Error {
	clone := *e
	clone.Fields = make(map[string]string, len(e.Fields)+1)
	maps.Copy(clone.Fields, e.Fields)
	clone.Fields[name] = message
	return &clone
}

// The constructors below own every message a user can be shown. Add cases here rather
// than writing message text at call sites, so wording stays consistent and auditable.

// InvalidInput reports input the user can correct, described in their terms.
func InvalidInput(message string) *Error {
	return &Error{Code: CodeInvalidInput, Status: http.StatusBadRequest, Message: message}
}

// NotAuthenticated reports a missing or expired session.
func NotAuthenticated() *Error {
	return &Error{
		Code:    CodeNotAuthenticated,
		Status:  http.StatusUnauthorized,
		Message: "Please sign in to continue.",
	}
}

// NotAuthorized reports a signed-in user acting outside their role.
func NotAuthorized() *Error {
	return &Error{
		Code:    CodeNotAuthorized,
		Status:  http.StatusForbidden,
		Message: "You do not have permission to do this. Ask an owner of this organization for access.",
	}
}

// NotFound covers both genuinely absent and forbidden-across-tenant resources, so the
// response never reveals whether something exists in another organization.
func NotFound(noun string) *Error {
	return &Error{
		Code:    CodeNotFound,
		Status:  http.StatusNotFound,
		Message: fmt.Sprintf("We could not find that %s. It may have been deleted.", noun),
	}
}

// AlreadyExists reports a uniqueness clash the user can resolve by choosing another value.
func AlreadyExists(message string) *Error {
	return &Error{Code: CodeAlreadyExists, Status: http.StatusConflict, Message: message}
}

// CredentialsFailed is deliberately vague about which half was wrong.
func CredentialsFailed() *Error {
	return &Error{
		Code:    CodeCredentialsFailed,
		Status:  http.StatusUnauthorized,
		Message: "That email and password combination does not match an account.",
	}
}

// RateLimited asks the user to wait rather than explaining the limiter.
func RateLimited() *Error {
	return &Error{
		Code:    CodeRateLimited,
		Status:  http.StatusTooManyRequests,
		Message: "Too many attempts. Please wait a moment and try again.",
	}
}

// Conflict reports a state clash, such as acting on something already in progress.
func Conflict(message string) *Error {
	return &Error{Code: CodeConflict, Status: http.StatusConflict, Message: message}
}

// Internal is the one bucket for unexpected failures. Every unrecognised error collapses
// here so no internal detail ever reaches a user.
func Internal(cause error) *Error {
	return &Error{
		Code:    CodeInternal,
		Status:  http.StatusInternalServerError,
		Message: "Something went wrong on our end. We have been notified — please try again shortly.",
		Cause:   cause,
	}
}

// AsError maps any error onto the response contract, collapsing unknown errors to Internal.
func AsError(err error) *Error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	return Internal(err)
}
