package config

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPathTraversal(t *testing.T) {
	// These should be detected as path traversal (raw ".." pattern)
	require.True(t, isPathTraversal("../"))
	require.True(t, isPathTraversal(".."))
	require.True(t, isPathTraversal("../etc"))
	require.True(t, isPathTraversal("test/.."))
	require.True(t, isPathTraversal("../path"))
	require.True(t, isPathTraversal("path/.."))

	// This is URL-encoded, not raw "..", so should NOT be detected here
	// It will be caught by URL decoding in ValidateBasePath
	require.False(t, isPathTraversal("..%2fetc"))

	// These should NOT be detected as path traversal
	require.False(t, isPathTraversal("./normal"))
	require.False(t, isPathTraversal("myapp"))
	require.False(t, isPathTraversal(""))
	require.False(t, isPathTraversal("."))
	require.False(t, isPathTraversal("a/b/c"))
	require.False(t, isPathTraversal("..hidden")) // starts with .. but not followed by /
}

func TestValidateBasePath(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		wantErr  bool
	}{
		{"plain traversal", "../etc", true},
		{"single encoded", "%2e%2e/etc", true},
		{"double encoded", "%252e%252e/etc", true},
		{"mixed encoding", "..%2fetc", true},
		{"valid path", "myapp", false},
		{"empty path", "", false},
		{"dot only", ".", false},
		{"relative with slash", "./normal", false},
		{"hidden folder", ".hidden", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBasePath(tt.basePath, "test.field")
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBasePath(%q) error = %v, wantErr = %v", tt.basePath, err, tt.wantErr)
			}
		})
	}
}

// Helper to avoid file I/O in tests - uses ValidateBasePath directly
func TestConfigBasePathValidation(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"..", true},
		{"%2e", false}, // %2e alone is not path traversal, but %2e%2e is
		{"test/..", true},
		{"test/%2e%2e", true}, // double encoded .. in the middle
		{"..test", false},     // starts with .. but not a traversal
		{"normal/path", false},
		{"./normal", false},
		{"../", true},
		{"%2e%2e", true}, // double encoded ..
	}

	for _, tt := range tests {
		err := ValidateBasePath(tt.path, "test.field")
		if tt.want && err == nil {
			t.Errorf("Expected error for path: %s", tt.path)
		}
		if !tt.want && err != nil {
			t.Errorf("Unexpected error for path: %s: %v", tt.path, err)
		}
	}
}

func TestValidateBasePathRejectsEncodedNullByte(t *testing.T) {
	// URL-encoded null bytes must be caught after decoding
	tests := []struct {
		name string
		path string
	}{
		{"single encoded null", "%00"},
		{"double encoded null", "%2500"},
		{"null in middle", "foo%00bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBasePath(tt.path, "test.field")
			if err == nil {
				t.Errorf("ValidateBasePath should reject encoded null byte: %s", tt.path)
			}
		})
	}
}

func TestValidateAppConfigBackendsMissingURL(t *testing.T) {
	encKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = 1 // different from encKey
	}
	cfg := &AppConfig{
		WebDAV: &WebDAVConfig{
			Backends: []WebDAVConfig{
				{URL: "https://webdav1.example.com", Login: "user", Token: "tok"},
				{URL: "", Login: "user", Token: "tok"}, // missing URL
			},
		},
		EncKey:  base64Encode(encKey),
		HMacKey: base64Encode(hmacKey),
	}
	err := ValidateAppConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing backend URL")
	}
}

func TestValidateAppConfigMaxMessageSizeTooSmall(t *testing.T) {
	encKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = 1
	}

	cfg := &AppConfig{
		WebDAV: &WebDAVConfig{
			URL:   "https://webdav.example.com",
			Login: "user",
			Token: "token",
		},
		EncKey:         base64Encode(encKey),
		HMacKey:        base64Encode(hmacKey),
		MaxMessageSize: 1000,
	}
	err := ValidateAppConfig(cfg)
	if err == nil {
		t.Fatal("expected error for MaxMessageSize below minimum")
	}
}

func TestValidateAppConfigMaxMessageSizeTooLarge(t *testing.T) {
	encKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = 1
	}

	cfg := &AppConfig{
		WebDAV: &WebDAVConfig{
			URL:   "https://webdav.example.com",
			Login: "user",
			Token: "token",
		},
		EncKey:         base64Encode(encKey),
		HMacKey:        base64Encode(hmacKey),
		MaxMessageSize: MaxMaxMessageSize + 1,
	}
	err := ValidateAppConfig(cfg)
	if err == nil {
		t.Fatal("expected error for MaxMessageSize above maximum")
	}
}

func TestValidateAppConfigInvalidLogLevel(t *testing.T) {
	encKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = 1
	}

	cfg := &AppConfig{
		WebDAV: &WebDAVConfig{
			URL:   "https://webdav.example.com",
			Login: "user",
			Token: "token",
		},
		EncKey:   base64Encode(encKey),
		HMacKey:  base64Encode(hmacKey),
		LogLevel: "verbose",
	}
	if err := ValidateAppConfig(cfg); err == nil {
		t.Fatal("expected error for invalid log_level")
	}

	cfg.LogLevel = "debug"
	if err := ValidateAppConfig(cfg); err != nil {
		t.Fatalf("expected valid log_level to pass, got: %v", err)
	}
}

func base64Encode(b []byte) string {
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(b)))
	base64.StdEncoding.Encode(enc, b)
	return string(enc)
}
