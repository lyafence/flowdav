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
	mu                sync.Mutex
	eng               *transport.Engine
	cancel            context.CancelFunc
	socksLnr          net.Listener
	socksErrCh        chan error
	currentAddr       string
	lastCfg           *config.AppConfig
	lastError         string
	pendingSocks5User string
	pendingSocks5Pass string

	logMu    sync.Mutex
	logBuf   [50]string
	logHead  int
	logCount int
)

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

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

type Status struct {
	Running        bool
	ActiveSessions int
	ListenAddr     string
	Error          string
	ProcessedFiles int    `json:"processed_files"`
	WebdavURL      string `json:"webdav_url"`
	Logs           string
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
func StartProxyFromData(data []byte, password, listenAddr string) error {
	logger.Info("StartProxyFromData: len=%d, encrypted=%t, listen=%q", len(data), len(data) > 0 && data[0] != '{', listenAddr)

	var (
		appCfg *config.AppConfig
		err    error
	)

	if len(data) > 0 && data[0] == '{' {
		appCfg, err = parseConfigJSON(data)
		if err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	if appCfg == nil {
		if password == "" {
			return fmt.Errorf("config is encrypted but no password provided")
		}
		enc, err := config.UnmarshalEncrypted(data)
		if err != nil {
			return fmt.Errorf("invalid encrypted config: %w", err)
		}
		plaintext, err := config.DecryptConfig(enc, password)
		if err != nil {
			logger.Error("decryption failed")
			return fmt.Errorf("decryption failed: %w", err)
		}
		appCfg, err = parseConfigJSON(plaintext)
		if err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	return startProxy(appCfg, listenAddr)
}

// StartProxyManual creates a proxy from explicit WebDAV and crypto parameters.
func StartProxyManual(webdavURL, webdavLogin, webdavToken, encKeyBase64, hmacKeyBase64, listenAddr string) error {
	logger.Info("StartProxyManual: url=%q, listen=%q", webdavURL, listenAddr)

	encKey, err := decodeKey(encKeyBase64, "enc_key")
	if err != nil {
		return err
	}
	hmacKey, err := decodeKey(hmacKeyBase64, "hmac_key")
	if err != nil {
		return err
	}

	appCfg := &config.AppConfig{
		StorageType: "webdav",
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
	return startProxy(appCfg, listenAddr)
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

	if eng != nil {
		return errors.New("proxy already running")
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
		return fmt.Errorf("storage: %w", err)
	}

	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	if appCfg.MaxMessageSize > 0 {
		transport.MaxMessageSize = appCfg.MaxMessageSize
		storage.MaxFileSize = appCfg.MaxMessageSize
	}

	ctx, ctxCancel := context.WithCancel(context.Background())
	engine := transport.NewEngine(backend, true, cryptoCfg)

	if appCfg.RefreshRateMs > 0 {
		engine.SetPollRate(appCfg.RefreshRateMs)
	}
	if appCfg.MinPollMs > 0 {
		engine.SetMinPollRate(appCfg.MinPollMs)
	}
	if appCfg.MaxPollMs > 0 {
		engine.SetMaxPollRate(appCfg.MaxPollMs)
	}
	if appCfg.FlushRateMs > 0 {
		engine.SetFlushRate(appCfg.FlushRateMs)
	}
	if appCfg.MaxSessions > 0 {
		engine.SetMaxSessions(appCfg.MaxSessions)
	}
	if appCfg.IdleTimeoutMs > 0 {
		engine.SetSessionIdleTimeout(appCfg.IdleTimeoutMs)
	}
	if appCfg.PaddingSize > 0 {
		engine.SetPaddingSize(appCfg.PaddingSize)
	}
	if appCfg.HoldMs > 0 {
		engine.SetHoldMax(appCfg.HoldMs)
	}
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
		return fmt.Errorf("SOCKS5 options: %w", err)
	}

	server := socks5.NewServer(serverOpts...)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		engine.Stop()
		ctxCancel()
		logger.Error("listen %s failed: %v", listenAddr, err)
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}

	cancel = ctxCancel
	eng = engine
	socksLnr = listener
	currentAddr = listenAddr
	lastCfg = appCfg
	socksErrCh = make(chan error, 1)
	lastError = ""

	logEvent("SOCKS5 proxy started on %s", listenAddr)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("SOCKS5 serve panic: %v", r)
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
			lastError = err.Error()
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
		wipeBytes(lastCfg.EncKeyDecoded)
		wipeBytes(lastCfg.HMacKeyDecoded)
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
	errStr := lastError
	wdavURL := ""
	if lastCfg != nil && lastCfg.WebDAV != nil {
		wdavURL = lastCfg.WebDAV.URL
	}
	mu.Unlock()

	s := &Status{Running: e != nil, ListenAddr: addr, Error: errStr, WebdavURL: wdavURL}
	if e != nil {
		stats := e.Stats()
		s.ActiveSessions = stats.ActiveSessions
		s.ProcessedFiles = stats.ProcessedFiles
	}
	s.Logs = getLogs()
	return s
}

// StopAndError returns a deferred error that occurred after a successful
// StartProxy* call.
//
// Two-phase error protocol:
//
//	Phase 1 — StartProxyFromData / StartProxyManual returns error synchronously
//	           (Go error → Java exception thrown by gomobile).
//	Phase 2 — If phase 1 succeeds but the SOCKS5 goroutine fails shortly
//	           after, StopAndError reads the deferred error from socksErrCh
//	           (filled by the SOCKS5 serve goroutine at line 381).
//
// Kotlin caller (MainActivity / ProxyManager) uses this sequence:
//
//	try { proxy.StartProxy*(...); if (!GetStatus().running) { err = StopAndError() } }
func StopAndError() string {
	mu.Lock()
	defer mu.Unlock()

	var errMsg string
	if socksErrCh != nil {
		select {
		case err := <-socksErrCh:
			errMsg = err.Error()
			logger.Error("StopAndError: SOCKS error: %s", errMsg)
		default:
		}
	}
	stopLocked()
	logEvent("Proxy stopped (error: %s)", errMsg)
	return errMsg
}
