// Package flowdavmobile — gomobile bridge exporting proxy API to Android (Kotlin/Java).
package flowdavmobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/things-go/go-socks5"

	"github.com/lyafence/flowdav/internal/config"
	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
	"github.com/lyafence/flowdav/internal/transport"
)

var (
	version           = "dev"
	mu                sync.Mutex
	eng               *transport.Engine
	cancel            context.CancelFunc
	socksLnr          net.Listener
	socksErrCh        chan error
	currentAddr       string
	lastCfg           *config.AppConfig
	lastErrCode       ErrorCode
	pendingSocks5User string
	pendingSocks5Pass string

	logMu    sync.Mutex
	logBuf   [50]string
	logHead  int
	logCount int
)

// Version returns the build version of the gomobile bridge.
// It is set at build time via -ldflags.
func Version() string {
	return version
}

// IsEncryptedConfig reports whether data is an encrypted config rather than
// JSON text. Delegates to config.IsEncryptedConfig so the Android UI and the
// engine share one rule.
func IsEncryptedConfig(data []byte) bool {
	return config.IsEncryptedConfig(data)
}

func logEvent(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	logger.Info("%s", msg)
	ts := time.Now().Format("15:04:05")
	entry := ts + " " + msg

	logMu.Lock()
	logBuf[logHead] = entry
	logHead = (logHead + 1) % len(logBuf)
	if logCount < len(logBuf) {
		logCount++
	}
	logMu.Unlock()
}

// redactURL strips any userinfo (login/token) from raw before it reaches
// user-visible logs. Non-URL values are returned unchanged.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}

// maxLogBytes caps the log payload returned to the app UI.
const maxLogBytes = 1500

func getLogs() string {
	logMu.Lock()
	n := logCount
	head := logHead
	buf := logBuf
	logMu.Unlock()
	if n == 0 {
		return ""
	}

	out := make([]byte, 0, 1600)
	for i := 0; i < n; i++ {
		idx := (head - n + i) % len(buf)
		if idx < 0 {
			idx += len(buf)
		}
		out = append(out, buf[idx]...)
		out = append(out, '\n')
	}
	if len(out) > maxLogBytes {
		// Budget exceeded: keep the newest lines (truncate the old side).
		tail := out[len(out)-maxLogBytes:]
		if nl := strings.IndexByte(string(tail), '\n'); nl >= 0 && nl+1 < len(tail) {
			tail = tail[nl+1:]
		}
		return "...\n" + string(tail)
	}
	return string(out)
}

type Status struct {
	Running        bool
	ActiveSessions int
	ListenAddr     string
	ErrorCode      string
	ProcessedFiles int    `json:"processed_files"`
	WebdavURL      string `json:"webdav_url"`
	Logs           string
}

// ErrorCode is a stable, machine-readable failure identifier crossing the
// gomobile bridge. Android maps these to localized user-facing strings; Go
// messages stay developer-facing and are written only to the log buffer.
type ErrorCode string

const (
	ErrEncryptedNoPw  ErrorCode = "encrypted_no_password"
	ErrDecryptFailed  ErrorCode = "decrypt_failed"
	ErrInvalidConfig  ErrorCode = "invalid_config"
	ErrInvalidKey     ErrorCode = "invalid_key"
	ErrAlreadyRunning ErrorCode = "already_running"
	ErrStorageInit    ErrorCode = "storage_init"
	ErrListenFailed   ErrorCode = "listen_failed"
	ErrRuntime        ErrorCode = "runtime"
	ErrUnknown        ErrorCode = "unknown"
)

// StartResult reports the outcome of a StartProxy* call. Success is false on
// failure; Code is a stable ErrorCode; Message is a developer-facing detail and
// must never be shown to the user.
type StartResult struct {
	Success bool
	Code    string
	Message string
}

func okResult() *StartResult { return &StartResult{Success: true} }

func errResult(code ErrorCode, err error) *StartResult {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != "" {
		logger.Error("StartProxy failed [%s]: %s", code, msg)
	}
	return &StartResult{Success: false, Code: string(code), Message: msg}
}

// codedError wraps an error with a stable ErrorCode so callers can classify
// failures from internal helpers without matching on message text.
type codedError struct {
	code ErrorCode
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func coded(code ErrorCode, err error) error { return &codedError{code: code, err: err} }

func codeOf(err error) ErrorCode {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	return ErrUnknown
}

func decodeKey(keyB64 string, name string) ([]byte, error) {
	if keyB64 == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	dec, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	if len(dec) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes, got %d", name, len(dec))
	}
	return dec, nil
}

func parseConfigJSON(data []byte) (*config.AppConfig, error) {
	var cfg config.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := config.ValidateAppConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// StartProxyFromData parses config from raw bytes and starts the proxy.
func StartProxyFromData(data []byte, password, listenAddr string) *StartResult {
	logger.Info("StartProxyFromData: len=%d, encrypted=%t, listen=%q", len(data), config.IsEncryptedConfig(data), listenAddr)

	var appCfg *config.AppConfig

	if !config.IsEncryptedConfig(data) {
		cfg, err := parseConfigJSON(data)
		if err != nil {
			return errResult(ErrInvalidConfig, err)
		}
		appCfg = cfg
	}

	if appCfg == nil {
		if password == "" {
			return errResult(ErrEncryptedNoPw, nil)
		}
		enc, err := config.UnmarshalEncrypted(data)
		if err != nil {
			return errResult(ErrInvalidConfig, err)
		}
		plaintext, err := config.DecryptConfig(enc, password)
		if err != nil {
			return errResult(ErrDecryptFailed, err)
		}
		cfg, err := parseConfigJSON(plaintext)
		if err != nil {
			return errResult(ErrInvalidConfig, err)
		}
		appCfg = cfg
	}

	if err := startProxy(appCfg, listenAddr); err != nil {
		return errResult(codeOf(err), err)
	}
	return okResult()
}

// StartProxyManual creates a proxy from explicit WebDAV and crypto parameters.
func StartProxyManual(webdavURL, webdavLogin, webdavToken, encKeyBase64, hmacKeyBase64, listenAddr string) *StartResult {
	logger.Info("StartProxyManual: url=%q, listen=%q", redactURL(webdavURL), listenAddr)

	encKey, err := decodeKey(encKeyBase64, "enc_key")
	if err != nil {
		return errResult(ErrInvalidKey, err)
	}
	hmacKey, err := decodeKey(hmacKeyBase64, "hmac_key")
	if err != nil {
		return errResult(ErrInvalidKey, err)
	}

	appCfg := &config.AppConfig{
		WebDAV: &config.WebDAVConfig{
			URL:   webdavURL,
			Login: webdavLogin,
			Token: webdavToken,
		},
		EncKey:         encKeyBase64,
		HMacKey:        hmacKeyBase64,
		EncKeyDecoded:  encKey,
		HMacKeyDecoded: hmacKey,
		LogLevel:       "info",
	}
	if err := config.ValidateAppConfig(appCfg); err != nil {
		return errResult(ErrInvalidConfig, err)
	}
	if err := startProxy(appCfg, listenAddr); err != nil {
		return errResult(codeOf(err), err)
	}
	return okResult()
}

// SetSocks5Auth sets SOCKS5 authentication credentials for the next proxy
// start. Must be called before StartProxyManual/StartProxyFromData. A partial
// pair (only one of user/pass) is rejected — otherwise the proxy could
// silently fall back to NoAuth. Pending credentials are cleared on stop.
func SetSocks5Auth(user, pass string) {
	mu.Lock()
	defer mu.Unlock()
	if (user == "") != (pass == "") {
		logger.Error("SetSocks5Auth: partial SOCKS5 credential pair rejected")
		pendingSocks5User = ""
		pendingSocks5Pass = ""
		return
	}
	pendingSocks5User = user
	pendingSocks5Pass = pass
}

// StorageTestResult reports the outcome of the storage round-trip self-test.
// Code is a stable, machine-readable value; Message is developer-facing and
// must never be shown to the user.
type StorageTestResult struct {
	Success bool
	Code    string
	Message string
}

const (
	storageTestOK      = "ok"
	storageTestNotRun  = "not_running"
	storageTestUnreach = "storage_unreachable"
	storageTestAuth    = "storage_auth"
)

// TestStorage runs a WebDAV upload/readback/delete round-trip through the
// running proxy's storage backend. It verifies URL, credentials, TLS and
// permissions without involving the remote flowdav server, so the outcome is
// independent of poll tuning (max_poll_ms and friends).
func TestStorage() *StorageTestResult {
	mu.Lock()
	engine := eng
	mu.Unlock()
	if engine == nil {
		return &StorageTestResult{Success: false, Code: storageTestNotRun, Message: "proxy is not running"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := engine.StorageRoundTrip(ctx); err != nil {
		code := storageTestUnreach
		var he *storage.HTTPError
		if errors.As(err, &he) && (he.Code == http.StatusUnauthorized || he.Code == http.StatusForbidden) {
			code = storageTestAuth
		}
		logger.Error("Storage self-test failed [%s]: %v", code, err)
		return &StorageTestResult{Success: false, Code: code, Message: err.Error()}
	}
	return &StorageTestResult{Success: true, Code: storageTestOK}
}

// ProbeTimeoutMs bounds the whole end-to-end probe. A cold server may only
// poll after its idle backoff (max_poll_ms, 60s by default), so the budget
// covers one full poll cycle plus margin; the caller splits it into short
// attempts. Falls back to the default 60s poll when no config is loaded.
func ProbeTimeoutMs() int32 {
	mu.Lock()
	poll := 60 * time.Second
	if lastCfg != nil && lastCfg.MaxPollMs > 0 {
		poll = time.Duration(lastCfg.MaxPollMs) * time.Millisecond
	}
	mu.Unlock()
	ms := poll.Milliseconds() + 12000
	if ms < 20000 {
		ms = 20000
	}
	if ms > 90000 {
		ms = 90000
	}
	return int32(ms)
}

func startProxy(appCfg *config.AppConfig, listenAddr string) error {
	mu.Lock()
	defer mu.Unlock()

	lastErrCode = ""

	if eng != nil {
		return coded(ErrAlreadyRunning, errors.New("proxy already running"))
	}

	logger.SetLevel(appCfg.LogLevel)
	logEvent("Starting proxy on %s (WebDAV: %s)", listenAddr, redactURL(appCfg.WebDAV.URL))
	if pendingSocks5User != "" {
		appCfg.SOCKS5User = pendingSocks5User
		appCfg.SOCKS5Pass = pendingSocks5Pass
		pendingSocks5User = ""
		pendingSocks5Pass = ""
	}
	// Re-validate the effective config: pending credentials are overlaid
	// after the callers' validation, so any bad overlay must be caught here
	// before the proxy starts.
	if err := config.ValidateAppConfig(appCfg); err != nil {
		return coded(ErrInvalidConfig, err)
	}

	backend, multiBackend, err := storage.NewBackendFromConfig(appCfg.WebDAV, appCfg.TLSFingerprint)
	if err != nil {
		return coded(ErrStorageInit, err)
	}

	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	ctx, ctxCancel := context.WithCancel(context.Background())
	engine := transport.NewEngine(backend, true, cryptoCfg)
	transport.ApplyConfigToEngine(appCfg, engine)
	engine.Start(ctx)
	engine.OnSessionEnd = func(sessionID string) {
		logEvent("Session %s ended", sessionID)
	}

	serverOpts, err := transport.NewSocks5Options(transport.Socks5Config{
		ListenAddr:   listenAddr,
		User:         appCfg.SOCKS5User,
		Pass:         appCfg.SOCKS5Pass,
		MaxConns:     appCfg.MaxConnections,
		Engine:       engine,
		MultiBackend: multiBackend,
		LogFn:        logEvent,
	})
	if err != nil {
		engine.Stop()
		ctxCancel()
		logger.Error("SOCKS5 options: %v", err)
		return coded(ErrUnknown, err)
	}

	server := socks5.NewServer(serverOpts...)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		engine.Stop()
		ctxCancel()
		logger.Error("listen %s failed: %v", listenAddr, err)
		return coded(ErrListenFailed, err)
	}

	cancel = ctxCancel
	eng = engine
	socksLnr = listener
	currentAddr = listenAddr
	lastCfg = appCfg
	socksErrCh = make(chan error, 1)

	logEvent("SOCKS5 proxy started on %s", listenAddr)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("SOCKS5 serve panic: %v", r)
				mu.Lock()
				lastErrCode = ErrRuntime
				stopLocked()
				mu.Unlock()
				logEvent("SOCKS5 serve panic: %v", r)
			}
		}()
		if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			mu.Lock()
			if socksErrCh != nil {
				select {
				case socksErrCh <- err:
				default:
				}
			}
			lastErrCode = ErrRuntime
			stopLocked()
			mu.Unlock()
			logEvent("SOCKS5 error: %v", err)
		}
	}()

	return nil
}

func StopProxy() {
	mu.Lock()
	defer mu.Unlock()
	logEvent("Proxy stopped")
	stopLocked()
}

// stopDrainTimeout bounds the engine drain during StopProxy, mirroring the
// CLI's graceful shutdown (cancel → Drain → Stop) on a mobile budget.
const stopDrainTimeout = 2 * time.Second

func stopLocked() {
	pendingSocks5User = ""
	pendingSocks5Pass = ""
	if eng == nil {
		return
	}
	if socksLnr != nil {
		socksLnr.Close()
		socksLnr = nil
	}
	// CLI shutdown order: cancel first (worker lifetime binds to stopCh),
	// then Drain extracts the txBuf tail, then Stop. Without Drain the tail
	// is silently dropped.
	if cancel != nil {
		cancel()
		cancel = nil
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), stopDrainTimeout)
	defer cancelDrain()
	if derr := eng.Drain(drainCtx); derr != nil {
		logEvent("Drain incomplete: %v", derr)
	}
	eng.Stop()
	if lastCfg != nil {
		config.WipeBytes(lastCfg.EncKeyDecoded)
		config.WipeBytes(lastCfg.HMacKeyDecoded)
		lastCfg.EncKey = ""
		lastCfg.HMacKey = ""
		lastCfg = nil
	}
	eng = nil
	currentAddr = ""
}

func GetStatus() *Status {
	mu.Lock()
	e := eng
	addr := currentAddr
	errCode := lastErrCode
	wdavURL := ""
	if lastCfg != nil && lastCfg.WebDAV != nil {
		wdavURL = lastCfg.WebDAV.URL
	}
	mu.Unlock()

	s := &Status{Running: e != nil, ListenAddr: addr, ErrorCode: string(errCode), WebdavURL: wdavURL}
	if e != nil {
		stats := e.Stats()
		s.ActiveSessions = stats.ActiveSessions
		s.ProcessedFiles = stats.ProcessedFiles
	}
	s.Logs = getLogs()
	return s
}

// StopAndError returns a deferred error code from after a successful start:
// phase 1 reports start failures via *StartResult, phase 2 (SOCKS5 goroutine
// died shortly after a successful start) surfaces ErrRuntime here. Empty means
// no deferred error.
func StopAndError() string {
	mu.Lock()
	defer mu.Unlock()

	code := ErrorCode("")
	if socksErrCh != nil {
		select {
		case err := <-socksErrCh:
			logger.Error("StopAndError: SOCKS error: %v", err)
			code = ErrRuntime
		default:
		}
	}
	stopLocked()
	if code != "" {
		logEvent("Proxy stopped (error: %s)", code)
	}
	return string(code)
}
