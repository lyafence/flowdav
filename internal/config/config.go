// Package config — load, validate, encrypt/decrypt configuration files.
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrEncryptedConfig is returned when attempting to load an encrypted config without a password.
var ErrEncryptedConfig = errors.New("config file is encrypted, use -p flag")

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

// ValidateBasePath checks a base_path for path traversal and invalid characters.
// It performs URL-decoding (including double-decoding) to catch encoded traversal
// sequences like %2e%2e%2f, %252e%252e%252f, etc.
func ValidateBasePath(basePath string, field string) error {
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
		// Re-check null bytes after decoding to catch %00 encoding
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
// Supports single backend (url) or multiple backends for round-robin (backends).
type WebDAVConfig struct {
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

	// MinPollMs is the minimum polling interval in milliseconds (idle backoff floor).
	// Default 500ms. Only used when RefreshRateMs is not set.
	MinPollMs int `json:"min_poll_ms,omitempty"`

	// MaxPollMs is the maximum polling interval in milliseconds (idle backoff ceiling).
	// Default 60000ms (60s). Only used when RefreshRateMs is not set.
	MaxPollMs int `json:"max_poll_ms,omitempty"`

	// FlushRateMs is the gathering (TX) interval in milliseconds for the engine.
	// Lower values = lower latency but more files created. Default 500ms.
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

	// MaxSessions limits concurrent WebDAV sessions. 0 = unlimited (default).
	MaxSessions int `json:"max_sessions,omitempty"`

	// MaxMessageSize limits the maximum envelope payload size in bytes.
	// Default 16777216 (16MB). Must be ≥65536 (64KB). Applied to both
	// transport.MaxMessageSize and storage.MaxFileSize.
	MaxMessageSize int `json:"max_message_size,omitempty"`

	// HealthPort enables a lightweight HTTP health server on this port.
	// When set, a GET /health endpoint is available returning JSON Engine stats.
	// If empty, no health server is started (zero overhead).
	HealthPort string `json:"health_port,omitempty"`

	// TLSFingerprint sets the TLS fingerprint profile for WebDAV connections.
	// Supported: "" (default=Chrome 133), "chrome", "chrome_auto".
	// Hardcoded to Chrome 133 when unset — no need to configure unless you
	// need a different profile for compatibility reasons.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`

	// IdleTimeoutMs is the session idle timeout in milliseconds.
	// If no data is exchanged for this duration, the session is
	// automatically closed. Default 10000 (10s). 0 = use default.
	IdleTimeoutMs int `json:"idle_timeout_ms,omitempty"`

	// PaddingSize is the bucket size in bytes for tail padding.
	// All uploaded files are padded to a multiple of this size
	// plus random slack. 0 = disabled.
	PaddingSize int `json:"padding_size,omitempty"`

	// HoldMs is the maximum random delay in milliseconds before
	// the server responds to a request. Decouples request/response
	// timing. 0 = disabled.
	HoldMs int `json:"hold_ms,omitempty"`
}

// ValidateAppConfig validates the config fields and decodes encryption keys.
// Populates cfg.EncKeyDecoded and cfg.HMacKeyDecoded on success.
// Called by Load and LoadEncrypted — all config validation lives here.
func ValidateAppConfig(cfg *AppConfig) error {
	if cfg.WebDAV == nil {
		return fmt.Errorf("webdav config is required")
	}

	// Unified format: check if single backend or multiple backends
	hasBackendsArray := len(cfg.WebDAV.Backends) > 0
	hasLegacyBackend := cfg.WebDAV.URL != ""

	if hasBackendsArray && hasLegacyBackend {
		return fmt.Errorf("cannot use both 'webdav.backends' and legacy 'webdav.url' simultaneously")
	}

	if hasBackendsArray {
		// Multi-backend mode
		if len(cfg.WebDAV.Backends) < 2 {
			return fmt.Errorf("webdav.backends requires at least 2 backends, got %d", len(cfg.WebDAV.Backends))
		}
		for i, be := range cfg.WebDAV.Backends {
			if be.BasePath != "" {
				if err := ValidateBasePath(be.BasePath, fmt.Sprintf("webdav.backends[%d].base_path", i)); err != nil {
					return err
				}
			}
		}
	} else if hasLegacyBackend {
		// Single backend mode (legacy format)
		if cfg.WebDAV.BasePath != "" {
			if err := ValidateBasePath(cfg.WebDAV.BasePath, "webdav.base_path"); err != nil {
				return err
			}
		}
	} else {
		return fmt.Errorf("webdav config requires either 'url' or 'backends'")
	}

	// Validate encryption keys (required)
	if cfg.EncKey == "" {
		return fmt.Errorf("enc_key is required")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.EncKey)
	if err != nil {
		return fmt.Errorf("invalid enc_key: not valid base64: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("invalid enc_key: must be 32 bytes after base64 decoding, got %d", len(key))
	}

	if cfg.HMacKey == "" {
		return fmt.Errorf("hmac_key is required")
	}
	hmacKey, err := base64.StdEncoding.DecodeString(cfg.HMacKey)
	if err != nil {
		return fmt.Errorf("invalid hmac_key: not valid base64: %w", err)
	}
	if len(hmacKey) != 32 {
		return fmt.Errorf("invalid hmac_key: must be 32 bytes after base64 decoding, got %d", len(hmacKey))
	}

	if bytes.Equal(key, hmacKey) {
		return fmt.Errorf("enc_key and hmac_key must be different")
	}

	// Validate MaxMessageSize (if set)
	if cfg.MaxMessageSize > 0 && cfg.MaxMessageSize < 65536 {
		return fmt.Errorf("max_message_size must be at least 65536 (64KB), got %d", cfg.MaxMessageSize)
	}

	// Validate TLSFingerprint (if set)
	if cfg.TLSFingerprint != "" && cfg.TLSFingerprint != "chrome" && cfg.TLSFingerprint != "chrome_auto" {
		return fmt.Errorf("invalid tls_fingerprint: %q (supported: chrome, chrome_auto)", cfg.TLSFingerprint)
	}

	// Validate HealthPort — must be loopback for security (design invariant)
	if cfg.HealthPort != "" {
		if !strings.HasPrefix(cfg.HealthPort, "127.0.0.1:") &&
			!strings.HasPrefix(cfg.HealthPort, "localhost:") &&
			!strings.HasPrefix(cfg.HealthPort, "[::1]:") {
			return fmt.Errorf("health_port must be loopback (127.0.0.1, localhost, or [::1]), got %q", cfg.HealthPort)
		}
	}

	// Store decoded keys for use by transport layer
	cfg.EncKeyDecoded = key
	cfg.HMacKeyDecoded = hmacKey

	return nil
}

// Load reads and parses a JSON config file.
func Load(path string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	// Check if this looks like an encrypted config (starts with salt)
	if len(b) > saltLen+nonceLen && !isJSON(b) {
		return nil, ErrEncryptedConfig
	}

	var cfg AppConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	if err := ValidateAppConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadEncrypted reads and decrypts an encrypted config file.
func LoadEncrypted(path, password string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read encrypted config %s: %w", path, err)
	}
	enc, err := UnmarshalEncrypted(b)
	if err != nil {
		return nil, fmt.Errorf("invalid encrypted config: %w", err)
	}
	plaintext, err := DecryptConfig(enc, password)
	if err != nil {
		return nil, err
	}
	var cfg AppConfig
	if err := json.Unmarshal(plaintext, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse decrypted config JSON: %w", err)
	}
	if err := ValidateAppConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadConfig handles config loading with encrypted fallback and
// interactive password prompt. Returns the parsed config or an error.
func LoadConfig(path, password string, askInteractive bool) (*AppConfig, error) {
	cfg, err := Load(path)
	if err == ErrEncryptedConfig {
		if password == "" && askInteractive {
			fmt.Print("Master password: ")
			pass, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return nil, fmt.Errorf("failed to read password: %w", err)
			}
			password = string(pass)
		}
		if password == "" {
			return nil, fmt.Errorf("config is encrypted. Use -p <password> or -p (interactive) or set FLOWDAV_PASSWORD env var")
		}
		cfg, err = LoadEncrypted(path, password)
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// ResolvePassword scans args for -p before flag.Parse.
// Returns password value, whether interactive prompt is requested, and cleaned args.
// Priority: -p VALUE > -p (interactive) > FLOWDAV_PASSWORD env var.
func ResolvePassword(args []string) (password string, interactive bool, rest []string) {
	password = os.Getenv("FLOWDAV_PASSWORD")
	for i := 0; i < len(args); i++ {
		if args[i] == "-p" || args[i] == "--password" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				password = args[i+1]
				rest = append(rest, args[:i]...)
				rest = append(rest, args[i+2:]...)
				return password, false, rest
			}
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return password, true, rest
		}
		if strings.HasPrefix(args[i], "-p=") {
			password = strings.TrimPrefix(args[i], "-p=")
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return password, false, rest
		}
		if args[i] == "-c" || args[i] == "--client" ||
			args[i] == "-s" || args[i] == "--server" ||
			args[i] == "-l" || args[i] == "--log" {
			i++ // skip value
		}
	}
	return password, false, args
}

func isJSON(data []byte) bool {
	data = trimLeftWhitespace(data)
	return len(data) > 0 && (data[0] == '{' || data[0] == '[')
}

func trimLeftWhitespace(data []byte) []byte {
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\t' || data[0] == '\n' || data[0] == '\r') {
		data = data[1:]
	}
	return data
}
