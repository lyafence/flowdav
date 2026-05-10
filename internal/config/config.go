package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// isPathTraversal checks if a path contains path traversal patterns.
// Specifically looks for "../" or "/../" patterns that could escape directories.
// Does not flag "./" or paths starting with "." followed by letter.
func isPathTraversal(path string) bool {
	// Check for "../" pattern anywhere
	if strings.Contains(path, "../") {
		return true
	}
	// Check if path is exactly ".." or starts with "../"
	if path == ".." {
		return true
	}
	if strings.HasPrefix(path, "../") {
		return true
	}
	// Check for trailing "/.." at end
	if strings.HasSuffix(path, "/..") {
		return true
	}
	return false
}

// validateBasePath checks a base_path for path traversal and invalid characters.
// It performs URL-decoding (including double-decoding) to catch encoded traversal
// sequences like %2e%2e%2f, %252e%252e%252f, etc.
func validateBasePath(basePath string, field string) error {
	// Reject null bytes and control characters
	if strings.ContainsAny(basePath, "\x00\x01\x02") {
		return fmt.Errorf("%s contains invalid characters", field)
	}

	// URL-decode once to catch %2e, %2f, etc.
	decoded, err := url.QueryUnescape(basePath)
	if err == nil {
		// URL-decode a second time to catch double-encoding like %252e
		decoded2, err2 := url.QueryUnescape(decoded)
		if err2 == nil {
			decoded = decoded2
		}
		// Re-check null bytes after decoding to catch %00 encoding (Audit H-004)
		if strings.ContainsAny(decoded, "\x00\x01\x02") {
			return fmt.Errorf("%s contains invalid characters after decoding", field)
		}
		if isPathTraversal(decoded) {
			return fmt.Errorf("%s contains path traversal sequence", field)
		}
	}

	// Also check the raw value for path traversal
	if isPathTraversal(basePath) {
		return fmt.Errorf("%s contains path traversal sequence", field)
	}

	return nil
}

// WebDAVConfig defines the WebDAV storage configuration.
// Supports single backend or multiple backends for round-robin.
type WebDAVConfig struct {
	Provider string         `json:"provider"`
	URL      string         `json:"url,omitempty"`
	Login    string         `json:"login"`
	Token    string         `json:"token"`
	BasePath string         `json:"base_path,omitempty"`
	Backends []WebDAVConfig `json:"backends,omitempty"`
}

// AppConfig defines the application-level overarching configuration.
type AppConfig struct {
	// ListenAddr is the SOCKS5 listening address for the client. E.g., "127.0.0.1:1080"
	ListenAddr string `json:"listen_addr,omitempty"`

	// StorageType defines the backend. Only "webdav" is supported.
	StorageType string `json:"storage_type"`

	// WebDAV contains the WebDAV configuration.
	// Can contain single backend or multiple backends (auto-detected).
	WebDAV *WebDAVConfig `json:"webdav,omitempty"`

	// EncKey is the AES-256 encryption key (base64).
	// Must be 32 bytes after base64 decoding. Used for envelope encryption.
	EncKey string `json:"enc_key"`

	// HMacKey is the HMAC-SHA256 key (base64).
	// Must be 32 bytes after base64 decoding. Used for HMAC verification.
	HMacKey string `json:"hmac_key"`

	// EncKeyDecoded is the decoded AES-256 key bytes (populated by Load).
	EncKeyDecoded []byte `json:"-"`
	// HMacKeyDecoded is the decoded HMAC-SHA256 key bytes (populated by Load).
	HMacKeyDecoded []byte `json:"-"`

	// RefreshRateMs is the polling (RX) interval in milliseconds for the engine.
	// Lower values = faster response but more API calls. Default 500ms.
	RefreshRateMs int `json:"refresh_rate_ms,omitempty"`

	// FlushRateMs is the gathering (TX) interval in milliseconds for the engine.
	// Lower values = lower latency but more files created. Default 300ms.
	FlushRateMs int `json:"flush_rate_ms,omitempty"`

	// LogLevel controls the verbosity of logs (debug, info, warn, error).
	LogLevel string `json:"log_level,omitempty"`

	// SOCKS5User is the username for SOCKS5 authentication.
	// If empty, no authentication is used (not recommended for production).
	SOCKS5User string `json:"socks5_user,omitempty"`

	// SOCKS5Pass is the password for SOCKS5 authentication.
	SOCKS5Pass string `json:"socks5_pass,omitempty"`

	// MaxConnections limits concurrent SOCKS5 connections to prevent resource exhaustion.
	// Default 100 if not specified.
	MaxConnections int `json:"max_connections,omitempty"`

	// HealthPort enables a lightweight HTTP health server on this port.
	// When set, a GET /health endpoint is available returning JSON Engine stats.
	// If empty, no health server is started (zero overhead).
	HealthPort string `json:"health_port,omitempty"`
}

// Load reads and parses a JSON config file.
func Load(path string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	// Validate required fields
	if cfg.StorageType == "" {
		return nil, fmt.Errorf("storage_type is required")
	}
	if cfg.StorageType != "webdav" {
		return nil, fmt.Errorf("only 'webdav' storage type is supported, got: %s", cfg.StorageType)
	}
	if cfg.WebDAV == nil {
		return nil, fmt.Errorf("webdav config is required")
	}

	// Unified format: check if single backend or multiple backends
	hasBackendsArray := len(cfg.WebDAV.Backends) > 0
	hasLegacyBackend := cfg.WebDAV.URL != ""

	if hasBackendsArray && hasLegacyBackend {
		return nil, fmt.Errorf("cannot use both 'webdav.backends' and legacy 'webdav.url' simultaneously")
	}

	if hasBackendsArray {
		// Multi-backend mode
		if len(cfg.WebDAV.Backends) < 2 {
			return nil, fmt.Errorf("webdav.backends requires at least 2 backends, got %d", len(cfg.WebDAV.Backends))
		}
		for i, be := range cfg.WebDAV.Backends {
			if be.Provider != "custom" {
				return nil, fmt.Errorf("webdav.backends[%d]: only 'custom' provider is supported", i)
			}
			if be.BasePath != "" {
				if err := validateBasePath(be.BasePath, fmt.Sprintf("webdav.backends[%d].base_path", i)); err != nil {
					return nil, err
				}
			}
		}
	} else if hasLegacyBackend {
		// Single backend mode (legacy format)
		if cfg.WebDAV.Provider != "custom" {
			return nil, fmt.Errorf("only 'custom' provider is supported for webdav")
		}
		if cfg.WebDAV.BasePath != "" {
			if err := validateBasePath(cfg.WebDAV.BasePath, "webdav.base_path"); err != nil {
				return nil, err
			}
		}
	} else {
		return nil, fmt.Errorf("webdav config requires either 'url' or 'backends'")
	}

	// Validate encryption keys (required)
	if cfg.EncKey == "" {
		return nil, fmt.Errorf("enc_key is required")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.EncKey)
	if err != nil {
		return nil, fmt.Errorf("invalid enc_key: not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid enc_key: must be 32 bytes after base64 decoding, got %d", len(key))
	}

	if cfg.HMacKey == "" {
		return nil, fmt.Errorf("hmac_key is required")
	}
	hmacKey, err := base64.StdEncoding.DecodeString(cfg.HMacKey)
	if err != nil {
		return nil, fmt.Errorf("invalid hmac_key: not valid base64: %w", err)
	}
	if len(hmacKey) != 32 {
		return nil, fmt.Errorf("invalid hmac_key: must be 32 bytes after base64 decoding, got %d", len(hmacKey))
	}

	// Store decoded keys for use by transport layer
	cfg.EncKeyDecoded = key
	cfg.HMacKeyDecoded = hmacKey

	return &cfg, nil
}
