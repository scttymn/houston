// Package move is the one-time move from the Rails Mission Control's
// database to this one (docs/plans/mission-control-go.md, decision 1).
package move

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RailsKeys are the Rails app's Active Record encryption keys, the
// AR_ENCRYPTION_PRIMARY_KEY and AR_ENCRYPTION_KEY_DERIVATION_SALT of the
// server's .env.
type RailsKeys struct {
	PrimaryKey string
	Salt       string
}

// ErrNotDecrypted is a stored value that isn't Rails' encryption under
// these keys: a wrong key, a changed byte, or not an envelope at all.
var ErrNotDecrypted = errors.New("not a value Rails encrypted with these keys")

// RailsCipher reads what Active Record encryption stored (its defaults, as
// the Rails app has them: non-deterministic AES-256-GCM, a key derived with
// PBKDF2-SHA256, no key references).
type RailsCipher struct{ aead cipher.AEAD }

// NewRailsCipher derives the key as ActiveSupport::KeyGenerator does
// (PBKDF2-HMAC-SHA256, 2^16 iterations, 32 bytes).
func NewRailsCipher(keys RailsKeys) (*RailsCipher, error) {
	if keys.PrimaryKey == "" || keys.Salt == "" {
		return nil, errors.New("the Rails encryption keys are missing (AR_ENCRYPTION_PRIMARY_KEY, AR_ENCRYPTION_KEY_DERIVATION_SALT)")
	}
	key, err := pbkdf2.Key(sha256.New, keys.PrimaryKey, []byte(keys.Salt), 1<<16, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &RailsCipher{aead: aead}, nil
}

// envelope is the JSON Rails stores: the payload and its headers, each
// base64. "c" is set when Rails deflated the value before encrypting it.
type envelope struct {
	P *string `json:"p"`
	H *struct {
		IV         string `json:"iv"`
		AuthTag    string `json:"at"`
		Compressed bool   `json:"c"`
	} `json:"h"`
}

// Decrypt is the value Rails encrypted into stored.
func (c *RailsCipher) Decrypt(stored string) (string, error) {
	var env envelope
	if err := json.Unmarshal([]byte(stored), &env); err != nil || env.P == nil || env.H == nil {
		return "", ErrNotDecrypted
	}
	payload, err1 := base64.StdEncoding.DecodeString(*env.P)
	iv, err2 := base64.StdEncoding.DecodeString(env.H.IV)
	tag, err3 := base64.StdEncoding.DecodeString(env.H.AuthTag)
	if err := errors.Join(err1, err2, err3); err != nil || len(iv) != c.aead.NonceSize() {
		return "", ErrNotDecrypted
	}
	plain, err := c.aead.Open(nil, iv, append(payload, tag...), nil)
	if err != nil {
		return "", ErrNotDecrypted
	}
	if env.H.Compressed {
		r, err := zlib.NewReader(bytes.NewReader(plain))
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrNotDecrypted, err)
		}
		if plain, err = io.ReadAll(r); err != nil {
			return "", fmt.Errorf("%w: %v", ErrNotDecrypted, err)
		}
	}
	return string(plain), nil
}
