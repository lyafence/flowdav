package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/lyafence/flowdav/internal/config"

	"github.com/lyafence/flowdav/internal/logger"

	"github.com/lyafence/flowdav/internal/storage"

	"github.com/lyafence/flowdav/internal/transport"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type rawResolver struct{}

func (rawResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	// Defends comprehensively against Local DNS leaks by doing absolutely nothing.
	return ctx, nil, nil
}

var version = "dev"

func main() {
	password, askInteractive, cleanArgs := config.ResolvePassword(os.Args[1:])
	os.Args = append([]string{os.Args[0]}, cleanArgs...)

	var configPath string
	flag.StringVar(&configPath, "c", "config.json", "Path to config file")
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Show version")
	var logLevel string
	flag.StringVar(&logLevel, "l", "", "Log level (debug|info|warn|error)")
	flag.Parse()

	if showVersion {
		fmt.Println("flowdav-client", version)
		os.Exit(0)
	}

	logger.Info("Starting flowdav Client...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appCfg, err := config.LoadConfig(configPath, password, askInteractive)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	logger.SetLevel(appCfg.LogLevel)
	if logLevel != "" {
		logger.SetLevel(logLevel)
	}

	backend, multiBackend, err := storage.NewBackendFromConfig(appCfg.WebDAV)
	if err != nil {
		log.Fatalf("Failed to init WebDAV storage: %v", err)
	}
	if err := backend.Login(ctx); err != nil {
		log.Fatalf("Backend login failed: %v", err)
	}

	cid := generateSessionID()[:8]
	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	if appCfg.MaxMessageSize > 0 {
		transport.MaxMessageSize = appCfg.MaxMessageSize
		storage.MaxFileSize = appCfg.MaxMessageSize
	}

	engine := transport.NewEngine(backend, true, cid, cryptoCfg)
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
	engine.Start(ctx)

	// Log session endings
	engine.OnSessionEnd = func(sessionID string) {
		logger.Info("Client session %s ended", sessionID)
	}

	if appCfg.HealthPort != "" {
		mux := http.ServeMux{}
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(engine.Stats())
		})
		ln, err := net.Listen("tcp", appCfg.HealthPort)
		if err != nil {
			logger.Info("Health server failed to listen on %s: %v", appCfg.HealthPort, err)
		} else {
			logger.Info("Health server listening on %s", appCfg.HealthPort)
			defer ln.Close()
			go func() {
				if err := http.Serve(ln, &mux); err != nil && !errors.Is(err, net.ErrClosed) {
					logger.Info("Health server error: %v", err)
				}
			}()
		}
	}

	listenAddr := appCfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:1080"
	}

	// Limit concurrent proxy connections to prevent resource exhaustion
	// Use config value or default to 100
	maxConns := appCfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	connLimit := make(chan struct{}, maxConns)

	// Create the library SOCKS5 server wrapping our custom WebDAV Engine tunnel
	// Build server options
	serverOpts := []socks5.Option{
		socks5.WithAuthMethods([]socks5.Authenticator{}), // Initialize with empty slice
		socks5.WithDial(func(dc context.Context, network, addr string) (net.Conn, error) {
			sessionID := generateSessionID()

			// Intelligently parse the address string to warn users if their browser is natively leaking DNS
			host, port, err := net.SplitHostPort(addr)
			if err == nil {
				if net.ParseIP(host) != nil {
					logger.Info("New covert session %s targeting RAW IP %s:%s (Warning: Local DNS Leak?)", sessionID, host, port)
				} else {
					logger.Info("New covert session %s targeting SECURE DOMAIN %s:%s", sessionID, host, port)
				}
			} else {
				logger.Info("New covert session %s targeting %s", sessionID, addr)
			}

			// Acquire connection slot (blocks if at limit)
			select {
			case connLimit <- struct{}{}:
			default:
				return nil, transport.ErrTooManyConns
			}

			session := transport.NewSession(sessionID)
			session.TargetAddr = addr
			if multiBackend != nil {
				session.BackendIdx = uint8(storage.RandBackendIndex(multiBackend.NumBackends()))
				logger.Info("Session %s assigned to backend %d", sessionID, session.BackendIdx)
			}
			engine.AddSession(session)

			// Instantly ping a blank payload so the remote end opens the actual TCP destination
			session.EnqueueTx(nil)

			return transport.NewVirtualConnWithOnClose(session, engine, func() { <-connLimit }), nil
		}),
		socks5.WithAssociateHandle(func(ctx context.Context, w io.Writer, req *socks5.Request) error {
			// Explicitly block UDP routing to confidently prevent ISP endpoint leakage
			socks5.SendReply(w, statute.RepCommandNotSupported, nil)
			return fmt.Errorf("covert UDP not supported")
		}),
		// DEFEND AGAINST LOCAL DNS LEAKS:
		// The library natively performs system DNS lookups for all FQDNs before proxying!
		// We explicitly override the resolver with a NoOp dummy to force raw strings into the pipe.
		socks5.WithResolver(rawResolver{}),
	}

	// Add SOCKS5 authentication if configured
	if appCfg.SOCKS5User != "" && appCfg.SOCKS5Pass != "" {
		credentials := socks5.StaticCredentials{appCfg.SOCKS5User: appCfg.SOCKS5Pass}
		serverOpts = append(serverOpts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.UserPassAuthenticator{Credentials: credentials},
		}))
		logger.Info("SOCKS5 authentication enabled for user: %s", appCfg.SOCKS5User)
	} else {
		serverOpts = append(serverOpts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.NoAuthAuthenticator{},
		}))
		logger.Info("WARNING: SOCKS5 server running WITHOUT authentication. Anyone with network access can use this proxy!")
	}

	server := socks5.NewServer(serverOpts...)

	logger.Info("Listening for SOCKS5 on %s...", listenAddr)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}

	// Channel to signal server failure so main can exit cleanly
	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logger.Error("SOCKS5 server failed: %v", err)
			}
			serverErrCh <- err
		}
	}()

	// Wait for either termination signal or server failure
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
		logger.Info("Received termination signal")
	case err := <-serverErrCh:
		logger.Error("SOCKS5 server stopped: %v", err)
	}
	logger.Info("Shutting down...")
	listener.Close()

	// Graceful shutdown: stop engine and wait for goroutines
	engine.Stop()
	cancel()
}
