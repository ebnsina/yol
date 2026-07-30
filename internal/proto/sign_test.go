package proto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func testSigner(t *testing.T) *SigningKey {
	t.Helper()
	key, err := NewSigningKey(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	if err != nil {
		t.Fatalf("NewSigningKey: %v", err)
	}
	return key
}

func testVerifier(t *testing.T, key *SigningKey) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(key.PublicKey())
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	signer := testSigner(t)
	verifier := testVerifier(t, signer)

	spec := Spec{
		Version:    42,
		ServerID:   "server-1",
		Containers: []SpecContainer{{Name: "app", Image: "registry/app:abc"}},
	}

	signed, err := signer.Sign(spec)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	back, err := verifier.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if back.Version != spec.Version || len(back.Containers) != 1 {
		t.Errorf("specification did not survive: %+v", back)
	}
}

// The public half is what goes onto a customer's server, so it must never be usable to sign.
func TestPublicKeyIsNotASigningKey(t *testing.T) {
	signer := testSigner(t)

	raw, err := base64.RawStdEncoding.DecodeString(signer.PublicKey())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	if bytes.Contains(signer.private, raw[:16]) && len(signer.private) == ed25519.PrivateKeySize {
		// ed25519 private keys do embed the public half, which is expected; the point is that
		// the published value alone is only half of it.
		if len(raw) == ed25519.PrivateKeySize {
			t.Error("the published key is the size of a signing key")
		}
	}
}

// Altering a specification after signing must be rejected, which is what stops reaching the
// connection from being enough to run containers on someone's machine.
func TestVerifyRejectsAlteredSpecifications(t *testing.T) {
	signer := testSigner(t)
	verifier := testVerifier(t, signer)

	signed, err := signer.Sign(Spec{Version: 1, Containers: []SpecContainer{{Name: "app", Image: "ours:1"}}})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Swap in a different image, as an attacker with the connection would.
	tampered := *signed
	tampered.Spec = json.RawMessage(
		`{"version":1,"containers":[{"name":"app","image":"theirs:evil","labels":null}],"volumes":null,"routes":null}`)

	if _, err := verifier.Verify(&tampered); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an altered specification verified: %v", err)
	}
}

func TestVerifyRejectsAnotherKey(t *testing.T) {
	ours := testSigner(t)
	theirs, err := NewSigningKey(bytes.Repeat([]byte{0x22}, ed25519.SeedSize))
	if err != nil {
		t.Fatalf("NewSigningKey: %v", err)
	}

	signed, err := theirs.Sign(Spec{Version: 1})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := testVerifier(t, ours).Verify(signed); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a specification signed by another key verified: %v", err)
	}
}

func TestVerifyRejectsMissingOrMalformedSignature(t *testing.T) {
	signer := testSigner(t)
	verifier := testVerifier(t, signer)

	signed, err := signer.Sign(Spec{Version: 1})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	for name, signature := range map[string]string{
		"empty":        "",
		"not base64":   "!!!!",
		"wrong length": base64.RawStdEncoding.EncodeToString([]byte("too short")),
	} {
		t.Run(name, func(t *testing.T) {
			broken := *signed
			broken.Signature = signature
			if _, err := verifier.Verify(&broken); !errors.Is(err, ErrBadSignature) {
				t.Errorf("verified with signature %q: %v", signature, err)
			}
		})
	}
}

func TestNewSigningKeyRejectsWrongSeedLength(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := NewSigningKey(bytes.Repeat([]byte{1}, size)); err == nil {
			t.Errorf("accepted a %d-byte seed", size)
		}
	}
}

func TestNewVerifierRejectsRubbish(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty":      "",
		"not base64": "!!!!",
		"too short":  base64.RawStdEncoding.EncodeToString([]byte("short")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewVerifier(encoded); err == nil {
				t.Error("accepted something that is not a public key")
			}
		})
	}
}

// The same seed must always name the same key, so a rotation is recognisable.
func TestKeyIDIsStable(t *testing.T) {
	first := testSigner(t)
	second := testSigner(t)

	if first.ID() != second.ID() {
		t.Errorf("same seed gave different ids: %q and %q", first.ID(), second.ID())
	}
	if first.ID() == "" {
		t.Error("key id is empty")
	}
}
