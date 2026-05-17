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
	mu          sync.Mutex
	eng         *transport.Engine
	cancel      context.CancelFunc
	socksLnr    net.Listener
	socksErrCh  chan error
	currentAddr string
	lastCfg     *config.AppConfig
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
	if cfg.EncKey != "" {
		key, err := decodeKey(cfg.EncKey, "enc_key")
		if err != nil {
			return nil, err
		}
		cfg.EncKeyDecoded = key
	}
	if cfg.HMacKey != "" {
		key, err := decodeKey(cfg.HMacKey, "hmac_key")
		if err != nil {
			return nil, err
		}
		cfg.HMacKeyDecoded = key
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
	var appCfg *config.AppConfig

	if len(data) > 0 && data[0] == '{' {
		appCfg, _ = parseConfigJSON(data)
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

func startProxy(appCfg *config.AppConfig, listenAddr string) error {
	mu.Lock()
	defer mu.Unlock()

	if eng != nil {
		return errors.New("proxy already running")
	}

	logger.SetLevel(appCfg.LogLevel)

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
			session := transport.NewSession(sessionID)
			session.TargetAddr = addr
			if multiBackend != nil {
				session.BackendIdx = uint8(storage.RandBackendIndex(multiBackend.NumBackends()))
			}
			engine.AddSession(session)
			session.EnqueueTx(nil)
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
	} else {
		serverOpts = append(serverOpts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.NoAuthAuthenticator{},
		}))
	}

	server := socks5.NewServer(serverOpts...)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		engine.Stop()
		ctxCancel()
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}

	cancel = ctxCancel
	eng = engine
	socksLnr = listener
	currentAddr = listenAddr
	lastCfg = appCfg
	socksErrCh = make(chan error, 1)

	go func() {
		if err := server.Serve(listener); err != nil {
			socksErrCh <- err
		}
	}()

	return nil
}

func StopProxy() {
	mu.Lock()
	defer mu.Unlock()
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
		lastCfg = nil
	}
	eng = nil
	currentAddr = ""
}

func GetStatus() *Status {
	mu.Lock()
	e := eng
	addr := currentAddr
	mu.Unlock()

	s := &Status{Running: e != nil, ListenAddr: addr}
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
		default:
		}
	}
	stopLocked()
	return errMsg
}
