package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

	sessions  map[string]*Session
	sessionMu sync.RWMutex

	// Tombstones for recently closed sessions to prevent re-triggering on delayed packets
	closedSessions   map[string]time.Time
	closedSessionsMu sync.Mutex

	pollTicker      time.Duration
	minPollInterval time.Duration
	maxPollInterval time.Duration
	flushTicker     time.Duration

	// Server mode handler: called when a new session is discovered
	OnNewSession func(sessionID, targetAddr string, s *Session)

	// OnSessionEnd is called when a session ends (client-side logging)
	OnSessionEnd func(sessionID string)

	// Concurrency control for storage operations (Upload/Download)
	sem chan struct{}

	// Track processed files with timestamps to avoid duplicates and enable TTL cleanup
	processed   map[string]time.Time
	processedMu sync.Mutex

	// Crypto configuration (nil = no encryption)
	cryptoCfg *CryptoConfig

	// Track in-flight upload filenames to prevent cleanupLoop from deleting files mid-upload
	inFlight sync.Map

	// Graceful shutdown support
	stopCh   chan struct{}
	wg       sync.WaitGroup
	wgMu     sync.Mutex
	stopped  bool

	// MaxSessions limits the total number of concurrent sessions (0 = unlimited)
	MaxSessions int

	// downloadPool limits goroutines in pollLoop
	downloadPool *DownloadWorkerPool

	// uploadJobs feeds a fixed pool of upload workers
	uploadJobs chan uploadJob
}

type uploadJob struct {
	filename   string
	buf        bytes.Buffer
	backendIdx uint8
}

func NewEngine(backend storage.Backend, isClient bool, cryptoCfg *CryptoConfig) *Engine {
	e := &Engine{
		backend:        backend,
		sessions:       make(map[string]*Session),
		closedSessions: make(map[string]time.Time),
		processed:      make(map[string]time.Time),
		cryptoCfg:      cryptoCfg,
		stopCh:         make(chan struct{}),
		// Default intervals: Poll (RX) and Flush (TX) - safe for cloud rate limits (~1 req/s)
		pollTicker:      500 * time.Millisecond,
		minPollInterval: 100 * time.Millisecond,
		maxPollInterval: 5 * time.Second,
		flushTicker:     500 * time.Millisecond,
	}
	if isClient {
		e.myDir = DirReq
		e.peerDir = DirRes
	} else {
		e.myDir = DirRes
		e.peerDir = DirReq
	}
	e.sem = make(chan struct{}, 8)
	e.downloadPool = NewDownloadWorkerPool(e, 16)
	e.uploadJobs = make(chan uploadJob, 16)
	return e
}

func (e *Engine) SetPollRate(ms int) {
	if ms > 0 {
		e.pollTicker = time.Duration(ms) * time.Millisecond
	}
}

func (e *Engine) SetMinPollRate(ms int) {
	if ms > 0 {
		e.minPollInterval = time.Duration(ms) * time.Millisecond
	}
}

func (e *Engine) SetMaxPollRate(ms int) {
	if ms > 0 {
		e.maxPollInterval = time.Duration(ms) * time.Millisecond
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
	numWorkers := cap(e.sem)
	e.wg.Add(3 + numWorkers)
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
		e.gcLoop(ctx)
	}()
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer e.wg.Done()
			e.uploadWorker(ctx)
		}()
	}
	e.downloadPool.Start(ctx, e.stopCh)
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
	e.downloadPool.Stop()
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
	var sessionsToWake []*Session

	for _, s := range sessions {
		payload, seq, closed, ok := s.ExtractTxBatch(e.myDir == DirReq)
		if !ok {
			continue
		}
		sessionsToWake = append(sessionsToWake, s)

		s.mu.Lock()
		bidx := s.BackendIdx
		s.mu.Unlock()

		env := Envelope{
			SessionID:  s.ID,
			Seq:        seq,
			Payload:    payload,
			Close:      closed,
			TargetAddr: s.TargetAddr,
			BackendIdx: bidx,
		}

		if closed {
			closedSessionIDs = append(closedSessionIDs, s.ID)
		}

		key := muxKey{BackendIdx: bidx}
		muxes[key] = append(muxes[key], env)
	}

	for _, s := range sessionsToWake {
		s.wakeupTx()
	}

	for key, mux := range muxes {
		// Split muxed envelopes into chunks that fit within the upload limit
		// to prevent silent truncation by WebDAV upload limits
		remaining := mux
		for len(remaining) > 0 {
			var buf bytes.Buffer
			var consumed int
			for _, env := range remaining {
				select {
				case <-ctx.Done():
					return
				default:
				}
				before := buf.Len()
				if err := env.EncodeWithCrypto(&buf, e.cryptoCfg); err != nil {
					logger.Info("mux encode error: %v", err)
					return
				}
				if buf.Len() > safeUploadSize() && consumed > 0 {
					buf.Truncate(before)
					break
				}
				consumed++
			}

			filename := randomFilename(uploadPrefix(e.myDir))

			e.inFlight.Store(filename, struct{}{})
			select {
			case e.uploadJobs <- uploadJob{filename: filename, buf: buf, backendIdx: key.BackendIdx}:
			case <-ctx.Done():
				e.inFlight.Delete(filename)
				return
			}
			remaining = remaining[consumed:]
		}
	}

	for _, id := range closedSessionIDs {
		e.RemoveSession(id)
	}
}

// safeUploadSize returns the effective upload limit, leaving ~12.5% headroom
// for envelope and crypto overhead (binary headers, nonces, GCM tags, HMAC).
// Scales with MaxMessageSize so user-configured limits are respected.
func safeUploadSize() int {
	s := MaxMessageSize * 7 / 8
	if s < 65536 {
		return 65536
	}
	return s
}

func (e *Engine) pollLoop(ctx context.Context) {
	logger.Info("pollLoop: started, peerDir=%s", pollPrefix(e.myDir))
	currentPollInterval := e.pollTicker
	timer := time.NewTimer(currentPollInterval)
	defer timer.Stop()
	backoffTimer := time.NewTimer(100 * time.Millisecond)
	if !backoffTimer.Stop() {
		select {
		case <-backoffTimer.C:
		default:
		}
	}
	defer backoffTimer.Stop()

	// peerPrefix is the direction byte we expect from the other side
	peerPrefix := pollPrefix(e.myDir)

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
					timer.Reset(jitterPollInterval(currentPollInterval))
					continue
				}
			}

			files, err := e.backend.ListQuery(ctx, peerPrefix)
			if err != nil {
				logger.Info("poll list error: %v", err)
				timer.Reset(jitterPollInterval(currentPollInterval))
				continue
			}

			if len(files) == 0 {
				// Exponential backoff: double interval, cap at max
				currentPollInterval *= 2
				if currentPollInterval > e.maxPollInterval {
					currentPollInterval = e.maxPollInterval
				}
				timer.Reset(jitterPollInterval(currentPollInterval))
				continue
			}

			// Reset to base interval on data received
			currentPollInterval = e.minPollInterval

			for _, entry := range files {
				// GC: delete files older than 5 minutes
				if time.Since(entry.ModTime) > 5*time.Minute {
					e.backend.Delete(ctx, entry.Filename)
					continue
				}

			e.processedMu.Lock()
			if ts, exists := e.processed[entry.Filename]; exists && time.Since(ts) < 5*time.Minute {
				e.processedMu.Unlock()
				continue
			}
			e.processedMu.Unlock()

			if e.downloadPool.Submit(downloadJob{
				filename:   entry.Filename,
				backendIdx: entry.BackendIdx,
			}, e.stopCh) {
				e.processedMu.Lock()
				e.processed[entry.Filename] = time.Now()
				e.processedMu.Unlock()
			}
			}

			// Burst backoff: fast re-poll after receiving files
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

			timer.Reset(jitterPollInterval(currentPollInterval))
			continue
		}
	}
}

// jitterPollInterval applies ±25% random jitter to the given duration
// to reduce traffic fingerprinting via polling patterns.
func jitterPollInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return d
	}
	// 0.75–1.25 range
	factor := 0.75 + float64(b[0])/255.0*0.5
	return time.Duration(float64(d) * factor)
}

// randomFilename generates an obfuscated filename with a direction prefix
// to avoid leaking client ID, timing, or session metadata via filenames.
func randomFilename(dirByte string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	return fmt.Sprintf("%s%s", dirByte, hex.EncodeToString(b[:]))
}

func (e *Engine) RemoveSession(id string) {
	e.sessionMu.Lock()
	delete(e.sessions, id)
	count := len(e.sessions)
	e.sessionMu.Unlock()

	logger.Info("Engine: Session %s removed (Total now: %d)", id, count)

	// Add to tombstone list
	e.closedSessionsMu.Lock()
	e.closedSessions[id] = time.Now()
	e.closedSessionsMu.Unlock()

	// Notify listener if set
	if e.OnSessionEnd != nil {
		e.OnSessionEnd(id)
	}
}

func (e *Engine) gcLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.closedSessionsMu.Lock()
			for id, t := range e.closedSessions {
				if time.Since(t) > 30*time.Second {
					delete(e.closedSessions, id)
				}
			}
			e.closedSessionsMu.Unlock()

			e.processedMu.Lock()
			for key, ts := range e.processed {
				if time.Since(ts) > 5*time.Minute {
					delete(e.processed, key)
				}
			}
			e.processedMu.Unlock()
		}
	}
}

// uploadWorker is a fixed goroutine that processes upload jobs from flushAll.
func (e *Engine) uploadWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case job, ok := <-e.uploadJobs:
			if !ok {
				return
			}
			e.inFlight.Store(job.filename, struct{}{})
			func() {
				defer e.inFlight.Delete(job.filename)
				defer func() {
					if r := recover(); r != nil {
						logger.Info("upload panic %s: %v", job.filename, r)
					}
				}()
				if err := retryStorage(ctx, e.stopCh, "upload "+job.filename, func() error {
					return e.backend.UploadByIndex(ctx, job.filename, &job.buf, job.backendIdx)
				}); err != nil {
					logger.Info("upload error %s (backend %d): %v", job.filename, job.backendIdx, err)
				}
			}()
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

// uploadPrefix returns the one-byte filename prefix for files uploaded by this side.
// Client uploads "r" (request), server uploads "s" (response).
func uploadPrefix(myDir Direction) string {
	if myDir == DirReq {
		return "r"
	}
	return "s"
}

// pollPrefix returns the one-byte filename prefix for files this side should poll.
// Client polls "s" (responses from server), server polls "r" (requests from client).
func pollPrefix(myDir Direction) string {
	if myDir == DirReq {
		return "s"
	}
	return "r"
}
