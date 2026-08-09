package transport

import (
	"context"
	"sync"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
)

// wakeupTx wakes all goroutines blocked in EnqueueTxCtx due to backpressure.
// Must be called without holding s.mu.
func (s *Session) wakeupTx() {
	s.mu.Lock()
	ch := s.txWait
	s.txWait = make(chan struct{})
	s.mu.Unlock()
	close(ch)
}

// Direction indicates if a file is rq (client to server) or rs (server to client)
type Direction string

const (
	DirReq Direction = "rq"
	DirRes Direction = "rs"
)

// MaxRxQueueSize limits out-of-order packet queue to prevent memory exhaustion
const (
	MaxRxQueueSize  = 1000
	MaxRxQueueBytes = 256 * 1024 * 1024 // 256MB per session
	// DefaultRxChanSize is the RxChan buffer depth (64 slots, ~256KB at 4KB payloads).
	DefaultRxChanSize = 64
)

// Session represents an active proxy connection mapped to files.
type Session struct {
	ID           string
	mu           sync.Mutex
	txBuf        []byte
	txSeq        uint64
	rxSeq        uint64
	rxQueue      map[uint64]Envelope
	rxQueueBytes int // total payload bytes in rxQueue; checked alongside MaxRxQueueSize
	lastActivity time.Time
	closed       bool
	rxClosed     bool // Safely tracks if RxChan was successfully closed
	rxOnce       sync.Once
	rxDone       chan struct{} // Closed alongside RxChan; select on this in ProcessRx to prevent send on closed channel
	TargetAddr   string

	// Backpressure: blocked when txBuf is too large
	// txWait is a channel closed to wake up waiters; replaced after each wakeup.
	txWait chan struct{}

	// App channel for receiving data downloaded from remote
	RxChan chan []byte

	// BackendIdx is the index of the WebDAV backend assigned to this session.
	// Assigned on seq=0 (client writes it). Server reads it and uses the same backend.
	BackendIdx uint8

	// notifyActivity is set by Engine.AddSession to reset the poll timer
	// when new data is enqueued, ensuring fast polling after idle backoff.
	notifyActivity func()

	// IdleTimeout is the inactivity duration after which the session is
	// automatically closed. Overridden by Engine.SessionIdleTimeout
	// if set. Default 10s.
	IdleTimeout time.Duration
}

// ReassignBackend picks a new random backend index different from the
// current one. Returns false if <2 backends.
func (s *Session) ReassignBackend(numBackends int) bool {
	if numBackends < 2 {
		return false
	}
	// First draw happens before the lock: crypto/rand may block briefly
	// (getrandom at early boot). Compare and assign stay under one mutex.
	newIdx := storage.CryptoRandInt(numBackends)
	s.mu.Lock()
	current := int(s.BackendIdx)
	for newIdx == current {
		newIdx = storage.CryptoRandInt(numBackends)
	}
	s.BackendIdx = uint8(newIdx)
	s.mu.Unlock()
	return true
}

func NewSession(id string) *Session {
	s := &Session{
		ID:           id,
		rxQueue:      make(map[uint64]Envelope),
		lastActivity: time.Now(),
		RxChan:       make(chan []byte, DefaultRxChanSize),
		txWait:       make(chan struct{}),
		rxDone:       make(chan struct{}),
		IdleTimeout:  10 * time.Second,
	}
	return s
}

// Close marks the session as closed and wakes any blocked writers.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.rxClosed = true
	s.mu.Unlock()
	s.rxOnce.Do(func() {
		close(s.RxChan)
		close(s.rxDone)
	})
	s.wakeupTx()
}

func (s *Session) TxBufLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.txBuf)
}

func (s *Session) EnqueueTx(ctx context.Context, data []byte) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.EnqueueTxCtx(ctx, data)
}

func (s *Session) EnqueueTxCtx(ctx context.Context, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Backpressure: block when txBuf exceeds 2MB
	for (len(s.txBuf) > 2*1024*1024 || len(s.txBuf)+len(data) > 2*1024*1024) && !s.closed {
		if ctx.Err() != nil {
			return
		}
		waitCh := s.txWait
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			return
		case <-waitCh:
		}
		s.mu.Lock()
		if ctx.Err() != nil {
			return
		}
	}

	if s.closed {
		return
	}

	s.txBuf = append(s.txBuf, data...)
	s.lastActivity = time.Now()
	if s.notifyActivity != nil {
		s.notifyActivity()
	}
}

// ExtractTxBatch atomically reads and clears the tx buffer under lock.
// Returns the buffered data, sequence number, close flag, and whether
// there is anything to send.
func (s *Session) ExtractTxBatch(isClient bool) (payload []byte, seq uint64, closed bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.lastActivity) > s.IdleTimeout {
		s.closed = true
	}

	shouldSend := len(s.txBuf) > 0 || (s.txSeq == 0 && isClient) || s.closed
	if !shouldSend {
		return nil, 0, false, false
	}

	payload = s.txBuf
	s.txBuf = nil
	seq = s.txSeq
	s.txSeq++
	closed = s.closed
	return payload, seq, closed, true
}

func (s *Session) ProcessRx(env *Envelope) {
	s.mu.Lock()

	if s.rxClosed {
		s.mu.Unlock()
		return // Ignore packets if the channel is already safely closed
	}

	// Collect payloads to send outside the lock to avoid deadlock
	var payloadsToSend [][]byte
	closeChannel := false

	if env.Seq == s.rxSeq {
		s.lastActivity = time.Now()
		if len(env.Payload) > 0 {
			// Deep copy to be consistent with queued packets
			payloadsToSend = append(payloadsToSend, append([]byte{}, env.Payload...))
		}
		s.rxSeq++
		if env.Close {
			s.rxClosed = true
			s.closed = true
			closeChannel = true
		}
		// Capture backend index on first packet (seq=0)
		if s.rxSeq == 1 {
			s.BackendIdx = env.BackendIdx
		}

		// process any queued future packets
		for {
			if nextEnv, ok := s.rxQueue[s.rxSeq]; ok {
				if len(nextEnv.Payload) > 0 {
					payloadsToSend = append(payloadsToSend, append([]byte{}, nextEnv.Payload...))
				}
				s.rxQueueBytes -= len(nextEnv.Payload)
				delete(s.rxQueue, s.rxSeq)
				s.rxSeq++
				if nextEnv.Close {
					s.rxClosed = true
					s.closed = true
					closeChannel = true
					break
				}
			} else {
				break
			}
		}
	} else if env.Seq > s.rxSeq {
		// Check queue size to prevent memory exhaustion from out-of-order packets
		if len(s.rxQueue) >= MaxRxQueueSize || s.rxQueueBytes >= MaxRxQueueBytes {
			logger.Warn("Session %s: rxQueue full (%d items, %d bytes), dropping packet seq=%d",
				s.ID, len(s.rxQueue), s.rxQueueBytes, env.Seq)
			s.mu.Unlock()
			return
		}
		// Deep-copy payload — env.Payload slice is reused after ProcessRx returns.
		payloadLen := len(env.Payload)
		s.rxQueue[env.Seq] = Envelope{
			SessionID:  env.SessionID,
			Seq:        env.Seq,
			TargetAddr: env.TargetAddr,
			Payload:    append([]byte{}, env.Payload...), // deep copy of slice
			Close:      env.Close,
		}
		s.rxQueueBytes += payloadLen
	}
	s.mu.Unlock()

	// Send payloads outside the lock to avoid deadlock
	// Reusable timer to prevent per-iteration timer leak
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for _, payload := range payloadsToSend {
		timer.Reset(30 * time.Second)
		select {
		case s.RxChan <- payload:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-s.rxDone:
			return
		case <-timer.C:
			logger.Warn("Session %s: RxChan blocked for 30s, dropping payload", s.ID)
		}
	}
	if closeChannel {
		s.rxOnce.Do(func() {
			close(s.RxChan)
			close(s.rxDone)
		})
	}
}
