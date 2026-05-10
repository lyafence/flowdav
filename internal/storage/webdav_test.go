package storage

import (
	"testing"
)

func TestFullPathTraversal(t *testing.T) {
	backend := &WebDAVBackend{basePath: "test"}
	_, err := backend.fullPath("../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestFullPathTraversalEncoded(t *testing.T) {
	backend := &WebDAVBackend{basePath: "test"}
	// URL-encoded ".." (%2e%2e)
	_, err := backend.fullPath("%2e%2e/etc/passwd")
	if err == nil {
		t.Fatal("expected error for URL-encoded path traversal")
	}
	// Double-encoded ".." (%252e%252e)
	_, err = backend.fullPath("%252e%252e/etc/passwd")
	if err == nil {
		t.Fatal("expected error for double-encoded path traversal")
	}
	// Mixed encoding
	_, err = backend.fullPath("..%2fetc/passwd")
	if err == nil {
		t.Fatal("expected error for mixed path traversal")
	}
}

func TestFullPathValid(t *testing.T) {
	backend := &WebDAVBackend{basePath: "test"}
	result, err := backend.fullPath("file.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "test/file.bin" {
		t.Errorf("expected 'test/file.bin', got: %s", result)
	}
}

func TestFullPathNoBasePath(t *testing.T) {
	backend := &WebDAVBackend{basePath: ""}
	result, err := backend.fullPath("file.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "file.bin" {
		t.Errorf("expected 'file.bin', got: %s", result)
	}
}

func TestFullPathDot(t *testing.T) {
	backend := &WebDAVBackend{}
	_, err := backend.fullPath(".")
	if err == nil {
		t.Fatal("expected error for '.' path")
	}
}

func TestFullPathDotDot(t *testing.T) {
	backend := &WebDAVBackend{}
	_, err := backend.fullPath("..")
	if err == nil {
		t.Fatal("expected error for '..' path")
	}
}

func TestMaxFileSize(t *testing.T) {
	if MaxFileSize != 16*1024*1024 {
		t.Errorf("unexpected MaxFileSize: %d", MaxFileSize)
	}
}

func TestIsLocalURL(t *testing.T) {
	tests := []struct {
		url   string
		local bool
	}{
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"https://example.com", false},
	}
	for _, tt := range tests {
		result := isLocalURL(tt.url)
		if result != tt.local {
			t.Errorf("isLocalURL(%s) = %v, want %v", tt.url, result, tt.local)
		}
	}
}

func TestValidateNotPrivateURL(t *testing.T) {
	err := validateNotPrivateURL("https://192.168.1.1/path")
	if err == nil {
		t.Error("validateNotPrivateURL should reject private IP 192.168.1.1")
	}
}

func TestValidateBasePathURL(t *testing.T) {
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

func TestNewWebDAVBackendInvalidProvider(t *testing.T) {
	_, err := NewWebDAVBackend("google", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for non-custom provider")
	}
}

func TestNewWebDAVBackendEmptyURL(t *testing.T) {
	_, err := NewWebDAVBackend("custom", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}
