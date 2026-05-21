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
	"sync"
	"syscall"
	"time"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
	"golang.org/x/term"

	"github.com/lyafence/flowdav/internal/config"
	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
	"github.com/lyafence/flowdav/internal/transport"
)

var version = "dev"

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type rawResolver struct{}

func (rawResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func main() {
	password, askInteractive, cleanArgs := config.ResolvePassword(os.Args[1:])
	os.Args = append([]string{os.Args[0]}, cleanArgs...)

	var clientPath, serverPath string
	var genMode, encryptMode bool
	var showVersion bool
	var logLevel string

	flag.StringVar(&clientPath, "c", "", "Run as client (config path)")
	flag.StringVar(&clientPath, "client", "", "")
	flag.StringVar(&serverPath, "s", "", "Run as server (config path)")
	flag.StringVar(&serverPath, "server", "", "")
	flag.BoolVar(&genMode, "g", false, "Generate config interactively")
	flag.BoolVar(&genMode, "gen", false, "")
	flag.BoolVar(&encryptMode, "e", false, "Encrypt config file")
	flag.BoolVar(&encryptMode, "encrypt", false, "")
	flag.BoolVar(&showVersion, "v", false, "Show version")
	flag.BoolVar(&showVersion, "version", false, "")
	flag.StringVar(&logLevel, "l", "", "Log level (debug|info|warn|error)")
	flag.StringVar(&logLevel, "log", "", "")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: flowdav [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Modes (mutually exclusive):\n")
		fmt.Fprintf(os.Stderr, "  -c, --client <path>     Run as SOCKS5 client\n")
		fmt.Fprintf(os.Stderr, "  -s, --server <path>     Run as WebDAV server\n")
		fmt.Fprintf(os.Stderr, "  -e, --encrypt <path>    Encrypt config file\n")
		fmt.Fprintf(os.Stderr, "  -g, --gen   <path>      Generate config interactively\n")
		fmt.Fprintf(os.Stderr, "  -g -e <path>            Generate + encrypt\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  -p, --password [value]  Master password (omit for prompt)\n")
		fmt.Fprintf(os.Stderr, "  -l, --log <level>       Log level (debug|info|warn|error)\n")
		fmt.Fprintf(os.Stderr, "  -v, --version           Show version\n")
	}
	flag.Parse()

	if showVersion {
		fmt.Println("flowdav", version)
		return
	}

	roles := 0
	if clientPath != "" {
		roles++
	}
	if serverPath != "" {
		roles++
	}
	if encryptMode {
		roles++
	}
	if genMode {
		roles++
	}
	if encryptMode && genMode {
		roles-- // -g -e counts as one combined mode
	}
	if roles != 1 {
		flag.Usage()
		os.Exit(1)
	}

	if genMode && encryptMode {
		path := flag.Arg(0)
		if path == "" {
			fmt.Fprintln(os.Stderr, "error: -g -e requires a config path")
			os.Exit(1)
		}
		runGenAndEncrypt(path, password)
		return
	}

	switch {
	case clientPath != "":
		runClient(clientPath, password, logLevel, askInteractive)
	case serverPath != "":
		runServer(serverPath, password, logLevel, askInteractive)
	case encryptMode:
		path := flag.Arg(0)
		if path == "" {
			fmt.Fprintln(os.Stderr, "error: -e requires a config path")
			os.Exit(1)
		}
		runEncrypt(path, password)
	case genMode:
		path := flag.Arg(0)
		if path == "" {
			fmt.Fprintln(os.Stderr, "error: -g requires a config path")
			os.Exit(1)
		}
		runGen(path)
	}
}

func runClient(configPath, password, logLevel string, askInteractive bool) {
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

	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	if appCfg.MaxMessageSize > 0 {
		transport.MaxMessageSize = appCfg.MaxMessageSize
		storage.MaxFileSize = appCfg.MaxMessageSize
	}

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
				srv := &http.Server{
					Handler:      &mux,
					ReadTimeout:  5 * time.Second,
					WriteTimeout: 5 * time.Second,
				}
				if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
					logger.Info("Health server error: %v", err)
				}
			}()
		}
	}

	listenAddr := appCfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:1080"
	}

	maxConns := appCfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	connLimit := make(chan struct{}, maxConns)

	serverOpts := []socks5.Option{
		socks5.WithDial(func(dc context.Context, network, addr string) (net.Conn, error) {
			sessionID := generateSessionID()

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
			return fmt.Errorf("covert UDP not supported")
		}),
		socks5.WithResolver(rawResolver{}),
	}

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

	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logger.Error("SOCKS5 server failed: %v", err)
			}
			serverErrCh <- err
		}
	}()

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

	engine.Stop()
	cancel()
}

func runServer(configPath, password, logLevel string, askInteractive bool) {
	logger.Info("Starting flowdav Server...")
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

	backend, _, err := storage.NewBackendFromConfig(appCfg.WebDAV)
	if err != nil {
		log.Fatalf("Failed to init WebDAV storage: %v", err)
	}

	if appCfg.MaxMessageSize > 0 {
		transport.MaxMessageSize = appCfg.MaxMessageSize
		storage.MaxFileSize = appCfg.MaxMessageSize
	}

	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	engine := transport.NewEngine(backend, false, cryptoCfg)
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

	engine.OnNewSession = func(sessionID, targetAddr string, session *transport.Session) {
		logger.Info("Server received new session %s destined for %s", sessionID, targetAddr)
		go handleServerConn(sessionID, targetAddr, session, engine)
	}

	engine.Start(ctx)
	logger.Info("Engine started, waiting for signal...")

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
				srv := &http.Server{
					Handler:      &mux,
					ReadTimeout:  5 * time.Second,
					WriteTimeout: 5 * time.Second,
				}
				if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
					logger.Info("Health server error: %v", err)
				}
			}()
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down server...")

	engine.Stop()
	cancel()
}

func handleServerConn(sessionID, targetAddr string, session *transport.Session, engine *transport.Engine) {
	defer engine.RemoveSession(sessionID)

	tcpAddr, err := net.ResolveTCPAddr("tcp", targetAddr)
	if err != nil {
		logger.Info("Resolve error to %s: %v", targetAddr, err)
		return
	}
	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		logger.Info("Dial error to %s: %v", targetAddr, err)
		return
	}
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)
	defer conn.Close()

	done := make(chan struct{})
	closeOnce := sync.Once{}

	closeConn := func() {
		closeOnce.Do(func() {
			close(done)
			conn.Close()
		})
	}

	errCh := make(chan error, 2)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Info("Session %s: panic in target reader: %v", sessionID, r)
			}
		}()
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
			}

			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := conn.Read(buf)
			if n > 0 {
				logger.Info("Session %s: read %d bytes from target", sessionID, n)
				session.EnqueueTx(buf[:n])
			}
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if !errors.Is(err, net.ErrClosed) {
					logger.Info("Session %s: target read error: %v", sessionID, err)
				}
				errCh <- err
				closeConn()
				return
			}
		}
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Info("Session %s: panic in target writer: %v", sessionID, r)
			}
		}()
		for {
			select {
			case <-done:
				return
			case data, ok := <-session.RxChan:
				if !ok {
					errCh <- fmt.Errorf("session closed by remote")
					closeConn()
					return
				}
				if len(data) > 0 {
					logger.Info("Session %s: got %d bytes from client Rx, writing to target", sessionID, len(data))
					if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
						logger.Info("Session %s: set write deadline error: %v", sessionID, err)
					}
					if _, err := conn.Write(data); err != nil {
						logger.Info("Session %s: target write error: %v", sessionID, err)
						errCh <- err
						closeConn()
						return
					}
				}
			}
		}
	}()

	connErr := <-errCh
	logger.Info("Session %s: connection ended: %v", sessionID, connErr)
	closeConn()
}

func runEncrypt(configPath, password string) {
	if password == "" {
		fmt.Print("Master password: ")
		pass, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		password = string(pass)
	}

	plaintext, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(plaintext, &cfg); err != nil {
		log.Fatalf("invalid JSON config: %v", err)
	}
	if _, hasKey := cfg["enc_key"]; !hasKey {
		log.Fatal("config missing enc_key — use -g to generate a config first")
	}
	if _, hasKey := cfg["hmac_key"]; !hasKey {
		log.Fatal("config missing hmac_key — use -g to generate a config first")
	}

	encrypted, err := config.EncryptConfig(plaintext, password)
	if err != nil {
		log.Fatalf("encryption failed: %v", err)
	}

	encPath := configPath + ".enc"
	data := config.MarshalEncrypted(encrypted)
	if err := os.WriteFile(encPath, data, 0600); err != nil { //nolint:gosec
		log.Fatalf("failed to write %s: %v", encPath, err)
	}
	fmt.Printf("Encrypted: %s\n", encPath)
}

func runGen(configPath string) {
	data := generateConfig()
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		log.Fatalf("failed to write config: %v", err)
	}
	fmt.Printf("Generated: %s\n", configPath)
}

func runGenAndEncrypt(configPath, password string) {
	if password == "" {
		fmt.Print("Master password: ")
		pass, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			log.Fatalf("failed to read password: %v", err)
		}
		password = string(pass)
	}

	data := generateConfig()

	encrypted, err := config.EncryptConfig(data, password)
	if err != nil {
		log.Fatalf("encryption failed: %v", err)
	}

	encPath := configPath + ".enc"
	out := config.MarshalEncrypted(encrypted)
	if err := os.WriteFile(encPath, out, 0600); err != nil { //nolint:gosec
		log.Fatalf("failed to write %s: %v", encPath, err)
	}
	fmt.Printf("Generated and encrypted: %s\n", encPath)
}
