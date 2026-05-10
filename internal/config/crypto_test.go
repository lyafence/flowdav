package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	original := []byte(`{"storage_type": "webdav", "enc_key": "dGVzdC1rZXktMzItYnl0ZXMtMTIzNDU2Nzg5MDEyMzQ1Njc4OTA="}`)
	password := "correct-horse-battery-staple"

	enc, err := EncryptConfig(original, password)
	require.NoError(t, err)
	require.Len(t, enc.Salt, saltLen)
	require.Len(t, enc.Nonce, nonceLen)
	require.NotEmpty(t, enc.Ciphertext)

	decrypted, err := DecryptConfig(enc, password)
	require.NoError(t, err)
	require.Equal(t, original, decrypted)
}

func TestEncryptDecryptWrongPassword(t *testing.T) {
	original := []byte(`{"storage_type": "webdav"}`)

	enc, err := EncryptConfig(original, "real-password")
	require.NoError(t, err)

	_, err = DecryptConfig(enc, "wrong-password")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid master password")
}

func TestEncryptDecryptCorruptedCiphertext(t *testing.T) {
	enc, err := EncryptConfig([]byte(`{"storage_type": "webdav"}`), "password")
	require.NoError(t, err)

	enc.Ciphertext[0] ^= 0xFF

	_, err = DecryptConfig(enc, "password")
	require.Error(t, err)
}

func TestEncryptEmptyPassword(t *testing.T) {
	original := []byte(`{"storage_type": "webdav"}`)

	enc, err := EncryptConfig(original, "")
	require.NoError(t, err)

	decrypted, err := DecryptConfig(enc, "")
	require.NoError(t, err)
	require.Equal(t, original, decrypted)
}

func TestEncryptLargePayload(t *testing.T) {
	payload := make([]byte, 1024*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	enc, err := EncryptConfig(payload, "password")
	require.NoError(t, err)

	decrypted, err := DecryptConfig(enc, "password")
	require.NoError(t, err)
	require.Equal(t, payload, decrypted)
}

func TestMarshalUnmarshalRoundtrip(t *testing.T) {
	enc, err := EncryptConfig([]byte(`{"storage_type": "webdav"}`), "password")
	require.NoError(t, err)

	data := MarshalEncrypted(enc)

	dec, err := UnmarshalEncrypted(data)
	require.NoError(t, err)
	require.Equal(t, enc.Salt, dec.Salt)
	require.Equal(t, enc.Nonce, dec.Nonce)
	require.Equal(t, enc.Ciphertext, dec.Ciphertext)
}

func TestUnmarshalEncryptedTooShort(t *testing.T) {
	_, err := UnmarshalEncrypted([]byte{1, 2, 3})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too short")
}

func TestUnmarshalEncryptedBoundary(t *testing.T) {
	_, err := UnmarshalEncrypted(make([]byte, saltLen+nonceLen))
	require.Error(t, err)

	_, err = UnmarshalEncrypted(make([]byte, saltLen+nonceLen+1))
	require.NoError(t, err)
}

func TestDeriveKeyDeterministic(t *testing.T) {
	salt := []byte("01234567890123456789012345678901")
	k1 := deriveKey([]byte("password"), salt, 10)
	k2 := deriveKey([]byte("password"), salt, 10)
	require.Equal(t, k1, k2)
}

func TestDeriveKeyDifferentPassword(t *testing.T) {
	salt := []byte("01234567890123456789012345678901")
	k1 := deriveKey([]byte("password-a"), salt, 10)
	k2 := deriveKey([]byte("password-b"), salt, 10)
	require.NotEqual(t, k1, k2)
}

func TestDeriveKeyDifferentSalt(t *testing.T) {
	s1 := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	s2 := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	k1 := deriveKey([]byte("password"), s1, 10)
	k2 := deriveKey([]byte("password"), s2, 10)
	require.NotEqual(t, k1, k2)
}

func TestLoadEncryptedSuccess(t *testing.T) {
	cfg := &AppConfig{
		StorageType: "webdav",
		WebDAV: &WebDAVConfig{
			Provider: "custom",
			URL:      "https://webdav.example.com",
			Login:    "user",
			Token:    "pass",
		},
		RefreshRateMs: 500,
		FlushRateMs:   500,
		LogLevel:      "info",
	}

	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i)
	}
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(255 - i)
	}
	cfg.EncKey = base64.StdEncoding.EncodeToString(encKey)
	cfg.HMacKey = base64.StdEncoding.EncodeToString(hmacKey)

	plaintext, err := json.Marshal(cfg)
	require.NoError(t, err)

	enc, err := EncryptConfig(plaintext, "test-password")
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "*.enc")
	require.NoError(t, err)
	_, err = f.Write(MarshalEncrypted(enc))
	require.NoError(t, err)
	f.Close()

	loaded, err := LoadEncrypted(f.Name(), "test-password")
	require.NoError(t, err)
	require.Equal(t, cfg.StorageType, loaded.StorageType)
	require.Equal(t, cfg.WebDAV.URL, loaded.WebDAV.URL)
	require.Equal(t, cfg.WebDAV.Login, loaded.WebDAV.Login)
	require.NotNil(t, loaded.EncKeyDecoded)
	require.Len(t, loaded.EncKeyDecoded, 32)
	require.NotNil(t, loaded.HMacKeyDecoded)
	require.Len(t, loaded.HMacKeyDecoded, 32)
}

func TestLoadEncryptedWrongPassword(t *testing.T) {
	enc, err := EncryptConfig([]byte(`{"storage_type": "webdav"}`), "real-password")
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "*.enc")
	require.NoError(t, err)
	f.Write(MarshalEncrypted(enc))
	f.Close()

	_, err = LoadEncrypted(f.Name(), "wrong-password")
	require.Error(t, err)
}

func TestLoadEncryptedCorruptedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.enc")
	require.NoError(t, err)
	f.Write([]byte{0, 1, 2, 3})
	f.Close()

	_, err = LoadEncrypted(f.Name(), "password")
	require.Error(t, err)
}

func TestLoadEncryptedNoFile(t *testing.T) {
	_, err := LoadEncrypted("/nonexistent/path.enc", "password")
	require.Error(t, err)
}

func TestLoadEncryptedInvalidConfigJSON(t *testing.T) {
	enc, err := EncryptConfig([]byte(`not-json`), "password")
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "*.enc")
	require.NoError(t, err)
	f.Write(MarshalEncrypted(enc))
	f.Close()

	_, err = LoadEncrypted(f.Name(), "password")
	require.Error(t, err)
}

func TestErrEncryptedConfigDetection(t *testing.T) {
	enc, err := EncryptConfig([]byte(`{"storage_type": "webdav"}`), "password")
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "*.cfg")
	require.NoError(t, err)
	f.Write(MarshalEncrypted(enc))
	f.Close()

	_, err = Load(f.Name())
	require.ErrorIs(t, err, ErrEncryptedConfig)
}

func TestIsJSON(t *testing.T) {
	require.True(t, isJSON([]byte(`{"a": 1}`)))
	require.True(t, isJSON([]byte(`[1, 2, 3]`)))
	require.True(t, isJSON([]byte(`  {"a": 1}`)))
	require.False(t, isJSON(nil))
	require.False(t, isJSON([]byte{}))
	require.True(t, isJSON([]byte("\n{\"a\": 1}")))
	require.False(t, isJSON([]byte(`hello`)))
}

func TestTrimLeftWhitespace(t *testing.T) {
	require.Equal(t, []byte("a"), trimLeftWhitespace([]byte("  a")))
	require.Equal(t, []byte("a"), trimLeftWhitespace([]byte("\n\ta")))
	require.Equal(t, []byte{}, trimLeftWhitespace([]byte("  \n  ")))
	require.Equal(t, []byte("abc"), trimLeftWhitespace([]byte("abc")))
}

func TestResolvePasswordExplicitValue(t *testing.T) {
	os.Unsetenv("FLOWDAV_PASSWORD")
	pass, interactive, rest := ResolvePassword([]string{"-c", "cfg.enc", "-p", "secret", "-l", "debug"})
	require.Equal(t, "secret", pass)
	require.False(t, interactive)
	require.Equal(t, []string{"-c", "cfg.enc", "-l", "debug"}, rest)
}

func TestResolvePasswordEqualSign(t *testing.T) {
	os.Unsetenv("FLOWDAV_PASSWORD")
	pass, interactive, rest := ResolvePassword([]string{"-c", "cfg.enc", "-p=secret"})
	require.Equal(t, "secret", pass)
	require.False(t, interactive)
	require.Equal(t, []string{"-c", "cfg.enc"}, rest)
}

func TestResolvePasswordInteractive(t *testing.T) {
	os.Unsetenv("FLOWDAV_PASSWORD")
	pass, interactive, rest := ResolvePassword([]string{"-c", "cfg.enc", "-p"})
	require.Empty(t, pass)
	require.True(t, interactive)
	require.Equal(t, []string{"-c", "cfg.enc"}, rest)
}

func TestResolvePasswordInteractiveEnd(t *testing.T) {
	os.Unsetenv("FLOWDAV_PASSWORD")
	pass, interactive, rest := ResolvePassword([]string{"-p"})
	require.Empty(t, pass)
	require.True(t, interactive)
	require.Empty(t, rest)
}

func TestResolvePasswordEnvVar(t *testing.T) {
	os.Setenv("FLOWDAV_PASSWORD", "env-pass")
	defer os.Unsetenv("FLOWDAV_PASSWORD")

	pass, interactive, rest := ResolvePassword([]string{"-c", "cfg.enc"})
	require.Equal(t, "env-pass", pass)
	require.False(t, interactive)
	require.Equal(t, []string{"-c", "cfg.enc"}, rest)
}

func TestResolvePasswordNoPassword(t *testing.T) {
	os.Unsetenv("FLOWDAV_PASSWORD")
	pass, interactive, rest := ResolvePassword([]string{"-c", "cfg.enc"})
	require.Empty(t, pass)
	require.False(t, interactive)
	require.Equal(t, []string{"-c", "cfg.enc"}, rest)
}

func TestResolvePasswordEnvOverriddenByFlag(t *testing.T) {
	os.Setenv("FLOWDAV_PASSWORD", "env-pass")
	defer os.Unsetenv("FLOWDAV_PASSWORD")

	pass, interactive, rest := ResolvePassword([]string{"-p", "flag-pass"})
	require.Equal(t, "flag-pass", pass)
	require.False(t, interactive)
	require.Empty(t, rest)
}

func TestResolvePasswordInteractiveOverridesEnv(t *testing.T) {
	os.Setenv("FLOWDAV_PASSWORD", "env-pass")
	defer os.Unsetenv("FLOWDAV_PASSWORD")

	pass, interactive, rest := ResolvePassword([]string{"-p"})
	require.Equal(t, "env-pass", pass)
	require.True(t, interactive)
	require.Empty(t, rest)
}
