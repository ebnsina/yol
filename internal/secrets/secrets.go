// Package secrets encrypts values that must be stored but never readable from the database
// alone, such as a customer's SSH key and their environment variables.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// ErrTampered means the value did not decrypt, because the key is wrong or the bytes were
// altered. The two are indistinguishable by design.
var ErrTampered = errors.New("secrets: value could not be decrypted")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create aead: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce and ciphertext together. context is authenticated
// but not stored, so a value sealed for one purpose cannot be opened as another.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	// rand.Read cannot fail in Go's implementation; a nonce collision here would be worse
	// than a panic, and there is no sensible way to continue.
	if _, err := rand.Read(nonce); err != nil {
		panic("secrets: cannot generate nonce: " + err.Error())
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context))
}

// SealString is Seal for text values.
func (b *Box) SealString(plaintext, context string) []byte {
	return b.Seal([]byte(plaintext), context)
}

// Open decrypts a value sealed with the same key and context.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	nonceSize := b.aead.NonceSize()
	if len(sealed) < nonceSize {
		return nil, ErrTampered
	}
	nonce, ciphertext := sealed[:nonceSize], sealed[nonceSize:]

	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return nil, ErrTampered
	}
	return plaintext, nil
}

// OpenString is Open for text values.
func (b *Box) OpenString(sealed []byte, context string) (string, error) {
	plaintext, err := b.Open(sealed, context)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// Contexts used across the codebase. Keeping them here stops two call sites from
// disagreeing about a string and silently failing to decrypt.
const (
	ContextSSHCredential = "ssh-credential"
	ContextEnvVar        = "env-var"
	ContextServiceSecret = "service-secret"
)
