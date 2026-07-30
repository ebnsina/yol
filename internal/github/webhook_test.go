package github

import (
	"errors"
	"testing"
)

const secret = "a-shared-secret"

// The address is public and anybody can post to it, so a delivery signed with anything else must be
// refused. Without this, anyone could make a customer's server deploy a commit of their choosing.
func TestADeliverySignedWithSomethingElseIsRefused(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)

	if err := Verify(secret, body, Sign("the-wrong-secret", body)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("error = %v, want the delivery refused", err)
	}
	if err := Verify(secret, body, ""); !errors.Is(err, ErrBadSignature) {
		t.Error("a delivery with no signature was accepted")
	}
	if err := Verify(secret, body, "sha256=not-hex"); !errors.Is(err, ErrBadSignature) {
		t.Error("a delivery with a nonsense signature was accepted")
	}
}

func TestAProperlySignedDeliveryIsAccepted(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)

	if err := Verify(secret, body, Sign(secret, body)); err != nil {
		t.Errorf("a properly signed delivery was refused: %v", err)
	}
}

// A body changed on the way must not verify, which is the other half of what the signature is for.
func TestAnAlteredDeliveryIsRefused(t *testing.T) {
	signature := Sign(secret, []byte(`{"after":"good-commit"}`))

	if err := Verify(secret, []byte(`{"after":"attacker-commit"}`), signature); err == nil {
		t.Error("a delivery whose contents were changed was accepted")
	}
}

const pushBody = `{
  "ref": "refs/heads/main",
  "after": "abcdef1234567890abcdef1234567890abcdef12",
  "repository": {"id": 987, "full_name": "owner/shop"},
  "installation": {"id": 42},
  "pusher": {"name": "someone"}
}`

// What a push has to tell us is which repository, which branch, and what to build.
func TestAPushSaysWhatToBuild(t *testing.T) {
	push, err := ParsePush([]byte(pushBody))
	if err != nil {
		t.Fatalf("read the delivery: %v", err)
	}
	if push == nil {
		t.Fatal("a push to a branch was ignored")
	}

	if push.Branch != "main" {
		t.Errorf("branch = %q, want the branch name without its reference prefix", push.Branch)
	}
	if push.CommitSHA != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("commit = %q, want what was pushed", push.CommitSHA)
	}
	if push.RepositoryID != 987 || push.InstallationID != 42 {
		t.Errorf("repository = %d, installation = %d, want both from the delivery",
			push.RepositoryID, push.InstallationID)
	}
	if push.FullName != "owner/shop" {
		t.Errorf("fullName = %q, want the repository it came from", push.FullName)
	}
}

// A tag is not a branch, and an environment follows a branch. Deploying on a tag would deploy on
// every release note somebody wrote.
func TestSomethingThatIsNotABranchIsIgnored(t *testing.T) {
	for _, ref := range []string{"refs/tags/v1.0.0", "refs/pull/12/merge", ""} {
		push, err := ParsePush([]byte(`{"ref":"` + ref + `","repository":{"id":1},"installation":{"id":1}}`))
		if err != nil {
			t.Fatalf("read a delivery for %s: %v", ref, err)
		}
		if push != nil {
			t.Errorf("%s was treated as a branch", ref)
		}
	}
}

// A delivery naming no repository cannot be acted on, and guessing would be worse than refusing.
func TestADeliveryMissingWhatItNeedsIsRefused(t *testing.T) {
	_, err := ParsePush([]byte(`{"ref":"refs/heads/main"}`))
	if err == nil {
		t.Error("a delivery naming no repository or installation was accepted")
	}
}

func TestSomethingThatIsNotADeliveryIsRefused(t *testing.T) {
	if _, err := ParsePush([]byte("not json at all")); err == nil {
		t.Error("something that is not a delivery was read as one")
	}
}

// A branch being deleted is a push with nothing to build, and has to be distinguishable from one
// that carries a commit.
func TestADeletedBranchIsMarkedAsSuch(t *testing.T) {
	push, err := ParsePush([]byte(`{
		"ref": "refs/heads/old-feature", "deleted": true, "after": "0000000000000000000000000000000000000000",
		"repository": {"id": 987, "full_name": "owner/shop"}, "installation": {"id": 42}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if push == nil || !push.Deleted {
		t.Error("a branch being deleted was not recognised, so a deploy of nothing would follow")
	}
}

// Access being taken away has to reach us when it happens rather than the next time a deploy fails.
func TestAccessBeingTakenAwayIsRecognised(t *testing.T) {
	event, err := ParseInstallation([]byte(`{
		"action": "deleted",
		"installation": {"id": 42, "account": {"login": "some-org"}}
	}`))
	if err != nil {
		t.Fatalf("read the delivery: %v", err)
	}
	if event.Action != "deleted" || event.InstallationID != 42 || event.Account != "some-org" {
		t.Errorf("event = %+v, want the installation and what happened to it", event)
	}
}
