// Package flowdavmobile — gomobile bridge exporting proxy API to Android (Kotlin/Java).
package flowdavmobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
		line := buf[idx]
		if len(out)+len(line)+1 > 1500 {
			out = append(out, "..."...)
			break
		}
		out = append(out, line...)
		out = append(out, '\n')
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
	logger.Info("StartProxyFromData: len=%d, encrypted=%t, listen=%q", len(data), len(data) > 0 && data[0] != '{', listenAddr)

	var appCfg *config.AppConfig

	if len(data) > 0 && data[0] == '{' {
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
	logger.Info("StartProxyManual: url=%q, listen=%q", webdavURL, listenAddr)

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

// SetSocks5Auth sets SOCKS5 authentication credentials for the next manual proxy.
// Must be called before or immediately after StartProxyManual.
func SetSocks5Auth(user, pass string) {
	mu.Lock()
	pendingSocks5User = user
	pendingSocks5Pass = pass
	mu.Unlock()
}

func startProxy(appCfg *config.AppConfig, listenAddr string) error {
	mu.Lock()
	defer mu.Unlock()

	lastErrCode = ""

	if eng != nil {
		return coded(ErrAlreadyRunning, errors.New("proxy already running"))
	}

	logger.SetLevel(appCfg.LogLevel)
	logEvent("Starting proxy on %s (WebDAV: %s)", listenAddr, appCfg.WebDAV.URL)
	if pendingSocks5User != "" {
		appCfg.SOCKS5User = pendingSocks5User
		appCfg.SOCKS5Pass = pendingSocks5Pass
		pendingSocks5User = ""
		pendingSocks5Pass = ""
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

func stopLocked() {
	if eng == nil {
		return
	}
	if socksLnr != nil {
		socksLnr.Close()
		socksLnr = nil
	}
	eng.Stop()
	if cancel != nil {
		cancel()
		cancel = nil
	}
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
	logEvent("Proxy stopped (error: %s)", code)
	return string(code)
}
