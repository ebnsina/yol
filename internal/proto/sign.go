package proto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Specifications are signed by the control plane and verified by the agent, so control of the
// transport alone is not enough to run containers on a customer's machine.
//
// The signature is asymmetric on purpose. A shared secret would have to be copied onto every
// server, and one compromised machine could then forge instructions for all of them. Agents
// hold only the public half, which is not a secret at all.
type SignedSpec struct {
	// The exact bytes that were signed. Kept raw so verification cannot disagree with parsing
	// over how a field is spelled or ordered.
	Spec      json.RawMessage `json:"spec"`
	Signature string          `json:"signature"`
	// Which key signed it, so a key can be replaced without every agent failing at once.
	KeyID string `json:"keyId,omitempty"`
}

// SignedMessage is any other instruction carrying a signature, used for those that are not a
// specification. A build request is one: it hands over a credential and causes code to run, so it
// is checked exactly as carefully.
type SignedMessage struct {
	// The exact bytes that were signed, kept raw for the same reason as in a specification.
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
	KeyID     string          `json:"keyId,omitempty"`
}

// ErrBadSignature means the instruction was not signed by the expected key, or was altered
// after signing. The two are indistinguishable, which is the point.
var ErrBadSignature = errors.New("proto: signature does not verify")

// SigningKey signs specifications. Held only by the control plane.
type SigningKey struct {
	private ed25519.PrivateKey
	id      string
}

// NewSigningKey builds a signing key from a 32-byte seed.
func NewSigningKey(seed []byte) (*SigningKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("proto: signing seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	private := ed25519.NewKeyFromSeed(seed)
	return &SigningKey{
		private: private,
		// Derived from the public half, so the same seed always names the same key.
		id: base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))[:12],
	}, nil
}

// PublicKey returns the half agents need, safe to write onto a customer's server.
func (k *SigningKey) PublicKey() string {
	return base64.RawStdEncoding.EncodeToString(k.private.Public().(ed25519.PublicKey))
}

// ID names this key.
func (k *SigningKey) ID() string { return k.id }

// Sign encodes and signs a specification.
func (k *SigningKey) Sign(spec Spec) (*SignedSpec, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("proto: encode specification: %w", err)
	}
	return &SignedSpec{
		Spec:      encoded,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(k.private, encoded)),
		KeyID:     k.id,
	}, nil
}

// SignMessage encodes and signs an instruction that is not a specification.
func (k *SigningKey) SignMessage(payload any) (*SignedMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("proto: encode instruction: %w", err)
	}
	return &SignedMessage{
		Payload:   encoded,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(k.private, encoded)),
		KeyID:     k.id,
	}, nil
}

// Verifier checks specifications. Held by agents, and contains no secret.
type Verifier struct {
	public ed25519.PublicKey
}

// NewVerifier builds a verifier from the encoded public key written during setup.
func NewVerifier(encoded string) (*Verifier, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("proto: read public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("proto: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return &Verifier{public: raw}, nil
}

// Verify checks the signature and returns the specification. An unverified specification is
// never parsed into anything that could be acted on.
func (v *Verifier) Verify(signed *SignedSpec) (*Spec, error) {
	signature, err := base64.RawStdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return nil, ErrBadSignature
	}
	if !ed25519.Verify(v.public, signed.Spec, signature) {
		return nil, ErrBadSignature
	}

	var spec Spec
	if err := json.Unmarshal(signed.Spec, &spec); err != nil {
		return nil, fmt.Errorf("proto: decode specification: %w", err)
	}
	return &spec, nil
}

// VerifyMessage checks the signature and only then reads the instruction into into. Nothing is
// parsed before the signature holds, so an unsigned instruction never becomes something to act on.
func (v *Verifier) VerifyMessage(signed *SignedMessage, into any) error {
	signature, err := base64.RawStdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return ErrBadSignature
	}
	if !ed25519.Verify(v.public, signed.Payload, signature) {
		return ErrBadSignature
	}
	if err := json.Unmarshal(signed.Payload, into); err != nil {
		return fmt.Errorf("proto: decode instruction: %w", err)
	}
	return nil
}
