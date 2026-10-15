// Package vault encrypts card data at rest with AES-256-GCM.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type Vault struct {
	aead cipher.AEAD
}

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("vault: key must be 32 bytes (AES-256)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

// Seal encrypts plaintext with a fresh random nonce and returns
// nonce || ciphertext || tag, ready to be stored as a single value.
func (v *Vault) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts what Seal produced. It fails if the data was altered or was
// sealed with another key.
func (v *Vault) Open(sealed []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("vault: sealed data is too short")
	}
	return v.aead.Open(nil, sealed[:n], sealed[n:], nil)
}
