package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A webhook address is public and anybody can post to it, so a delivery is only ever acted on once
// it carries a signature made with the secret only GitHub and we know. Without that check, anyone
// could make a customer's server deploy a commit of their choosing.

// ErrBadSignature means the delivery was not signed with our secret, or was altered on the way.
var ErrBadSignature = errors.New("github: the delivery signature does not verify")

// SignatureHeader is the header GitHub signs a delivery with.
const SignatureHeader = "X-Hub-Signature-256"

// EventHeader names what happened.
const EventHeader = "X-GitHub-Event"

// Verify checks a delivery against the secret. The comparison is made in constant time so the
// answer cannot be arrived at one character at a time.
func Verify(secret string, body []byte, signature string) error {
	expected := Sign(secret, body)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return ErrBadSignature
	}
	return nil
}

// Sign produces the signature GitHub would send for a body. Exported because a test that builds
// its own delivery needs it, and because a signature built two different ways is a signature that
// works in tests and fails in production.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Push is what a commit being pushed tells us: which repository, which branch, and what to build.
type Push struct {
	InstallationID int64
	RepositoryID   int64
	FullName       string
	Branch         string
	CommitSHA      string
	// Who pushed, for showing beside the deploy rather than for deciding anything.
	Pusher string
	// True when the branch was deleted, which is a push with nothing to build.
	Deleted bool
}

// ParsePush reads a push delivery. Only the fields that decide what to deploy are read; the rest of
// what GitHub sends is deliberately ignored.
func ParsePush(body []byte) (*Push, error) {
	var delivery struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
		Created bool   `json:"created"`

		Repository struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Pusher struct {
			Name string `json:"name"`
		} `json:"pusher"`
	}
	if err := json.Unmarshal(body, &delivery); err != nil {
		return nil, fmt.Errorf("github: read the delivery: %w", err)
	}

	// Tags and other references are not branches, so nothing follows them.
	branch, isBranch := strings.CutPrefix(delivery.Ref, "refs/heads/")
	if !isBranch || branch == "" {
		return nil, nil
	}
	if delivery.Repository.ID == 0 || delivery.Installation.ID == 0 {
		return nil, errors.New("github: the delivery names no repository or installation")
	}

	return &Push{
		InstallationID: delivery.Installation.ID,
		RepositoryID:   delivery.Repository.ID,
		FullName:       delivery.Repository.FullName,
		Branch:         branch,
		CommitSHA:      delivery.After,
		Pusher:         delivery.Pusher.Name,
		Deleted:        delivery.Deleted,
	}, nil
}

// InstallationEvent is an installation being added, removed, or having its repositories changed.
type InstallationEvent struct {
	Action         string
	InstallationID int64
	Account        string
}

// ParseInstallation reads an installation delivery, which is how access being taken away reaches us
// without waiting for a deploy to fail.
func ParseInstallation(body []byte) (*InstallationEvent, error) {
	var delivery struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &delivery); err != nil {
		return nil, fmt.Errorf("github: read the delivery: %w", err)
	}
	if delivery.Installation.ID == 0 {
		return nil, errors.New("github: the delivery names no installation")
	}

	return &InstallationEvent{
		Action:         delivery.Action,
		InstallationID: delivery.Installation.ID,
		Account:        delivery.Installation.Account.Login,
	}, nil
}
