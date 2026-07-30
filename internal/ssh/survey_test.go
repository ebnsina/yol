package ssh

import (
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

// Only Ubuntu and Debian are supported, and the refusal must read as a sentence.
func TestUnsupported(t *testing.T) {
	for _, name := range []string{"Ubuntu", "Debian GNU/Linux", "ubuntu"} {
		if reason := unsupported(proto.HostFacts{OSName: name}); reason != "" {
			t.Errorf("%s was rejected: %s", name, reason)
		}
	}

	for _, name := range []string{"Alpine Linux", "CentOS Linux", "Fedora", ""} {
		reason := unsupported(proto.HostFacts{OSName: name})
		if reason == "" {
			t.Errorf("%q should not be accepted yet", name)
			continue
		}
		if reason[len(reason)-1] != '.' {
			t.Errorf("reason for %q is not a sentence: %q", name, reason)
		}
	}
}

func TestTargetAddr(t *testing.T) {
	cases := map[Target]string{
		{Host: "203.0.113.9"}:                  "203.0.113.9:22",
		{Host: "203.0.113.9", Port: 2222}:      "203.0.113.9:2222",
		{Host: "2001:db8::1", Port: 22}:        "[2001:db8::1]:22",
		{Host: "server.example.com", Port: 22}: "server.example.com:22",
	}
	for target, want := range cases {
		if got := target.Addr(); got != want {
			t.Errorf("Addr() = %q, want %q", got, want)
		}
	}
}

func TestAuthMethodsRequiresACredential(t *testing.T) {
	if _, err := authMethods(Credential{User: "root"}); err == nil {
		t.Error("no key and no password should be an error")
	}
	if _, err := authMethods(Credential{User: "root", Key: "not a key"}); err == nil {
		t.Error("an unreadable key should be an error")
	}
	if _, err := authMethods(Credential{User: "root", Password: "hunter2"}); err != nil {
		t.Errorf("a password should be accepted: %v", err)
	}
}
