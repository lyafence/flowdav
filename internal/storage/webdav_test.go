package storage

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gowebdav "github.com/studio-b12/gowebdav"
)

// TestUploadAuthHTTPErrorClassified pins the Android storage self-test
// contract: 401/403 must surface as *HTTPError so that bridge.TestStorage
// reports storage_auth instead of falling back to storage_unreachable, and
// 429 stays classified for the MultiBackend circuit breaker. Other statuses
// (e.g. 500) must remain generic errors.
func TestUploadAuthHTTPErrorClassified(t *testing.T) {
	codes := []struct {
		status int
		class  bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, false},
	}

	for _, tc := range codes {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			backend := &WebDAVBackend{
				client:     gowebdav.NewClient(srv.URL, "test", "test"),
				httpClient: srv.Client(),
				rootURL:    srv.URL,
				login:      "test",
				token:      "test",
				basePath:   "myapp",
			}

			err := backend.Upload(context.Background(), "test-file", bytes.NewReader([]byte("payload")))
			if err == nil {
				t.Fatal("expected error")
			}
			var he *HTTPError
			if !errors.As(err, &he) {
				if !tc.class {
					return // generic error, as expected
				}
				t.Fatalf("HTTP %d lost classification: %v", tc.status, err)
			}
			if !tc.class {
				t.Fatalf("HTTP %d must NOT be classified as HTTPError, got %d", tc.status, he.Code)
			}
			if he.Code != tc.status {
				t.Fatalf("classified wrong code: got %d, want %d", he.Code, tc.status)
			}
		})
	}
}

// TestDownloadAuthHTTPErrorClassified mirrors the Upload classification for
// the direct HTTP path used by Download.
func TestDownloadAuthHTTPErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	backend := &WebDAVBackend{
		httpClient: srv.Client(),
		rootURL:    srv.URL,
		login:      "test",
		token:      "test",
		basePath:   "myapp",
	}

	_, err := backend.Download(context.Background(), "test-file")
	if err == nil {
		t.Fatal("expected error")
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("401 lost classification: %v", err)
	}
	if he.Code != http.StatusUnauthorized {
		t.Fatalf("classified wrong code: got %d, want 401", he.Code)
	}
}

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

// TestFullPathDirectionMapping verifies that the direction-prefix byte maps
// filenames into the correct subdirectory and uppercases the hex body.
func TestFullPathDirectionMapping(t *testing.T) {
	backend := &WebDAVBackend{basePath: "test"}
	cases := []struct {
		name string
		want string
	}{
		{"rabc1234", "test/invoices/ABC1234"},
		{"sdef5678", "test/receipts/DEF5678"},
	}
	for _, tc := range cases {
		got, err := backend.fullPath(tc.name)
		if err != nil {
			t.Errorf("fullPath(%q) error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("fullPath(%q) = %q, want %q", tc.name, got, tc.want)
		}
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
		// Link-local and CGNAT are not "local" for the HTTP/SSRF exemption;
		// they must still pass HTTPS and validateNotPrivateURL.
		{"http://169.254.169.254:80/", false},
		{"http://100.64.0.1:8080/", false},
	}
	for _, tt := range tests {
		result := isLocalURL(tt.url)
		if result != tt.local {
			t.Errorf("isLocalURL(%s) = %v, want %v", tt.url, result, tt.local)
		}
	}
}

func TestValidateNotPrivateURL(t *testing.T) {
	tests := []struct {
		url     string
		reject  bool
		comment string
	}{
		{"https://192.168.1.1/path", true, "private IP 192.168.1.1"},
		{"https://10.0.0.1/path", true, "private IP 10.0.0.1"},
		{"https://127.0.0.1/path", true, "loopback 127.0.0.1"},
		{"https://169.254.169.254/path", true, "link-local metadata IP"},
		{"https://100.64.0.1/path", true, "CGNAT shared address space"},
		{"https://1.1.1.1/path", false, "public IP"},
		{"https://example.com/path", false, "public hostname"},
	}
	for _, tt := range tests {
		err := validateNotPrivateURL(tt.url)
		if tt.reject && err == nil {
			t.Errorf("validateNotPrivateURL should reject %s (%s)", tt.url, tt.comment)
		}
		if !tt.reject && err != nil {
			t.Errorf("validateNotPrivateURL should accept %s (%s): %v", tt.url, tt.comment, err)
		}
	}
}

func TestIsCGNAT(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"100.64.0.0", true},
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"1.1.1.1", false},
	}
	for _, tt := range tests {
		got := isCGNAT(net.ParseIP(tt.ip))
		if got != tt.want {
			t.Errorf("isCGNAT(%s) = %v, want %v", tt.ip, got, tt.want)
		}
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
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:displayname>root</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
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

func TestRandomHeaderTransport(t *testing.T) {
	var seenAccept, seenLang, seenCC int
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest("GET", "http://example.com", nil)
		tr := &randomHeaderTransport{inner: &nullTransport{}}
		if resp, err := tr.RoundTrip(req); err == nil {
			resp.Body.Close()
		}
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
		if resp, err := tr.RoundTrip(req); err == nil {
			resp.Body.Close()
		}

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

func TestRedirectGuardBlocksCrossOrigin(t *testing.T) {
	redirectTo := "http://169.254.169.254/latest/meta-data/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTo, http.StatusFound)
	}))
	defer srv.Close()

	guard := &redirectGuardTransport{inner: http.DefaultTransport}
	req, err := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := guard.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "cross-origin redirect blocked") {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatalf("expected cross-origin redirect to be blocked, got: %v", err)
	}
}

func TestRedirectGuardAllowsSameOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+r.Host+"/redirected")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	guard := &redirectGuardTransport{inner: http.DefaultTransport}
	req, err := http.NewRequestWithContext(context.Background(), "GET", srv.URL+"/original", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := guard.RoundTrip(req)
	if err != nil {
		t.Fatalf("same-origin redirect should not be blocked: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected 302, got %d", resp.StatusCode)
	}
}

type nullTransport struct{}

func (t *nullTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

// TestIsDeniedIP verifies the SSRF deny-list covers loopback, private,
// link-local, unspecified and CGNAT ranges, and not public addresses.
func TestIsDeniedIP(t *testing.T) {
	cases := []struct {
		ip     string
		denied bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // cloud metadata (link-local)
		{"100.64.0.1", true},      // CGNAT
		{"100.128.0.1", false},    // just outside CGNAT
		{"0.0.0.0", true},
		{"fe80::1", true},
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		if got := isDeniedIP(net.ParseIP(c.ip)); got != c.denied {
			t.Errorf("isDeniedIP(%s) = %v, want %v", c.ip, got, c.denied)
		}
	}
}

// acceptAndClose accepts connections and immediately closes them, so a TLS
// handshake against it fails quickly without speaking TLS.
func acceptAndClose(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

// TestDialerRefusesPrivatePeerWhenNotAllowed verifies the DNS-rebinding guard:
// when the configured host was validated as public (allowPrivate=false), a peer
// that is actually private must be refused.
func TestDialerRefusesPrivatePeerWhenNotAllowed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go acceptAndClose(ln)

	dial := newUtlsDialer("", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = dial(ctx, "tcp", ln.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "refusing connection to private address") {
		t.Errorf("dialer must refuse a private peer when allowPrivate=false, got: %v", err)
	}
}

// TestDialerAllowsPrivatePeerWhenLocal verifies the guard does not fire for a
// validated local (self-hosted/LAN) WebDAV: the call proceeds to the TLS
// handshake and fails there, not with the SSRF refusal.
func TestDialerAllowsPrivatePeerWhenLocal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go acceptAndClose(ln)

	dial := newUtlsDialer("", true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = dial(ctx, "tcp", ln.Addr().String())
	if err != nil && strings.Contains(err.Error(), "refusing connection to private address") {
		t.Errorf("allowPrivate=true must not trigger the SSRF refusal, got: %v", err)
	}
}

func TestIsPayloadName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF0123456789", true},
		{"0123456789abcde", false},
		{"0123456789abcdef0", false},
		{"0123456789abcdeg", false},
		{"..", false},
	}
	for _, tc := range cases {
		if got := isPayloadName(tc.name); got != tc.ok {
			t.Errorf("isPayloadName(%q) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

func TestListQueryFiltersDirectoriesAndForeignFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/invoices/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/invoices/0123456789abcdef</d:href><d:propstat><d:prop><d:resourcetype/><d:getlastmodified>Mon, 02 Jan 2006 15:04:05 GMT</d:getlastmodified></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/invoices/not-a-payload</d:href><d:propstat><d:prop><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/invoices/another-dir</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`))
	}))
	defer srv.Close()

	backend := &WebDAVBackend{
		client:     gowebdav.NewClient(srv.URL, "test", "test"),
		httpClient: srv.Client(),
		rootURL:    srv.URL,
		login:      "test",
		token:      "test",
	}
	files, err := backend.ListQuery(context.Background(), "r")
	if err != nil {
		t.Fatalf("ListQuery: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 payload file, got %d: %+v", len(files), files)
	}
	if files[0].Filename != "r0123456789abcdef" {
		t.Fatalf("unexpected filename: %q", files[0].Filename)
	}
}

func TestWebDAVOperationsHonorContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	backend := &WebDAVBackend{
		client:     gowebdav.NewClient(srv.URL, "test", "test"),
		httpClient: srv.Client(),
		rootURL:    srv.URL,
		login:      "test",
		token:      "test",
		basePath:   "myapp",
	}

	operations := []struct {
		name string
		call func(context.Context) error
	}{
		{"Upload", func(ctx context.Context) error {
			return backend.Upload(ctx, "r0123456789abcdef", bytes.NewReader([]byte("x")))
		}},
		{"Delete", func(ctx context.Context) error {
			return backend.Delete(ctx, "r0123456789abcdef")
		}},
		{"ListQuery", func(ctx context.Context) error {
			_, err := backend.ListQuery(ctx, "r")
			return err
		}},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			start := time.Now()
			if err := op.call(ctx); err == nil {
				t.Fatalf("%s should fail when context expires", op.name)
			}
			if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
				t.Fatalf("%s ignored context: %v", op.name, elapsed)
			}
		})
	}
}
