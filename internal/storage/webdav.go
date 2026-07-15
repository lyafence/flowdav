package storage

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	gowebdav "github.com/studio-b12/gowebdav"

	"github.com/lyafence/flowdav/internal/logger"
)

// MaxFileSize limits upload/download size to prevent OOM attacks.
// Set once at startup (by cmd/ entrypoints) before any goroutines begin,
// matching transport.MaxMessageSize. Exception to "no global state" by
// the same rationale as transport.MaxMessageSize — OOM prevention.
var MaxFileSize = 16 * 1024 * 1024

var (
	dirReq = "invoices"
	dirRes = "receipts"
)

// chromeUA mimics a recent Chrome browser User-Agent to blend in with
// legitimate WebDAV client traffic and avoid WAF/bot detection.
const chromeUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// HTTPError wraps an HTTP status code for storage-level error classification.
type HTTPError struct {
	Code int
	Err  error
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %v", e.Code, e.Err)
}

func (e *HTTPError) Unwrap() error {
	return e.Err
}

// IsRateLimited checks if an error is a 429 Too Many Requests.
func IsRateLimited(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code == http.StatusTooManyRequests
	}
	return false
}

// userAgentTransport wraps an http.RoundTripper to inject a browser
// User-Agent header on every request, preventing Go-http-client detection.
type userAgentTransport struct {
	inner http.RoundTripper
	ua    string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", t.ua)
	return t.inner.RoundTrip(req)
}

// randomHeaderTransport wraps an http.RoundTripper and randomizes
// per-request headers to reduce HTTP fingerprinting.
type randomHeaderTransport struct {
	inner http.RoundTripper
}

func (t *randomHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Random Accept — cycle through common values to avoid a fixed signature.
	// Only set if not already present (gowebdav sets Accept for PROPFIND).
	if req.Header.Get("Accept") == "" {
		accepts := []string{
			"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"*/*",
			"text/html,application/json,application/xml;q=0.9,*/*;q=0.8",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
		}
		req.Header.Set("Accept", accepts[cryptoRandInt(len(accepts))])
	}

	// Random Accept-Language.
	if req.Header.Get("Accept-Language") == "" {
		langs := []string{"en-US,en;q=0.9", "en-GB,en;q=0.8", "en-US,en;q=0.7", "ru-RU,ru;q=0.8,en;q=0.5"}
		req.Header.Set("Accept-Language", langs[cryptoRandInt(len(langs))])
	}

	// Vary Cache-Control.
	if req.Header.Get("Cache-Control") == "" && cryptoRandInt(2) == 0 {
		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	}

	return t.inner.RoundTrip(req)
}

// redirectGuardTransport blocks cross-origin HTTP redirects to prevent SSRF
// (e.g., a malicious WebDAV server redirecting to 169.254.169.254).
// Same-origin redirects (same scheme + host + port) pass through.
type redirectGuardTransport struct {
	inner http.RoundTripper
}

func (t *redirectGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, locErr := resp.Location()
		if locErr == nil && isCrossOrigin(req.URL, loc) {
			resp.Body.Close()
			return nil, fmt.Errorf("cross-origin redirect blocked: %s -> %s", req.URL, loc)
		}
	}
	return resp, nil
}

// isCrossOrigin returns true if two URLs differ in scheme, host, or port.
func isCrossOrigin(a, b *url.URL) bool {
	return a.Scheme != b.Scheme || a.Host != b.Host
}

// isCGNAT reports whether ip belongs to the carrier-grade NAT shared
// address space (100.64.0.0/10, RFC 6598). Go's IsPrivate does not
// cover this range, so an explicit check is required.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

// cryptoRandInt returns a non-negative random int in [0, n) using crypto/rand.
// Uses rejection sampling to avoid modulo bias when n does not divide 256.
// Currently called only with n ∈ {2, 4} where no rejection is needed.
func cryptoRandInt(n int) int {
	if n <= 0 {
		return 0
	}
	// When n evenly divides 256, modulo has no bias.
	if 256%n == 0 {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			logger.Info("cryptoRandInt rand read error: %v", err)
			return 0
		}
		return int(b[0]) % n
	}
	// Rejection sampling for general case.
	mask := byte(256 - (256 % n))
	for {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			logger.Info("cryptoRandInt rand read error: %v", err)
			return 0
		}
		if b[0] < mask {
			return int(b[0]) % n
		}
	}
}

// newUtlsDialer returns a TLS dial function matching the given fingerprint
// profile. Empty string defaults to Chrome 133.
func newUtlsDialer(fingerprint string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	var helloID utls.ClientHelloID
	switch fingerprint {
	case "", "chrome":
		helloID = utls.HelloChrome_133
	case "chrome_auto":
		helloID = utls.HelloChrome_Auto
	default:
		helloID = utls.HelloChrome_133
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		tcpConn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			tcpConn.Close()
			return nil, err
		}
		uConn := utls.UClient(tcpConn, &utls.Config{
			ServerName: host,
		}, helloID)
		if err := uConn.HandshakeContext(ctx); err != nil {
			tcpConn.Close()
			return nil, err
		}
		return uConn, nil
	}
}

type WebDAVBackend struct {
	client     *gowebdav.Client
	httpClient *http.Client
	token      string
	login      string
	rootURL    string
	basePath   string
}

func NewWebDAVBackend(login, token, basePath, url, tlsFingerprint string) (*WebDAVBackend, error) {
	if url == "" {
		return nil, fmt.Errorf("WebDAV URL is required")
	}

	// Enforce HTTPS for non-local URLs to prevent traffic interception
	isLocal := isLocalURL(url)
	if !isLocal && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("WebDAV URL must use HTTPS, got: %s", url)
	}

	// Prevent SSRF by rejecting private/internal IP addresses (except for local testing)
	if !isLocal {
		if err := validateNotPrivateURL(url); err != nil {
			return nil, err
		}
	}

	rootURL := url
	dialTLS := newUtlsDialer(tlsFingerprint)
	transport := &http.Transport{
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		DialTLSContext:        dialTLS,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper), // uTLS не поддерживает HTTP/2
	}
	uaTransport := &userAgentTransport{inner: transport, ua: chromeUA}
	randomTransport := &randomHeaderTransport{inner: uaTransport}
	wrappedTransport := &redirectGuardTransport{inner: randomTransport}

	client := gowebdav.NewClient(rootURL, login, token)
	client.SetTransport(wrappedTransport)
	client.SetTimeout(90 * time.Second)
	httpClient := &http.Client{
		Timeout:   90 * time.Second,
		Transport: wrappedTransport,
	}

	if basePath != "" {
		basePath = path.Clean(basePath)
		if basePath == "." || basePath == "/" {
			basePath = ""
		}
	}

	backend := &WebDAVBackend{
		client:     client,
		httpClient: httpClient,
		token:      token,
		login:      login,
		rootURL:    rootURL,
		basePath:   basePath,
	}

	// Test connection and create directories
	ctx := context.Background()
	if err := backend.Login(ctx); err != nil {
		return nil, fmt.Errorf("WebDAV login failed: %w", err)
	}

	return backend, nil
}

// dnsTimeout limits DNS lookup time to prevent SSRF via DNS rebinding
const dnsTimeout = 2 * time.Second

func isLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := u.Hostname()

	// Check for localhost patterns
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	// Try to resolve and check if it's a private IP
	ip := net.ParseIP(host)
	if ip == nil {
		// Use timeout-based resolver only - no fallback to prevent SSRF
		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()

		resolver := &net.Resolver{PreferGo: true}
		addrs, err := resolver.LookupHost(ctx, host)
		if err != nil {
			return false
		}
		for _, addr := range addrs {
			ip = net.ParseIP(addr)
			if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
				return true
			}
		}
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
}

func validateNotPrivateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		// Use timeout-based resolver only - no fallback to prevent SSRF
		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()

		resolver := &net.Resolver{PreferGo: true}
		addrs, err := resolver.LookupHost(ctx, host)
		if err != nil {
			return fmt.Errorf("cannot resolve host: %s", host)
		}
		for _, addr := range addrs {
			ip = net.ParseIP(addr)
			if ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified() || isCGNAT(ip)) {
				return fmt.Errorf("WebDAV URL cannot point to private/loopback IP: %s (resolved from %s)", ip, host)
			}
		}
		return nil
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified() || isCGNAT(ip) {
		return fmt.Errorf("WebDAV URL cannot point to private/loopback IP: %s", ip)
	}

	return nil
}

func (w *WebDAVBackend) fullPath(name string) (string, error) {
	// URL-decode to catch encoded path traversal attempts (including double-encoding)
	decoded := name
	if decoded2, err := url.QueryUnescape(decoded); err == nil {
		decoded = decoded2
		// Second decode for double-encoded like %252e
		if decoded3, err2 := url.QueryUnescape(decoded2); err2 == nil {
			decoded = decoded3
		}
	}
	// Reject path traversal attempts (check both raw and decoded)
	if strings.Contains(name, "..") || strings.Contains(decoded, "..") {
		return "", fmt.Errorf("path traversal detected in filename: %s", name)
	}
	// Ensure clean filename without directory separators
	name = path.Base(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid filename: %s", name)
	}

	// Map direction prefix → subdirectory, strip prefix, uppercase
	var sub string
	switch {
	case strings.HasPrefix(name, "r"):
		sub = dirReq
		name = strings.ToUpper(name[1:])
	case strings.HasPrefix(name, "s"):
		sub = dirRes
		name = strings.ToUpper(name[1:])
	}

	if sub != "" {
		name = path.Join(sub, name)
	}
	if w.basePath == "" {
		return name, nil
	}
	return path.Join(w.basePath, name), nil
}

func (w *WebDAVBackend) Login(_ context.Context) error {
	var dirs []string
	if w.basePath == "" {
		dirs = []string{dirReq, dirRes}
	} else {
		dirs = []string{w.basePath, path.Join(w.basePath, dirReq), path.Join(w.basePath, dirRes)}
	}
	for _, d := range dirs {
		err := w.client.Mkdir(d, 0755)
		if err != nil {
			// 403 Forbidden is always fatal — even if the directory already exists,
			// we won't be able to upload files to it. Propagate immediately.
			if gowebdav.IsErrCode(err, http.StatusForbidden) {
				return fmt.Errorf("failed to create %s: permission denied (403)", d)
			}
			_, statErr := w.client.Stat(d)
			if statErr != nil {
				return fmt.Errorf("failed to create %s: %w", d, err)
			}
		}
	}
	return nil
}

func (w *WebDAVBackend) ListQuery(_ context.Context, prefix string) ([]FileEntry, error) {
	// Map direction prefix → subdirectory
	var sub string
	switch prefix {
	case "r":
		sub = dirReq
	case "s":
		sub = dirRes
	}
	dir := "/"
	if w.basePath != "" {
		dir = w.basePath
	}
	if sub != "" {
		dir = path.Join(dir, sub)
	}
	files, err := w.client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list error: %w", err)
	}

	var result []FileEntry
	for _, f := range files {
		// Reconstruct internal name: prepend direction prefix + lowercase
		result = append(result, FileEntry{
			Filename:   prefix + strings.ToLower(f.Name()),
			BackendIdx: 0,
			ModTime:    f.ModTime(),
		})
	}
	return result, nil
}

func (w *WebDAVBackend) Upload(ctx context.Context, filename string, data io.Reader) error {
	_ = ctx
	full, err := w.fullPath(filename)
	if err != nil {
		return fmt.Errorf("upload error: %w", err)
	}

	// Limit read to MaxFileSize+1 to prevent OOM while detecting truncation
	limitedReader := io.LimitReader(data, int64(MaxFileSize)+1)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return fmt.Errorf("read error: %w", err)
	}
	if len(content) > MaxFileSize {
		return fmt.Errorf("upload error: file too large: %d bytes (max %d)", len(content), MaxFileSize)
	}

	err = w.client.Write(full, content, 0644)
	if err != nil {
		if gowebdav.IsErrCode(err, http.StatusTooManyRequests) {
			return &HTTPError{Code: http.StatusTooManyRequests, Err: err}
		}
		return fmt.Errorf("upload error: %w", err)
	}
	logger.Debug("WebDAV: uploaded %s (%d bytes)", filename, len(content))
	return nil
}

func (w *WebDAVBackend) Download(ctx context.Context, filename string) (io.ReadCloser, error) {
	full, err := w.fullPath(filename)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}

	u, err := url.Parse(w.rootURL)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	u = u.JoinPath(full)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	req.SetBasicAuth(w.login, w.token)
	req.Header.Set("User-Agent", chromeUA)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, &HTTPError{Code: http.StatusTooManyRequests, Err: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download error: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > int64(MaxFileSize) {
		resp.Body.Close()
		return nil, fmt.Errorf("download error: file too large: %d bytes (max %d)", resp.ContentLength, MaxFileSize)
	}

	if resp.ContentLength > 0 {
		return &exactReader{io.LimitReader(resp.Body, resp.ContentLength), resp.Body}, nil
	}
	return newMaxSizeReader(resp.Body, MaxFileSize), nil
}

// exactReader reads exactly n bytes from the response body, preventing
// trailing bytes from being interpreted as crypto frame data.
type exactReader struct {
	io.Reader
	io.Closer
}

// maxSizeReader wraps an io.ReadCloser and ensures the total bytes read
// do not exceed maxSize. Returns an error if the limit is exceeded.
// Handles chunked responses where Content-Length is unavailable.
type maxSizeReader struct {
	rc    io.ReadCloser
	limit int
	total int
}

func (r *maxSizeReader) Read(p []byte) (int, error) {
	remain := r.limit + 1 - r.total
	if remain <= 0 {
		return 0, fmt.Errorf("file too large: exceeds %d bytes", r.limit)
	}
	if len(p) > remain {
		p = p[:remain]
	}
	n, err := r.rc.Read(p)
	r.total += n
	if r.total > r.limit {
		return n, fmt.Errorf("file too large: exceeds %d bytes", r.limit)
	}
	return n, err
}

func (r *maxSizeReader) Close() error {
	return r.rc.Close()
}

func newMaxSizeReader(rc io.ReadCloser, maxSize int) io.ReadCloser {
	return &maxSizeReader{rc: rc, limit: maxSize}
}

func (w *WebDAVBackend) Delete(ctx context.Context, filename string) error {
	_ = ctx
	full, err := w.fullPath(filename)
	if err != nil {
		return fmt.Errorf("delete error: %w", err)
	}
	return w.client.Remove(full)
}

func (w *WebDAVBackend) UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error {
	_ = idx
	return w.Upload(ctx, filename, data)
}

func (w *WebDAVBackend) UploadAny(ctx context.Context, filename string, data io.Reader) (uint8, error) {
	return 0, w.Upload(ctx, filename, data)
}

func (w *WebDAVBackend) DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error) {
	_ = idx
	return w.Download(ctx, filename)
}
