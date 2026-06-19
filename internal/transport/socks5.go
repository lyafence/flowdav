package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
)

type Socks5Config struct {
	ListenAddr   string
	User         string
	Pass         string
	MaxConns     int
	Engine       *Engine
	MultiBackend *storage.MultiBackend
	LogFn        func(format string, args ...any)
}

func NewSocks5Options(cfg Socks5Config) ([]socks5.Option, error) {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 100
	}

	if cfg.User == "" || cfg.Pass == "" {
		loopback := true
		if cfg.ListenAddr != "" {
			host, _, err := net.SplitHostPort(cfg.ListenAddr)
			loopback = err == nil && (host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()))
		}
		if !loopback {
			logMsg("SOCKS5 listening on non-loopback address %s without authentication", cfg.LogFn, cfg.ListenAddr)
		}
	}

	connLimit := make(chan struct{}, cfg.MaxConns)

	opts := []socks5.Option{
		socks5.WithDial(func(_ context.Context, _, addr string) (net.Conn, error) {
			sessionID := generateSessionID()

			host, port, err := net.SplitHostPort(addr)
			if err == nil {
				if net.ParseIP(host) != nil {
					logMsg("New covert session %s targeting RAW IP %s:%s (Warning: Local DNS Leak?)", cfg.LogFn, sessionID, host, port)
				} else {
					logMsg("New covert session %s targeting SECURE DOMAIN %s:%s", cfg.LogFn, sessionID, host, port)
				}
			} else {
				logMsg("New covert session %s targeting %s", cfg.LogFn, sessionID, addr)
			}

			select {
			case connLimit <- struct{}{}:
			default:
				return nil, ErrTooManyConns
			}
			released := false
			defer func() {
				if !released {
					<-connLimit
				}
			}()

			session := NewSession(sessionID)
			session.TargetAddr = addr
			if cfg.MultiBackend != nil {
				session.BackendIdx = uint8(storage.RandBackendIndex(cfg.MultiBackend.NumBackends()))
				logMsg("Session %s assigned to backend %d", cfg.LogFn, sessionID, session.BackendIdx)
			}
			cfg.Engine.AddSession(session)

			session.EnqueueTx(nil)

			released = true
			return NewVirtualConnWithOnClose(session, cfg.Engine, func() { <-connLimit }), nil
		}),
		socks5.WithAssociateHandle(func(_ context.Context, w io.Writer, _ *socks5.Request) error {
			_ = socks5.SendReply(w, statute.RepCommandNotSupported, nil)
			return fmt.Errorf("UDP not supported")
		}),
		socks5.WithResolver(rawResolver{}),
	}

	if cfg.User != "" && cfg.Pass != "" {
		creds := socks5.StaticCredentials{cfg.User: cfg.Pass}
		opts = append(opts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.UserPassAuthenticator{Credentials: creds},
		}))
		logMsg("SOCKS5 authentication enabled for user: %s", cfg.LogFn, cfg.User)
	} else {
		opts = append(opts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.NoAuthAuthenticator{},
		}))
		logMsg("SOCKS5 running without authentication", cfg.LogFn)
	}

	return opts, nil
}

func logMsg(msg string, logFn func(string, ...any), args ...any) {
	if logFn != nil {
		logFn(msg, args...)
	} else {
		logger.Info(msg, args...)
	}
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type rawResolver struct{}

func (rawResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}
