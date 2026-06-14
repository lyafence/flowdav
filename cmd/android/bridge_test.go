package flowdavmobile

import (
	"encoding/base64"
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
	validKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	makeCfg := func(encKey, hmacKey string) []byte {
		return []byte(`{"enc_key":"` + encKey + `","hmac_key":"` + hmacKey + `","webdav":{"url":"http://example.com"}}`)
	}

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"valid", makeCfg(validKey, validKey), ""},
		{"missing webdav", []byte(`{"enc_key":"` + validKey + `","hmac_key":"` + validKey + `"}`), "webdav config is required"},
		{"missing enc_key", []byte(`{"hmac_key":"` + validKey + `","webdav":{"url":"http://example.com"}}`), "enc_key is required"},
		{"missing hmac_key", []byte(`{"enc_key":"` + validKey + `","webdav":{"url":"http://example.com"}}`), "hmac_key is required"},
		{"invalid JSON", []byte(`{bad`), "invalid character"},
		{"bad enc_key", makeCfg("!!!", validKey), "invalid enc_key:"},
		{"bad hmac_key", makeCfg(validKey, "!!!"), "invalid hmac_key:"},
		{"short key", makeCfg(base64.StdEncoding.EncodeToString(make([]byte, 16)), validKey), "invalid enc_key: must be 32 bytes"},
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
			require.Equal(t, "webdav", cfg.StorageType)
			require.Len(t, cfg.EncKeyDecoded, 32)
			require.Len(t, cfg.HMacKeyDecoded, 32)
		})
	}
}

func TestParseConfigJSONDefaults(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg, err := parseConfigJSON([]byte(`{"enc_key":"` + validKey + `","hmac_key":"` + validKey + `","webdav":{"url":"http://example.com"},"max_message_size":65536,"log_level":"debug"}`))
	require.NoError(t, err)
	require.Equal(t, 65536, cfg.MaxMessageSize)
	require.Equal(t, "debug", cfg.LogLevel)
	require.Equal(t, "webdav", cfg.StorageType)
}

func TestWipeBytes(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	wipeBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("b[%d] = %d, want 0", i, v)
		}
	}
}

func TestWipeBytesEmpty(t *testing.T) {
	require.NotPanics(t, func() { wipeBytes(nil) })
	require.NotPanics(t, func() { wipeBytes([]byte{}) })
}
