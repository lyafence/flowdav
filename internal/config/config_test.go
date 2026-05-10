package config

import (
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
	// It will be caught by URL decoding in validateBasePath
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
			err := validateBasePath(tt.basePath, "test.field")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBasePath(%q) error = %v, wantErr = %v", tt.basePath, err, tt.wantErr)
			}
		})
	}
}

// Helper to avoid file I/O in tests - uses validateBasePath directly
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
		err := validateBasePath(tt.path, "test.field")
		if tt.want && err == nil {
			t.Errorf("Expected error for path: %s", tt.path)
		}
		if !tt.want && err != nil {
			t.Errorf("Unexpected error for path: %s: %v", tt.path, err)
		}
	}
}
