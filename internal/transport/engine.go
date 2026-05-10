package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
)

// Engine manages the local sessions, periodically flushes Tx buffers to files,
// and polls for new Rx files.
type Engine struct {
	backend storage.Backend
	myDir   Direction // DirReq for client, DirRes for server
	peerDir Direction // DirRes for client, DirReq for server
	id      string    // ClientID for client, empty for server

	sessions  map[string]*Session
	sessionMu sync.RWMutex

	// Tombstones for recently closed sessions to prevent re-triggering on delayed packets
	closedSessions   map[string]time.Time
	closedSessionsMu sync.Mutex

	pollTicker  time.Duration
	flushTicker time.Duration

	// Server mode handler: called when a new session is discovered
	OnNewSession func(sessionID, targetAddr string, s *Session)

	// OnSessionEnd is called when a session ends (client-side logging)
	OnSessionEnd func(sessionID string)

	// Concurrency control for storage operations (Upload/Download)
	sem chan struct{}

	// Concurrency control for download goroutines in pollLoop
	downloadSem chan struct{}

	// Track processed files with timestamps to avoid duplicates and enable TTL cleanup
	processed   map[string]time.Time
	processedMu sync.Mutex

	// Crypto configuration (nil = no encryption)
	cryptoCfg *CryptoConfig

	// Track in-flight upload filenames to prevent cleanupLoop from deleting files mid-upload
	inFlight sync.Map

	// Graceful shutdown support
	stopCh  chan struct{}
	wg      sync.WaitGroup
	wgMu    sync.Mutex
	stopped bool

	// MaxSessions limits the total number of concurrent sessions (0 = unlimited)
	MaxSessions int
}

func NewEngine(backend storage.Backend, isClient bool, clientID string, cryptoCfg *CryptoConfig) *Engine {
	e := &Engine{
		backend:        backend,
		id:             clientID,
		sessions:       make(map[string]*Session),
		closedSessions: make(map[string]time.Time),
		processed:      make(map[string]time.Time),
		cryptoCfg:      cryptoCfg,
		stopCh:         make(chan struct{}),
		// Default intervals: Poll (RX) and Flush (TX) - safe for cloud rate limits (~1 req/s)
		pollTicker:  500 * time.Millisecond,
		flushTicker: 500 * time.Millisecond,
	}
	if isClient {
		e.myDir = DirReq
		e.peerDir = DirRes
	} else {
		e.myDir = DirRes
		e.peerDir = DirReq
	}
	e.sem = make(chan struct{}, 8)
	e.downloadSem = make(chan struct{}, 16)
	return e
}

func (e *Engine) SetPollRate(ms int) {
	if ms > 0 {
		e.pollTicker = time.Duration(ms) * time.Millisecond
	}
}

func (e *Engine) SetFlushRate(ms int) {
	if ms > 0 {
		e.flushTicker = time.Duration(ms) * time.Millisecond
	}
}

func (e *Engine) SetMaxSessions(max int) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	e.MaxSessions = max
}

func (e *Engine) Start(ctx context.Context) {
	e.wg.Add(3)
	go func() {
		defer e.wg.Done()
		e.flushLoop(ctx)
	}()
	go func() {
		defer e.wg.Done()
		e.pollLoop(ctx)
	}()
	go func() {
		defer e.wg.Done()
		e.cleanupLoop(ctx)
	}()
}

// Stop gracefully shuts down all engine goroutines
func (e *Engine) Stop() {
	e.wgMu.Lock()
	if e.stopped {
		e.wgMu.Unlock()
		return
	}
	e.stopped = true
	close(e.stopCh)
	e.wgMu.Unlock()

	e.wg.Wait()
}

func (e *Engine) GetSession(id string) *Session {
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	return e.sessions[id]
}

func (e *Engine) AddSession(s *Session) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	e.sessions[s.ID] = s
	logger.Info("Engine.AddSession: Added session %s (Total now: %d)", s.ID, len(e.sessions))
}

func (e *Engine) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(e.flushTicker)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.flushAll(ctx)
		}
	}
}

type muxKey struct {
	CID        string
	BackendIdx uint8
}

func (e *Engine) flushAll(ctx context.Context) {
	e.sessionMu.Lock()
	sessions := make([]*Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		sessions = append(sessions, s)
	}
	e.sessionMu.Unlock()

	muxes := make(map[muxKey][]Envelope)
	var closedSessionIDs []string

	for _, s := range sessions {
		s.mu.Lock()

		if time.Since(s.lastActivity) > 10*time.Second {
			s.closed = true
		}

		shouldSend := len(s.txBuf) > 0 || (s.txSeq == 0 && e.myDir == DirReq) || s.closed

		if !shouldSend {
			s.mu.Unlock()
			continue
		}

		payload := s.txBuf
		s.txBuf = nil
		s.txCond.Broadcast()

		cid := s.ClientID
		if cid == "" && e.myDir == DirReq {
			cid = e.id
		}

		env := Envelope{
			SessionID:  s.ID,
			Seq:        s.txSeq,
			Payload:    payload,
			Close:      s.closed,
			TargetAddr: s.TargetAddr,
			BackendIdx: s.BackendIdx,
		}

		s.txSeq++
		if s.closed {
			closedSessionIDs = append(closedSessionIDs, s.ID)
		}

		key := muxKey{CID: cid, BackendIdx: s.BackendIdx}
		muxes[key] = append(muxes[key], env)
		s.mu.Unlock()
	}

	for key, mux := range muxes {
		fnameCID := key.CID
		if fnameCID == "" {
			fnameCID = "unknown"
		}
		filename := fmt.Sprintf("%s-%s-%d.bin", e.myDir, fnameCID, time.Now().UnixNano())
		backendIdx := key.BackendIdx

		e.inFlight.Store(filename, struct{}{})
		go func(fname string, m []Envelope, bIdx uint8) {
			defer e.inFlight.Delete(fname)
			defer func() {
				if r := recover(); r != nil {
					logger.Info("upload panic %s: %v", fname, r)
				}
			}()
			e.sem <- struct{}{}
			defer func() { <-e.sem }()

			var buf bytes.Buffer
			for _, env := range m {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := env.EncodeWithCrypto(&buf, e.cryptoCfg); err != nil {
					logger.Info("mux encode error: %v", err)
					return
				}
			}

			if err := e.backend.UploadByIndex(ctx, fname, &buf, bIdx); err != nil {
				logger.Info("upload error %s (backend %d): %v", fname, bIdx, err)
			}
		}(filename, mux, backendIdx)
	}

	for _, id := range closedSessionIDs {
		e.RemoveSession(id)
	}
}

func (e *Engine) pollLoop(ctx context.Context) {
	logger.Info("pollLoop: started, myDir=%s, peerDir=%s", e.myDir, e.peerDir)
	currentPollInterval := e.pollTicker
	maxPollInterval := 5 * time.Second
	timer := time.NewTimer(currentPollInterval)
	defer timer.Stop()
	// Reusable timer for the 100ms post-receive backoff to avoid time.After() leaks
	backoffTimer := time.NewTimer(100 * time.Millisecond)
	if !backoffTimer.Stop() {
		select {
		case <-backoffTimer.C:
		default:
		}
	}
	defer backoffTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-timer.C:
			if e.myDir == DirReq {
				e.sessionMu.RLock()
				count := len(e.sessions)
				e.sessionMu.RUnlock()
				if count == 0 {
					timer.Reset(currentPollInterval)
					continue
				}
			}

			prefix := string(e.peerDir) + "-"
			if e.myDir == DirReq {
				prefix += e.id + "-"
			} else {
				prefix += ""
			}
			files, err := e.backend.ListQuery(ctx, prefix)
			if err != nil {
				logger.Info("poll list error: %v", err)
				timer.Reset(currentPollInterval)
				continue
			}

			if len(files) == 0 {
				if e.myDir == DirRes {
					e.sessionMu.RLock()
					activeSessions := len(e.sessions)
					e.sessionMu.RUnlock()

					if activeSessions == 0 {
						currentPollInterval += 500 * time.Millisecond
						if currentPollInterval > maxPollInterval {
							currentPollInterval = maxPollInterval
						}
					} else {
						currentPollInterval = e.pollTicker
					}
				}
				timer.Reset(currentPollInterval)
				continue
			}

			currentPollInterval = e.pollTicker

			// Concurrency control for download goroutines in pollLoop
			var wg sync.WaitGroup
			for _, entry := range files {
				fname := strings.TrimSuffix(entry.Filename, ".bin")
				backendIdx := entry.BackendIdx
				parts := strings.Split(fname, "-")
				if len(parts) < 3 {
					continue
				}
				tsStr := parts[len(parts)-1]
				ts, err := strconv.ParseInt(tsStr, 10, 64)
				if err == nil && ts > 0 && time.Since(time.Unix(0, ts)) > 5*time.Minute {
					e.backend.Delete(ctx, entry.Filename)
					continue
				}
				fileClientID := strings.Join(parts[1:len(parts)-1], "-")

				e.processedMu.Lock()
				if ts, exists := e.processed[entry.Filename]; exists && time.Since(ts) < 5*time.Minute {
					e.processedMu.Unlock()
					continue
				}
				e.processed[entry.Filename] = time.Now()
				e.processedMu.Unlock()

				wg.Add(1)
				go func(fname, fileClientID string, backendIdx uint8) {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							logger.Info("download panic %s: %v", fname, r)
						}
					}()

					e.downloadSem <- struct{}{}
					defer func() { <-e.downloadSem }()

					e.sem <- struct{}{}
					defer func() { <-e.sem }()

					select {
					case <-ctx.Done():
						return
					case <-e.stopCh:
						return
					default:
					}

					rc, err := e.backend.DownloadByIndex(ctx, fname, backendIdx)
					if err != nil {
						if rc != nil {
							rc.Close()
						}
						logger.Info("download error %s (backend %d): %v", fname, backendIdx, err)
						return
					}
					defer rc.Close()

					for {
						var env Envelope
						if e.cryptoCfg != nil {
							decodedEnv, err := DecodeEnvelopeWithCrypto(rc, e.cryptoCfg)
							if err != nil {
								if err != io.EOF && err != io.ErrUnexpectedEOF {
									logger.Info("mux crypto decode error %s: %v", fname, err)
								}
								break
							}
							env = *decodedEnv
						} else {
							if err := env.Decode(rc); err != nil {
								if err != io.EOF && err != io.ErrUnexpectedEOF {
									logger.Info("mux decode error %s: %v", fname, err)
								}
								break
							}
						}

						e.closedSessionsMu.Lock()
						if _, exists := e.closedSessions[env.SessionID]; exists {
							e.closedSessionsMu.Unlock()
							continue
						}
						e.closedSessionsMu.Unlock()

						e.sessionMu.Lock()
						s, exists := e.sessions[env.SessionID]
						if !exists && e.myDir == DirRes && e.OnNewSession != nil {
							if e.MaxSessions > 0 && len(e.sessions) >= e.MaxSessions {
								e.sessionMu.Unlock()
								logger.Info("Engine: session limit reached (%d), dropping new session %s", e.MaxSessions, env.SessionID)
								continue
							}
							s = NewSession(env.SessionID)
							s.ClientID = fileClientID
							s.BackendIdx = env.BackendIdx
							e.sessions[env.SessionID] = s
							sessionID := env.SessionID
							targetAddr := env.TargetAddr
							clientID := fileClientID
							backendIdx := env.BackendIdx
							e.sessionMu.Unlock()
							logger.Info("Engine: Triggering new session %s for Client %s (backend %d)", sessionID, clientID, backendIdx)
							e.OnNewSession(sessionID, targetAddr, s)
						} else {
							e.sessionMu.Unlock()
						}

						if s != nil {
							envCopy := env
							s.ProcessRx(&envCopy)
						}
					}

					e.backend.Delete(ctx, fname)

					e.processedMu.Lock()
					delete(e.processed, fname)
					e.processedMu.Unlock()
				}(entry.Filename, fileClientID, backendIdx)
			}

			wg.Wait()

			if !backoffTimer.Stop() {
				select {
				case <-backoffTimer.C:
				default:
				}
			}
			backoffTimer.Reset(100 * time.Millisecond)
			pollAgain := true
			for pollAgain {
				select {
				case <-backoffTimer.C:
					pollAgain = false
				case <-e.stopCh:
					return
				case <-ctx.Done():
					return
				}
			}

			timer.Reset(currentPollInterval)
			continue
		}
	}
}

func (e *Engine) RemoveSession(id string) {
	e.sessionMu.Lock()
	delete(e.sessions, id)
	e.sessionMu.Unlock()

	logger.Info("Engine: Session %s removed (Total now: %d)", id, len(e.sessions))

	// Add to tombstone list
	e.closedSessionsMu.Lock()
	e.closedSessions[id] = time.Now()
	e.closedSessionsMu.Unlock()

	// Notify listener if set
	if e.OnSessionEnd != nil {
		e.OnSessionEnd(id)
	}
}

func (e *Engine) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			// Cleanup old tombstones (older than 30s)
			e.closedSessionsMu.Lock()
			for id, t := range e.closedSessions {
				if time.Since(t) > 30*time.Second {
					delete(e.closedSessions, id)
				}
			}
			e.closedSessionsMu.Unlock()

			// Periodically clear old processed entries by TTL (5 min)
			e.processedMu.Lock()
			for key, ts := range e.processed {
				if time.Since(ts) > 5*time.Minute {
					delete(e.processed, key)
				}
			}
			e.processedMu.Unlock()

			// ZERO-TRAFFIC CLIENT OPTIMIZATION:
			if e.myDir == DirReq {
				e.sessionMu.RLock()
				count := len(e.sessions)
				e.sessionMu.RUnlock()
				if count == 0 {
					continue
				}
			}

			files, err := e.backend.ListQuery(ctx, string(e.myDir)+"-")
			if err != nil {
				logger.Info("cleanupLoop ListQuery error: %v", err)
				continue
			}
			for _, entry := range files {
				// Пропускаем файлы, которые ещё загружаются
				if _, inflight := e.inFlight.Load(entry.Filename); inflight {
					continue
				}
				fname := strings.TrimSuffix(entry.Filename, ".bin")
				parts := strings.Split(fname, "-")
				if len(parts) < 3 {
					continue
				}
				tsStr := parts[len(parts)-1]
				ts, err := strconv.ParseInt(tsStr, 10, 64)
				if err == nil {
					t := time.Unix(0, ts)
					if time.Since(t) > 10*time.Second {
						e.backend.Delete(ctx, entry.Filename)
					}
				}
			}
		}
	}
}

type EngineStats struct {
	ActiveSessions int    `json:"active_sessions"`
	ProcessedFiles int    `json:"processed_files"`
	ClosedSessions int    `json:"closed_sessions"`
	PollTickerMs   int    `json:"poll_ticker_ms"`
	FlushTickerMs  int    `json:"flush_ticker_ms"`
	Role           string `json:"role"`
}

func (e *Engine) Stats() EngineStats {
	e.sessionMu.RLock()
	sessions := len(e.sessions)
	e.sessionMu.RUnlock()

	e.processedMu.Lock()
	processed := len(e.processed)
	e.processedMu.Unlock()

	e.closedSessionsMu.Lock()
	closed := len(e.closedSessions)
	e.closedSessionsMu.Unlock()

	role := "client"
	if e.myDir == DirRes {
		role = "server"
	}

	return EngineStats{
		ActiveSessions: sessions,
		ProcessedFiles: processed,
		ClosedSessions: closed,
		PollTickerMs:   int(e.pollTicker.Milliseconds()),
		FlushTickerMs:  int(e.flushTicker.Milliseconds()),
		Role:           role,
	}
}
