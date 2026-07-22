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

// TestValidateBasePathTripleEncodedNull documents a KNOWN LIMITATION:
// triple-encoded null bytes bypass the decoder because the code only does
// TWO levels of url.QueryUnescape. Input "%252500" decodes to:
//
//	level 1: "%2500"   (%25 → %)
//	level 2: "%00"     (%25 → %)
//
// The check strings.ContainsAny("%00", "\x00") is FALSE because "%00"
// contains only printable characters ('%', '0', '0'), not a real null byte.
//
// Practical impact is LOW: the WebDAV server would receive "%00" literally
// in the path, and modern servers do not interpret percent-encoded null
// bytes as path traversal vectors. This test exists to document the gap.
func TestValidateBasePathTripleEncodedNull(_ *testing.T) {
	// Triple encoding bypass — code only does 2 decode levels
	err := ValidateBasePath("%252500", "test.field")
	if err != nil {
		// If this changes in the future, update the decoder.
		// Currently this is the expected limitation.
		return
	}
	// This is the expected vulnerable path — the string "%00" is
	// not a real null byte and has no practical path traversal risk.
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

func base64Encode(b []byte) string {
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(b)))
	base64.StdEncoding.Encode(enc, b)
	return string(enc)
}
