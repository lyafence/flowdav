package flowdavmobile

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeKey(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 16))

	tests := []struct {
		name    string
		keyB64  string
		keyName string
		wantErr string
		wantLen int
	}{
		{"valid", validKey, "enc_key", "", 32},
		{"empty", "", "enc_key", "enc_key is required", 0},
		{"bad base64", "!!!", "enc_key", "invalid enc_key:", 0},
		{"wrong length", shortKey, "hmac_key", "hmac_key must be 32 bytes, got 16", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeKey(tt.keyB64, tt.keyName)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, tt.wantLen)
		})
	}
}

func TestParseConfigJSON(t *testing.T) {
	encKey := base64.StdEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	hmacKey := base64.StdEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	makeCfg := func(encKey, hmacKey string) []byte {
		return []byte(`{"enc_key":"` + encKey + `","hmac_key":"` + hmacKey + `","webdav":{"url":"http://example.com"}}`)
	}

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"valid", makeCfg(encKey, hmacKey), ""},
		{"missing webdav", []byte(`{"enc_key":"` + encKey + `","hmac_key":"` + hmacKey + `"}`), "webdav config is required"},
		{"missing enc_key", []byte(`{"hmac_key":"` + hmacKey + `","webdav":{"url":"http://example.com"}}`), "enc_key is required"},
		{"missing hmac_key", []byte(`{"enc_key":"` + encKey + `","webdav":{"url":"http://example.com"}}`), "hmac_key is required"},
		{"invalid JSON", []byte(`{bad`), "invalid character"},
		{"bad enc_key", makeCfg("!!!", hmacKey), "invalid enc_key:"},
		{"bad hmac_key", makeCfg(encKey, "!!!"), "invalid hmac_key:"},
		{"short key", makeCfg(base64.StdEncoding.EncodeToString(make([]byte, 16)), hmacKey), "invalid enc_key: must be 32 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseConfigJSON(tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, cfg)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.NotNil(t, cfg.WebDAV)
			require.Len(t, cfg.EncKeyDecoded, 32)
			require.Len(t, cfg.HMacKeyDecoded, 32)
		})
	}
}

func TestParseConfigJSONDefaults(t *testing.T) {
	encKey := base64.StdEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	hmacKey := base64.StdEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	cfg, err := parseConfigJSON([]byte(`{"enc_key":"` + encKey + `","hmac_key":"` + hmacKey + `","webdav":{"url":"http://example.com"},"max_message_size":65536,"log_level":"debug"}`))
	require.NoError(t, err)
	require.Equal(t, 65536, cfg.MaxMessageSize)
	require.Equal(t, "debug", cfg.LogLevel)
}

// TestErrorCodesStable pins the wire values of the bridge ErrorCode constants.
// These strings are the contract consumed by Android's ProxyState.ErrorKind
// (android/app/src/main/java/com/flowdav/app/ProxyState.kt); changing a value
// here without updating the Kotlin side silently breaks UI error mapping.
func TestErrorCodesStable(t *testing.T) {
	want := []string{
		"encrypted_no_password",
		"decrypt_failed",
		"invalid_config",
		"invalid_key",
		"already_running",
		"storage_init",
		"listen_failed",
		"runtime",
		"unknown",
	}
	got := []string{
		string(ErrEncryptedNoPw),
		string(ErrDecryptFailed),
		string(ErrInvalidConfig),
		string(ErrInvalidKey),
		string(ErrAlreadyRunning),
		string(ErrStorageInit),
		string(ErrListenFailed),
		string(ErrRuntime),
		string(ErrUnknown),
	}
	require.Equal(t, want, got, "keep ProxyState.ErrorKind in sync")
}

func TestCodeOfClassifies(t *testing.T) {
	require.Equal(t, ErrStorageInit, codeOf(coded(ErrStorageInit, errors.New("boom"))))
	require.Equal(t, ErrListenFailed, codeOf(fmt.Errorf("listen: %w", coded(ErrListenFailed, errors.New("boom")))))
	require.Equal(t, ErrUnknown, codeOf(errors.New("plain error")))
}
