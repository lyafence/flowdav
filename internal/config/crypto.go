package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	saltLen    = 32
	nonceLen   = 12
	pbkdf2Iter = 600000
	keyLen     = 32
)

type EncryptedConfig struct {
	Salt       []byte
	Nonce      []byte
	Ciphertext []byte
}

func EncryptConfig(plaintext []byte, password string) (*EncryptedConfig, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	kek := deriveKey([]byte(password), salt, pbkdf2Iter)
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

func DecryptConfig(enc *EncryptedConfig, password string) ([]byte, error) {
	kek := deriveKey([]byte(password), enc.Salt, pbkdf2Iter)
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

func MarshalEncrypted(enc *EncryptedConfig) []byte {
	buf := make([]byte, saltLen+nonceLen+len(enc.Ciphertext))
	copy(buf[0:], enc.Salt)
	copy(buf[saltLen:], enc.Nonce)
	copy(buf[saltLen+nonceLen:], enc.Ciphertext)
	return buf
}

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

func deriveKey(password, salt []byte, iter int) []byte {
	h := hmac.New(sha256.New, password)
	h.Write(salt)
	var block [4]byte
	binary.BigEndian.PutUint32(block[:], 1)
	h.Write(block[:])
	u := h.Sum(nil)
	result := make([]byte, len(u))
	copy(result, u)
	for i := 1; i < iter; i++ {
		h.Reset()
		h.Write(u)
		u = h.Sum(nil)
		for j := range u {
			result[j] ^= u[j]
		}
	}
	return result
}
