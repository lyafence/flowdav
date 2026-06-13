package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gowebdav "github.com/studio-b12/gowebdav"
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

	err = validateNotPrivateURL("https://10.0.0.1/path")
	if err == nil {
		t.Error("validateNotPrivateURL should reject private IP 10.0.0.1")
	}

	err = validateNotPrivateURL("https://127.0.0.1/path")
	if err == nil {
		t.Error("validateNotPrivateURL should reject loopback 127.0.0.1")
	}
}

func TestNewWebDAVBackendEmptyURL(t *testing.T) {
	_, err := NewWebDAVBackend("", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

// TestLoginMkdirForbidden verifies that Login returns an error immediately
// when Mkdir returns 403 Forbidden, even if Stat would succeed. This prevents
// silent data loss when using a read-only WebDAV token.
func TestLoginMkdirForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusForbidden)
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:displayname>root</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gowebdav.NewClient(srv.URL, "test", "test")
	backend := &WebDAVBackend{
		client:   client,
		basePath: "myapp",
	}

	err := backend.Login(context.Background())
	if err == nil {
		t.Fatal("expected error for 403 Forbidden on Mkdir")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to mention 403, got: %v", err)
	}
}

func TestCryptoRandIntRange(t *testing.T) {
	for max := 1; max <= 10; max++ {
		for i := 0; i < 100; i++ {
			got := cryptoRandInt(max)
			if got < 0 || got >= max {
				t.Fatalf("cryptoRandInt(%d) = %d, out of range [0, %d)", max, got, max)
			}
		}
	}
	// max ≤ 0 returns 0
	if got := cryptoRandInt(0); got != 0 {
		t.Errorf("cryptoRandInt(0) = %d, want 0", got)
	}
	if got := cryptoRandInt(-1); got != 0 {
		t.Errorf("cryptoRandInt(-1) = %d, want 0", got)
	}
}

func TestRandomHeaderTransport(t *testing.T) {
	var seenAccept, seenLang, seenCC int
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest("GET", "http://example.com", nil)
		tr := &randomHeaderTransport{inner: &nullTransport{}}
		tr.RoundTrip(req)
		if a := req.Header.Get("Accept"); a != "" {
			seenAccept++
		}
		if l := req.Header.Get("Accept-Language"); l != "" {
			seenLang++
		}
		if req.Header.Get("Cache-Control") != "" {
			seenCC++
		}
	}
	if seenAccept == 0 {
		t.Error("Accept header never set")
	}
	if seenLang == 0 {
		t.Error("Accept-Language header never set")
	}
	if seenCC == 0 {
		t.Error("Cache-Control header never set (may be deleted randomly — low probability)")
	}
}

func TestRandomHeaderTransportPreservesExisting(t *testing.T) {
	for i := 0; i < 20; i++ {
		req, _ := http.NewRequest("PROPFIND", "http://example.com", nil)
		req.Header.Set("Accept", "application/xml,text/xml")
		req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
		req.Header.Set("Cache-Control", "max-age=0")

		tr := &randomHeaderTransport{inner: &nullTransport{}}
		tr.RoundTrip(req)

		if req.Header.Get("Accept") != "application/xml,text/xml" {
			t.Fatal("randomHeaderTransport overwrote preset Accept header")
		}
		if req.Header.Get("Accept-Language") != "fr-FR,fr;q=0.9" {
			t.Fatal("randomHeaderTransport overwrote preset Accept-Language header")
		}
		if req.Header.Get("Cache-Control") != "max-age=0" {
			t.Fatal("randomHeaderTransport overwrote preset Cache-Control header")
		}
	}
}

type nullTransport struct{}

func (t *nullTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}
