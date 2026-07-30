package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	const password = "a-long-enough-passphrase"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("hash contains the password in clear text")
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Errorf("unexpected hash format: %q", encoded)
	}

	ok, err := VerifyPassword(password, encoded)
	if err != nil || !ok {
		t.Errorf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}

	ok, err = VerifyPassword(password+"x", encoded)
	if err != nil || ok {
		t.Errorf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
	}
}

// Equal passwords must not produce equal hashes, or the salt is not doing its job.
func TestHashPasswordIsSalted(t *testing.T) {
	a, err := HashPassword("a-long-enough-passphrase")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("a-long-enough-passphrase")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"not a hash":    "hunter2",
		"wrong algo":    "$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$a2V5",
		"missing parts": "$argon2id$v=19$m=65536,t=3,p=4",
		"bad version":   "$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$a2V5",
		"bad base64":    "$argon2id$v=19$m=65536,t=3,p=4$!!!!$!!!!",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("a-long-enough-passphrase", encoded)
			if ok {
				t.Error("malformed hash verified successfully")
			}
			if err == nil {
				t.Error("expected an error for a malformed hash")
			}
		})
	}
}

// A hash written with different cost parameters must still verify, so the cost can be
// raised later without locking anyone out.
func TestVerifyPasswordHonoursStoredParameters(t *testing.T) {
	const password = "a-long-enough-passphrase"

	// Hashed with lower cost than the current constants, as an older record would be.
	weak, err := hashWithParams(password, 1, 8*1024, 1)
	if err != nil {
		t.Fatalf("hashWithParams: %v", err)
	}
	if !strings.Contains(weak, "m=8192,t=1,p=1") {
		t.Fatalf("parameters not encoded in hash: %q", weak)
	}

	ok, err := VerifyPassword(password, weak)
	if err != nil || !ok {
		t.Errorf("VerifyPassword with weaker stored parameters = %v, %v; want true, nil", ok, err)
	}
}

func TestNewSessionTokenIsUniqueAndHashed(t *testing.T) {
	seen := make(map[string]bool)
	for range 50 {
		token, hash, err := NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if seen[token] {
			t.Fatal("duplicate session token generated")
		}
		seen[token] = true

		if len(hash) != 32 {
			t.Errorf("hash length = %d, want 32", len(hash))
		}
		if string(hash) == token {
			t.Error("stored hash equals the token")
		}
		if got := HashToken(token); string(got) != string(hash) {
			t.Error("HashToken does not reproduce the stored hash")
		}
	}
}
