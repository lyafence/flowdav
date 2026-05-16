package flowdavmobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
)

type rawResolver struct{}

func (rawResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

type Status struct {
	Running        bool
	ActiveSessions int
	ListenAddr     string
	Error          string
}

// StartProxy loads config from file and starts the proxy.
func StartProxy(configPath, password, listenAddr string) error {
	appCfg, err := config.LoadConfig(configPath, password, false)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return startProxy(appCfg, listenAddr)
}

// StartProxyManual creates a proxy from explicit WebDAV and crypto parameters.
func StartProxyManual(webdavURL, webdavLogin, webdavToken, encKeyBase64, hmacKeyBase64, listenAddr string) error {
	encKey, err := base64.StdEncoding.DecodeString(encKeyBase64)
	if err != nil {
		return fmt.Errorf("invalid enc_key: %w", err)
	}
	if len(encKey) != 32 {
		return fmt.Errorf("enc_key must be 32 bytes, got %d", len(encKey))
	}
	hmacKey, err := base64.StdEncoding.DecodeString(hmacKeyBase64)
	if err != nil {
		return fmt.Errorf("invalid hmac_key: %w", err)
	}
	if len(hmacKey) != 32 {
		return fmt.Errorf("hmac_key must be 32 bytes, got %d", len(hmacKey))
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
