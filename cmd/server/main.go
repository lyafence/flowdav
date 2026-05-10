package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lyafence/flowdav/internal/config"

	"github.com/lyafence/flowdav/internal/logger"

	"github.com/lyafence/flowdav/internal/storage"

	"github.com/lyafence/flowdav/internal/transport"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "c", "config.json", "Path to config file")
	var logLevel string
	flag.StringVar(&logLevel, "l", "", "Log level (debug|info|warn|error)")
	flag.Parse()

	logger.Info("Starting flowdav Server...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appCfg, err := config.Load(configPath)
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
	if err := backend.Login(ctx); err != nil {
		log.Fatalf("Backend login failed: %v", err)
	}

	cryptoCfg := &transport.CryptoConfig{
		EncKey:  appCfg.EncKeyDecoded,
		HMacKey: appCfg.HMacKeyDecoded,
	}
	engine := transport.NewEngine(backend, false, "", cryptoCfg)
	if appCfg.RefreshRateMs > 0 {
		engine.SetPollRate(appCfg.RefreshRateMs)
	}
	if appCfg.FlushRateMs > 0 {
		engine.SetFlushRate(appCfg.FlushRateMs)
	}

	// Called by polling loop when a new incoming session file is found
	engine.OnNewSession = func(sessionID, targetAddr string, session *transport.Session) {
		logger.Info("Server received new session %s destined for %s", sessionID, targetAddr)
		go handleServerConn(sessionID, targetAddr, session, engine)
	}

	engine.Start(ctx)
	logger.Info("Engine started, waiting for signal...")

	if appCfg.HealthPort != "" {
		go func() {
			mux := http.ServeMux{}
			mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(engine.Stats())
			})
			ln, err := net.Listen("tcp", appCfg.HealthPort)
			if err != nil {
				logger.Info("Health server failed to listen on %s: %v", appCfg.HealthPort, err)
				return
			}
			logger.Info("Health server listening on %s", appCfg.HealthPort)
			if err := http.Serve(ln, &mux); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Info("Health server error: %v", err)
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down server...")

	// Graceful shutdown: stop engine and wait for goroutines
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

	// Use a done channel to signal both goroutines to stop
	done := make(chan struct{})
	closeOnce := sync.Once{}

	// Close connection only once when both goroutines are done
	closeConn := func() {
		closeOnce.Do(func() {
			close(done)
			conn.Close()
		})
	}

	errCh := make(chan error, 2)

	// Conn -> Tx (Res)
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
				// Check if we're done - if so, this is expected
				select {
				case <-done:
					return
				default:
				}
				// Ignore timeout errors — just loop to check done channel
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// Don't log error if connection was closed intentionally
				if !errors.Is(err, net.ErrClosed) {
					logger.Info("Session %s: target read error: %v", sessionID, err)
				}
				errCh <- err
				closeConn()
				return
			}
		}
	}()

	// Rx (Req) -> Conn
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
			default:
			}
			data, ok := <-session.RxChan
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
	}()

	connErr := <-errCh
	logger.Info("Session %s: connection ended: %v", sessionID, connErr)
	closeConn()
}
