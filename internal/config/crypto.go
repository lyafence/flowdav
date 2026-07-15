package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

const (
	saltLen  = 32
	nonceLen = 12
	keyLen   = 32
)

// pbkdf2Iter is a var so tests can override it for speed.
// Production value: 600000 iterations (~750ms per call).
var pbkdf2Iter = 600000

// EncryptedConfig holds the salt, nonce, and ciphertext for a PBKDF2+AES-GCM encrypted config.
type EncryptedConfig struct {
	Salt       []byte
	Nonce      []byte
	Ciphertext []byte
}

// EncryptConfig encrypts plaintext config JSON with PBKDF2+AES-GCM using the given password.
func EncryptConfig(plaintext []byte, password string) (*EncryptedConfig, error) {
	if password == "" {
		return nil, errors.New("password required")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	kek, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return &EncryptedConfig{
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

// DecryptConfig decrypts an EncryptedConfig with PBKDF2+AES-GCM using the given password.
func DecryptConfig(enc *EncryptedConfig, password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("password required")
	}
	kek, err := pbkdf2.Key(sha256.New, password, enc.Salt, pbkdf2Iter, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, enc.Nonce, enc.Ciphertext, nil)
	if err != nil {
		return nil, errors.New("invalid master password or corrupted config")
	}
	return plaintext, nil
}

// MarshalEncrypted serializes an EncryptedConfig into a single byte slice (salt+nonce+ciphertext).
func MarshalEncrypted(enc *EncryptedConfig) []byte {
	buf := make([]byte, saltLen+nonceLen+len(enc.Ciphertext))
	copy(buf[0:], enc.Salt)
	copy(buf[saltLen:], enc.Nonce)
	copy(buf[saltLen+nonceLen:], enc.Ciphertext)
	return buf
}

// UnmarshalEncrypted deserializes a byte slice into an EncryptedConfig (salt+nonce+ciphertext).
func UnmarshalEncrypted(data []byte) (*EncryptedConfig, error) {
	if len(data) < saltLen+nonceLen+1 {
		return nil, errors.New("invalid encrypted config: too short")
	}
	enc := &EncryptedConfig{
		Salt:       make([]byte, saltLen),
		Nonce:      make([]byte, nonceLen),
		Ciphertext: make([]byte, len(data)-saltLen-nonceLen),
	}
	copy(enc.Salt, data[:saltLen])
	copy(enc.Nonce, data[saltLen:saltLen+nonceLen])
	copy(enc.Ciphertext, data[saltLen+nonceLen:])
	return enc, nil
}
