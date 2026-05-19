package flowdavmobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

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
)

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

type rawResolver struct{}

func (rawResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type Status struct {
	Running        bool
	ActiveSessions int
	ListenAddr     string
	Error          string
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

func applyDefaults(cfg *config.AppConfig) {
	if cfg.StorageType == "" {
		cfg.StorageType = "webdav"
	}
}

func parseConfigJSON(data []byte) (*config.AppConfig, error) {
	var cfg config.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.WebDAV == nil {
		return nil, fmt.Errorf("webdav config is required")
	}
	hasBackends := len(cfg.WebDAV.Backends) > 0
	hasLegacy := cfg.WebDAV.URL != ""
	if hasBackends && hasLegacy {
		return nil, fmt.Errorf("cannot use both 'webdav.backends' and legacy 'webdav.url' simultaneously")
	}
	if hasBackends {
		if len(cfg.WebDAV.Backends) < 2 {
			return nil, fmt.Errorf("webdav.backends requires at least 2 backends, got %d", len(cfg.WebDAV.Backends))
		}
		for i, be := range cfg.WebDAV.Backends {
			if be.BasePath != "" {
				if err := config.ValidateBasePath(be.BasePath, fmt.Sprintf("webdav.backends[%d].base_path", i)); err != nil {
					return nil, err
				}
			}
		}
	} else if hasLegacy {
		if cfg.WebDAV.BasePath != "" {
			if err := config.ValidateBasePath(cfg.WebDAV.BasePath, "webdav.base_path"); err != nil {
				return nil, err
			}
		}
	} else {
		return nil, fmt.Errorf("webdav config requires either 'url' or 'backends'")
	}
	if cfg.EncKey == "" {
		return nil, fmt.Errorf("enc_key is required")
	}
	key, err := decodeKey(cfg.EncKey, "enc_key")
	if err != nil {
		return nil, err
	}
	cfg.EncKeyDecoded = key
	if cfg.HMacKey == "" {
		return nil, fmt.Errorf("hmac_key is required")
	}
	hmacKey, err := decodeKey(cfg.HMacKey, "hmac_key")
	if err != nil {
		return nil, err
	}
	cfg.HMacKeyDecoded = hmacKey
	if cfg.MaxMessageSize > 0 && cfg.MaxMessageSize < 65536 {
		return nil, fmt.Errorf("max_message_size must be at least 65536 (64KB), got %d", cfg.MaxMessageSize)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// StartProxy loads config from file and starts the proxy.
func StartProxy(configPath, password, listenAddr string) error {
	appCfg, err := config.LoadConfig(configPath, password, false)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return startProxy(appCfg, listenAddr)
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
	logger.Info("startProxy: listen=%q, wdav_url=%q", listenAddr, appCfg.WebDAV.URL)

	if pendingSocks5User != "" {
		appCfg.SOCKS5User = pendingSocks5User
		appCfg.SOCKS5Pass = pendingSocks5Pass
		pendingSocks5User = ""
		pendingSocks5Pass = ""
	}

	backend, multiBackend, err := storage.NewBackendFromConfig(appCfg.WebDAV)
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
	engine.Start(ctx)

	engine.OnSessionEnd = func(sessionID string) {
		logger.Info("Client session %s ended", sessionID)
	}

	maxConns := appCfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	connLimit := make(chan struct{}, maxConns)

	serverOpts := []socks5.Option{
		socks5.WithDial(func(dc context.Context, network, addr string) (net.Conn, error) {
			sessionID := generateSessionID()
			select {
			case connLimit <- struct{}{}:
			default:
				return nil, transport.ErrTooManyConns
			}
			released := false
			defer func() {
				if !released {
					<-connLimit
				}
			}()
			session := transport.NewSession(sessionID)
			session.TargetAddr = addr
			if host, port, err := net.SplitHostPort(addr); err == nil {
				if net.ParseIP(host) != nil {
					logger.Info("New covert session %s targeting RAW IP %s:%s (Warning: Local DNS Leak?)", sessionID, host, port)
				} else {
					logger.Info("New covert session %s targeting SECURE DOMAIN %s:%s", sessionID, host, port)
				}
			}
			if multiBackend != nil {
				session.BackendIdx = uint8(storage.RandBackendIndex(multiBackend.NumBackends()))
				logger.Info("Session %s assigned to backend %d", sessionID, session.BackendIdx)
			}
			engine.AddSession(session)
			session.EnqueueTx(nil)
			released = true
			return transport.NewVirtualConnWithOnClose(session, engine, func() { <-connLimit }), nil
		}),
		socks5.WithAssociateHandle(func(ctx context.Context, w io.Writer, req *socks5.Request) error {
			socks5.SendReply(w, statute.RepCommandNotSupported, nil)
			return fmt.Errorf("UDP not supported")
		}),
		socks5.WithResolver(rawResolver{}),
	}

	if appCfg.SOCKS5User != "" && appCfg.SOCKS5Pass != "" {
		creds := socks5.StaticCredentials{appCfg.SOCKS5User: appCfg.SOCKS5Pass}
		serverOpts = append(serverOpts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.UserPassAuthenticator{Credentials: creds},
		}))
		logger.Info("SOCKS5 authentication enabled for user: %s", appCfg.SOCKS5User)
	} else {
		serverOpts = append(serverOpts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.NoAuthAuthenticator{},
		}))
		logger.Info("WARNING: SOCKS5 server running WITHOUT authentication. Anyone with network access can use this proxy!")
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

	logger.Info("SOCKS5 proxy started on %s", listenAddr)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("SOCKS5 serve panic: %v", r)
			}
		}()
		if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			socksErrCh <- err
			mu.Lock()
			lastError = err.Error()
			mu.Unlock()
		}
	}()

	return nil
}

func StopProxy() {
	mu.Lock()
	defer mu.Unlock()
	logger.Info("StopProxy called")
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
	mu.Unlock()

	s := &Status{Running: e != nil, ListenAddr: addr, Error: errStr}
	if e != nil {
		stats := e.Stats()
		s.ActiveSessions = stats.ActiveSessions
	}
	return s
}

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
	logger.Info("StopAndError returning: %q", errMsg)
	return errMsg
}
