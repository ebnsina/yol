package auth

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/ebnsina/yol/internal/httpx"
)

const maxNameLength = 80

// NormalizeEmail lowercases and trims so an address is stored one way only.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail reports a per-field message the client can show next to the input.
func ValidateEmail(email string) *httpx.Error {
	if email == "" {
		return fieldError("email", "Please enter your email address.")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return fieldError("email", "Please enter a valid email address.")
	}
	return nil
}

// ValidatePassword prefers length over character-class rules, which is stronger and less
// frustrating than the usual requirements.
func ValidatePassword(password string) *httpx.Error {
	if password == "" {
		return fieldError("password", "Please choose a password.")
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fieldError("password", "Please use at least 12 characters. A short phrase works well.")
	}
	if utf8.RuneCountInString(password) > 200 {
		return fieldError("password", "Please use fewer than 200 characters.")
	}
	return nil
}

// ValidateName checks the display name.
func ValidateName(name string) *httpx.Error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fieldError("name", "Please enter your name.")
	}
	if utf8.RuneCountInString(trimmed) > maxNameLength {
		return fieldError("name", "Please use a shorter name.")
	}
	return nil
}

// fieldError pairs a form-level summary with the message for the offending field.
func fieldError(field, message string) *httpx.Error {
	return httpx.InvalidInput("Please check the highlighted fields and try again.").
		WithField(field, message)
}
