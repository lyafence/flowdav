package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
)

// Engine manages the local sessions, periodically flushes Tx buffers to files,
// and polls for new Rx files.
//
// Lock ordering: all mutexes are acquired sequentially with one exception —
// sessionMu.RLock may be held while locking Session.mu (in txQueueStats).
// No code path ever reverses this order (Session.mu → sessionMu), so
// deadlock is impossible. All other mutexes are never held concurrently.
type Engine struct {
	backend storage.Backend
	myDir   Direction // DirReq for client, DirRes for server

	sessions  map[string]*Session
	sessionMu sync.RWMutex

	// Tombstones for recently closed sessions to prevent re-triggering on delayed packets
	closedSessions   map[string]time.Time
	closedSessionsMu sync.Mutex

	pollTicker      time.Duration
	minPollInterval time.Duration
	maxPollInterval time.Duration
	flushTicker     time.Duration
	pollActivityCh  chan struct{}

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

	// Graceful shutdown support
	stopCh  chan struct{}
	wg      sync.WaitGroup
	wgMu    sync.Mutex
	stopped bool

	// MaxSessions limits the total number of concurrent sessions (0 = unlimited)
	MaxSessions int

	// SessionIdleTimeout overrides the default idle timeout for new sessions.
	// 0 = use session default (10s).
	SessionIdleTimeout time.Duration

	// PaddingSize is the bucket size for tail padding (0 = disabled).
	PaddingSize int
	// HoldMax is the max server-side random delay (0 = disabled).
	HoldMax time.Duration

	// pollJitterMin and pollJitterMax define the jitter range for poll
	// intervals. Default 0.25–1.75 (±75%).
	pollJitterMin float64
	pollJitterMax float64

	// flushJitterMin and flushJitterMax define the jitter range for flush
	// intervals. Default 0.5–1.5 (±50%).
	flushJitterMin float64
	flushJitterMax float64

	// downloadPool limits goroutines in pollLoop
	downloadPool *DownloadWorkerPool

	// uploadJobs feeds a fixed pool of upload workers
	uploadJobs chan uploadJob

	uploadRetries   atomic.Int64
	downloadRetries atomic.Int64
}

type uploadJob struct {
	filename   string
	data       []byte
	backendIdx uint8
	sessions   []*Session // sessions contributing to this mux; used for backend migration on 429
	numBackend int        // total backends, for ReassignBackend
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
		minPollInterval: 500 * time.Millisecond,
		maxPollInterval: 60 * time.Second,
		flushTicker:     500 * time.Millisecond,
		pollActivityCh:  make(chan struct{}, 1),
		pollJitterMin:   0.25,
		pollJitterMax:   1.75,
		flushJitterMin:  0.5,
		flushJitterMax:  1.5,
	}
	if isClient {
		e.myDir = DirReq
	} else {
		e.myDir = DirRes
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

func (e *Engine) SetMaxSessions(n int) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	// Locked: MaxSessions is read under sessionMu in pool.go.
	e.MaxSessions = n
}

func (e *Engine) SetSessionIdleTimeout(ms int) {
	if ms > 0 {
		e.SessionIdleTimeout = time.Duration(ms) * time.Millisecond
	}
}

func (e *Engine) SetPaddingSize(size int) {
	if size > 0 {
		e.PaddingSize = size
	}
}

func (e *Engine) SetHoldMax(ms int) {
	if ms > 0 {
		e.HoldMax = time.Duration(ms) * time.Millisecond
	}
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

// Drain flushes all pending session data to upload jobs until either all
// txBufs and the uploadJobs channel are empty or ctx is cancelled.
// It is intended to be called during graceful shutdown before Stop.
func (e *Engine) Drain(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var gw *gzip.Writer
	if e.cryptoCfg != nil {
		gw, _ = gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		defer gw.Close()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.sessionMu.RLock()
			pendingBuf := false
			for _, s := range e.sessions {
				if s.TxBufLen() > 0 {
					pendingBuf = true
					break
				}
			}
			e.sessionMu.RUnlock()

			if !pendingBuf && len(e.uploadJobs) == 0 {
				return nil
			}

			e.flushAll(ctx, gw)
		}
	}
}

func (e *Engine) AddSession(s *Session) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()

	select {
	case <-e.stopCh:
		logger.Info("Engine.AddSession: rejected session %s, engine stopped", s.ID)
		return
	default:
	}

	e.sessions[s.ID] = s
	if e.SessionIdleTimeout > 0 {
		s.IdleTimeout = e.SessionIdleTimeout
	}
	s.notifyActivity = func() {
		select {
		case e.pollActivityCh <- struct{}{}:
		default:
		}
	}
	logger.Info("Engine.AddSession: Added session %s (Total now: %d)", s.ID, len(e.sessions))
}

func (e *Engine) flushLoop(ctx context.Context) {
	timer := time.NewTimer(e.flushTicker)
	defer timer.Stop()

	var gw *gzip.Writer
	if e.cryptoCfg != nil {
		gw, _ = gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		defer gw.Close()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			e.flushAll(ctx, gw)
			return
		case <-timer.C:
			e.flushAll(ctx, gw)
			timer.Reset(e.jitterFlushInterval(e.flushTicker))
		}
	}
}

type muxKey struct {
	BackendIdx uint8
}

func (e *Engine) flushAll(ctx context.Context, gzipWriter *gzip.Writer) {
	e.sessionMu.Lock()
	sessions := make([]*Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		sessions = append(sessions, s)
	}
	e.sessionMu.Unlock()

	// Server-side hold delay: random sleep before processing to
	// decouple request timing from response timing.
	if e.HoldMax > 0 && e.myDir == DirRes {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			logger.Info("hold delay rand read error: %v", err)
		}
		n := int64(b[0]) | int64(b[1])<<8 | int64(b[2])<<16 | int64(b[3])<<24 |
			int64(b[4])<<32 | int64(b[5])<<40 | int64(b[6])<<48 | int64(b[7])<<56
		if n < 0 {
			n = -n
		}
		d := time.Duration(n % int64(e.HoldMax))
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return
		}
	}

	muxes := make(map[muxKey][]Envelope)
	muxSessions := make(map[muxKey][]*Session)
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
		muxSessions[key] = append(muxSessions[key], s)
	}

	for _, s := range sessionsToWake {
		s.wakeupTx()
	}

	// Count total backends for session migration on 429
	numBackend := 1
	if mb, ok := e.backend.(*storage.MultiBackend); ok {
		numBackend = mb.NumBackends()
	}

	for key, mux := range muxes {
		// Split muxed envelopes into chunks that fit within the upload limit
		// to prevent silent truncation by WebDAV upload limits
		sessList := muxSessions[key]
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
				if err := env.encodeWithCrypto(&buf, e.cryptoCfg, gzipWriter); err != nil {
					logger.Info("mux encode error: %v", err)
					return
				}
				if buf.Len() > safeUploadSize() && consumed > 0 {
					buf.Truncate(before)
					break
				}
				consumed++
			}

			// Tail padding: add random bytes up to safeUploadSize to
			// disguise the true payload size from storage observers.
			if e.PaddingSize > 0 {
				padFile(&buf, e.PaddingSize, safeUploadSize())
			}

			filename, err := randomFilename(uploadPrefix(e.myDir))
			if err != nil {
				logger.Info("random filename error: %v", err)
				return
			}

			select {
			case e.uploadJobs <- uploadJob{
				filename:   filename,
				data:       buf.Bytes(),
				backendIdx: key.BackendIdx,
				sessions:   sessList,
				numBackend: numBackend,
			}:
			case <-ctx.Done():
				return
			case <-e.stopCh:
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
		case <-e.pollActivityCh:
			currentPollInterval = e.minPollInterval
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(e.jitterPollInterval(currentPollInterval))
			continue
		case <-timer.C:
			if e.myDir == DirReq {
				e.sessionMu.RLock()
				count := len(e.sessions)
				e.sessionMu.RUnlock()
				if count == 0 {
					timer.Reset(e.jitterPollInterval(currentPollInterval))
					continue
				}
			}

			files, err := e.backend.ListQuery(ctx, peerPrefix)
			if err != nil {
				logger.Info("poll list error: %v", err)
				timer.Reset(e.jitterPollInterval(currentPollInterval))
				continue
			}

			if len(files) == 0 {
				// Exponential backoff: double interval, cap at max
				currentPollInterval *= 2
				if currentPollInterval > e.maxPollInterval {
					currentPollInterval = e.maxPollInterval
				}
				timer.Reset(e.jitterPollInterval(currentPollInterval))
				continue
			}

			// Reset to base interval on data received
			currentPollInterval = e.minPollInterval

			for _, entry := range files {
				// GC: delete files older than 5 minutes
				if time.Since(entry.ModTime) > 5*time.Minute {
					if err := e.backend.Delete(ctx, entry.Filename); err != nil {
						logger.Info("GC delete error %s: %v", entry.Filename, err)
					}
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
			backoffTimer.Reset(e.jitterPollInterval(100 * time.Millisecond))
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

			timer.Reset(e.jitterPollInterval(currentPollInterval))
			continue
		}
	}
}

// jitterPollInterval applies random jitter within the engine's
// configured range to reduce traffic fingerprinting.
func (e *Engine) jitterPollInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return d
	}
	rng := e.pollJitterMax - e.pollJitterMin
	factor := e.pollJitterMin + float64(b[0])/255.0*rng
	return time.Duration(float64(d) * factor)
}

// jitterFlushInterval applies random jitter within the engine's
// configured flush range to avoid fixed-interval patterns.
func (e *Engine) jitterFlushInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return d
	}
	rng := e.flushJitterMax - e.flushJitterMin
	factor := e.flushJitterMin + float64(b[0])/255.0*rng
	return time.Duration(float64(d) * factor)
}

// randomFilename generates an obfuscated filename with a direction prefix
// to avoid leaking client ID, timing, or session metadata via filenames.
func randomFilename(dirByte string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand read failed: %w", err)
	}
	return dirByte + hex.EncodeToString(b[:]), nil
}

func (e *Engine) RemoveSession(id string) {
	e.sessionMu.Lock()

	// Add tombstone BEFORE deleting from sessions so that a concurrent
	// download worker cannot recreate the session in the gap.
	e.closedSessionsMu.Lock()
	e.closedSessions[id] = time.Now()
	e.closedSessionsMu.Unlock()

	delete(e.sessions, id)
	count := len(e.sessions)
	e.sessionMu.Unlock()

	logger.Info("Engine: Session %s removed (Total now: %d)", id, count)

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
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Info("upload panic %s: %v", job.filename, r)
					}
				}()
				attempts, err := retryStorage(ctx, e.stopCh, "upload "+job.filename, func() error {
					return e.backend.UploadByIndex(ctx, job.filename, bytes.NewReader(job.data), job.backendIdx)
				})
				if err == nil {
					select {
					case e.pollActivityCh <- struct{}{}:
					default:
					}
				}
				if attempts > 1 {
					e.uploadRetries.Add(int64(attempts - 1))
				}
				if err != nil {
					// If rate-limited, try non-indexed upload (picks different backend)
					if storage.IsRateLimited(err) {
						logger.Info("upload 429 %s (backend %d): trying fallback", job.filename, job.backendIdx)
						newIdx, fallbackErr := e.backend.UploadAny(ctx, job.filename, bytes.NewReader(job.data))
						if fallbackErr == nil {
							logger.Info("upload fallback succeeded %s → backend %d", job.filename, newIdx)
							for _, s := range job.sessions {
								if s.ReassignBackend(job.numBackend) {
									logger.Info("session %s migrated to backend %d after 429", s.ID, s.BackendIdx)
								}
							}
							return
						}
						logger.Info("upload fallback also failed %s: %v", job.filename, fallbackErr)
					}
					logger.Info("upload error %s (backend %d): %v", job.filename, job.backendIdx, err)
				}
			}()
		}
	}
}

type EngineStats struct {
	ActiveSessions  int                   `json:"active_sessions"`
	ProcessedFiles  int                   `json:"processed_files"`
	ClosedSessions  int                   `json:"closed_sessions"`
	UploadRetries   int64                 `json:"upload_retries"`
	DownloadRetries int64                 `json:"download_retries"`
	TxQueueBytes    int64                 `json:"tx_queue_bytes"`
	TxQueueSessions int                   `json:"tx_queue_sessions"`
	PollTickerMs    int                   `json:"poll_ticker_ms"`
	FlushTickerMs   int                   `json:"flush_ticker_ms"`
	Role            string                `json:"role"`
	Backends        []storage.BackendStat `json:"backends,omitempty"`
}

func (e *Engine) txQueueStats() (bytes int64, sessions int) {
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	for _, s := range e.sessions {
		b := s.TxBufLen()
		bytes += int64(b)
		if b > 0 {
			sessions++
		}
	}
	return
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

	txBytes, txSessions := e.txQueueStats()

	var backends []storage.BackendStat
	if mb, ok := e.backend.(*storage.MultiBackend); ok {
		backends = mb.Stats()
	}

	role := "client"
	if e.myDir == DirRes {
		role = "server"
	}

	return EngineStats{
		ActiveSessions:  sessions,
		ProcessedFiles:  processed,
		ClosedSessions:  closed,
		UploadRetries:   e.uploadRetries.Load(),
		DownloadRetries: e.downloadRetries.Load(),
		TxQueueBytes:    txBytes,
		TxQueueSessions: txSessions,
		PollTickerMs:    int(e.pollTicker.Milliseconds()),
		FlushTickerMs:   int(e.flushTicker.Milliseconds()),
		Role:            role,
		Backends:        backends,
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

// padFile appends random bytes to buf up to a bucket boundary with
// random slack, capping at maxSize to respect upload limits.
func padFile(buf *bytes.Buffer, bucket, maxSize int) {
	if buf.Len() >= maxSize {
		return
	}
	// Always pad: when remain==0 (exact bucket multiple) we pad to the
	// next bucket boundary + random slack, to avoid leaking alignment.
	padLen := bucket - (buf.Len() % bucket)
	var r [1]byte
	if _, err := rand.Read(r[:]); err != nil {
		logger.Info("padding rand read error: %v", err)
	}
	padLen += int(r[0])
	if buf.Len()+padLen > maxSize {
		padLen = maxSize - buf.Len()
	}
	if padLen > 0 {
		pad := make([]byte, padLen)
		if _, err := rand.Read(pad); err != nil {
			logger.Info("padding rand read error: %v", err)
		}
		buf.Write(pad)
	}
}
