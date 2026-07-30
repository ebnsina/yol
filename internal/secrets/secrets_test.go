package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{0xab}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := testBox(t)
	const key = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret material\n"

	sealed := box.SealString(key, ContextSSHCredential)
	if strings.Contains(string(sealed), "secret material") {
		t.Fatal("plaintext is visible in the sealed value")
	}

	opened, err := box.OpenString(sealed, ContextSSHCredential)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != key {
		t.Error("value did not survive the round trip")
	}
}

func TestNewRejectsWrongKeyLength(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := New(bytes.Repeat([]byte{1}, size)); err == nil {
			t.Errorf("New accepted a %d-byte key", size)
		}
	}
}

// Sealing the same value twice must produce different bytes, or the nonce is not doing its job.
func TestSealIsNotDeterministic(t *testing.T) {
	box := testBox(t)
	first := box.SealString("same value", ContextEnvVar)
	second := box.SealString("same value", ContextEnvVar)

	if bytes.Equal(first, second) {
		t.Error("two seals of the same value are identical")
	}
}

// A value sealed for one purpose must not open as another, so a stored SSH key cannot be
// read back through a path meant for environment variables.
func TestContextIsAuthenticated(t *testing.T) {
	box := testBox(t)
	sealed := box.SealString("a private key", ContextSSHCredential)

	if _, err := box.OpenString(sealed, ContextEnvVar); !errors.Is(err, ErrTampered) {
		t.Errorf("opening with the wrong context returned %v, want ErrTampered", err)
	}
}

func TestOpenRejectsAlteredBytes(t *testing.T) {
	box := testBox(t)
	sealed := box.SealString("a private key", ContextSSHCredential)

	cases := map[string][]byte{
		"flipped byte in ciphertext": append(append([]byte{}, sealed[:len(sealed)-1]...), sealed[len(sealed)-1]^0xff),
		"flipped byte in nonce":      append([]byte{sealed[0] ^ 0xff}, sealed[1:]...),
		"truncated":                  sealed[:len(sealed)-4],
		"too short":                  sealed[:4],
		"empty":                      {},
	}
	for name, altered := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := box.Open(altered, ContextSSHCredential); !errors.Is(err, ErrTampered) {
				t.Errorf("Open returned %v, want ErrTampered", err)
			}
		})
	}
}

func TestOpenRejectsAnotherKey(t *testing.T) {
	mine := testBox(t)
	theirs, err := New(bytes.Repeat([]byte{0xcd}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sealed := mine.SealString("a private key", ContextSSHCredential)
	if _, err := theirs.Open(sealed, ContextSSHCredential); !errors.Is(err, ErrTampered) {
		t.Errorf("a different key opened the value: %v", err)
	}
}

func TestSealHandlesEmptyAndLargeValues(t *testing.T) {
	box := testBox(t)
	for name, value := range map[string][]byte{
		"empty": {},
		"large": bytes.Repeat([]byte("x"), 1<<16),
	} {
		t.Run(name, func(t *testing.T) {
			opened, err := box.Open(box.Seal(value, ContextEnvVar), ContextEnvVar)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(opened, value) {
				t.Error("value did not survive the round trip")
			}
		})
	}
}
