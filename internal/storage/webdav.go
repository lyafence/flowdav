package storage

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
	gowebdav "github.com/studio-b12/gowebdav"
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

type WebDAVBackend struct {
	client     *gowebdav.Client
	httpClient *http.Client
	token      string
	login      string
	rootURL    string
	basePath   string
}

func NewWebDAVBackend(login, token, basePath, url string) (*WebDAVBackend, error) {
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
	// Connect directly to the rootURL (rclone serve webdav /data makes /data the root)
	client := gowebdav.NewClient(rootURL, login, token)
	transport := &http.Transport{
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
	client.SetTransport(transport)
	client.SetTimeout(90 * time.Second)
	httpClient := &http.Client{
		Timeout:   90 * time.Second,
		Transport: transport,
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
			if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
				return fmt.Errorf("WebDAV URL cannot point to private/loopback IP: %s (resolved from %s)", ip, host)
			}
		}
		return nil
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
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

func (w *WebDAVBackend) Login(ctx context.Context) error {
	// Create basePath + subdirectories
	dirs := []string{w.basePath}
	if w.basePath != "" {
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

func (w *WebDAVBackend) ListQuery(ctx context.Context, prefix string) ([]FileEntry, error) {
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

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download error: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > int64(MaxFileSize) {
		resp.Body.Close()
		return nil, fmt.Errorf("download error: file too large: %d bytes (max %d)", resp.ContentLength, MaxFileSize)
	}

	return newLimitReadCloser(resp.Body, int64(MaxFileSize)+1), nil
}

type limitReadCloser struct {
	io.Reader
	io.Closer
}

func newLimitReadCloser(r io.ReadCloser, limit int64) io.ReadCloser {
	return &limitReadCloser{
		Reader: io.LimitReader(r, limit),
		Closer: r,
	}
}

func (w *WebDAVBackend) Delete(ctx context.Context, filename string) error {
	full, err := w.fullPath(filename)
	if err != nil {
		return fmt.Errorf("delete error: %w", err)
	}
	return w.client.Remove(full)
}

func (w *WebDAVBackend) UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error {
	return w.Upload(ctx, filename, data)
}

func (w *WebDAVBackend) DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error) {
	return w.Download(ctx, filename)
}

