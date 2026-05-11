package storage

import (
	"bytes"
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

// MaxFileSize is the maximum file size allowed for upload/download (16MB)
const MaxFileSize = 16 * 1024 * 1024

type WebDAVBackend struct {
	client   *gowebdav.Client
	token    string
	login    string
	rootURL  string
	basePath string
}

func NewWebDAVBackend(provider, login, token, basePath, url string) (*WebDAVBackend, error) {
	// Only custom provider is supported
	if provider != "custom" {
		return nil, fmt.Errorf("only 'custom' provider is supported, got: %s", provider)
	}
	if url == "" {
		return nil, fmt.Errorf("URL is required for custom WebDAV provider")
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
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	client.SetTransport(transport)
	backend := &WebDAVBackend{
		client:  client,
		token:   token,
		login:   login,
		rootURL: rootURL,
		basePath: basePath,
	}

	// Test connection
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
	if w.basePath == "" {
		return name, nil
	}
	return path.Join(w.basePath, name), nil
}

func (w *WebDAVBackend) Login(ctx context.Context) error {
	// Create basePath directory if specified
	if w.basePath != "" {
		err := w.client.Mkdir(w.basePath, 0755)
		if err != nil {
			// Ignore "already exists" errors - check if path exists now
			_, statErr := w.client.Stat(w.basePath)
			if statErr != nil {
				return fmt.Errorf("failed to create basePath %s: %w", w.basePath, err)
			}
		}
	}
	return nil
}

func (w *WebDAVBackend) ListQuery(ctx context.Context, prefix string) ([]FileEntry, error) {
	dir := "/"
	if w.basePath != "" {
		dir = w.basePath
	}
	files, err := w.client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list error: %w", err)
	}

	var result []FileEntry
	for _, f := range files {
		if strings.HasPrefix(f.Name(), prefix) {
			result = append(result, FileEntry{Filename: f.Name(), BackendIdx: 0, ModTime: f.ModTime()})
		}
	}
	return result, nil
}

func (w *WebDAVBackend) Upload(ctx context.Context, filename string, data io.Reader) error {
	full, err := w.fullPath(filename)
	if err != nil {
		return fmt.Errorf("upload error: %w", err)
	}

	// Limit read to MaxFileSize+1 to prevent OOM while detecting truncation
	limitedReader := io.LimitReader(data, MaxFileSize+1)
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
	// Note: gowebdav client.Read returns []byte, so we can't do true streaming
	// For large files, consider using a different WebDAV library or direct HTTP
	full, err := w.fullPath(filename)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	content, err := w.client.Read(full)
	if err != nil {
		return nil, fmt.Errorf("download error: %w", err)
	}
	
	// Limit the size to prevent memory issues
	if len(content) > MaxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", len(content), MaxFileSize)
	}
	
	return io.NopCloser(bytes.NewReader(content)), nil
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

