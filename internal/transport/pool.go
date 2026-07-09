package transport

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
	"github.com/lyafence/flowdav/internal/storage"
)

const (
	retryAttempts = 3
	retryBaseWait = 100 * time.Millisecond
)

func retryStorage(ctx context.Context, stopCh <-chan struct{}, desc string, fn func() error) (attempts int, err error) {
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return attempt, ctx.Err()
			case <-stopCh:
				return attempt, context.Canceled
			default:
			}
		}

		err = fn()
		if err == nil {
			return attempt + 1, nil
		}

		if attempt < retryAttempts-1 {
			wait := retryBaseWait << attempt
			logger.Info("retry %s: attempt %d failed, retrying in %v: %v", desc, attempt+1, wait, err)
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return attempt + 1, ctx.Err()
			case <-stopCh:
				timer.Stop()
				return attempt + 1, context.Canceled
			}
		}
	}
	logger.Info("retry %s: all %d attempts failed: %v", desc, retryAttempts, err)
	return retryAttempts, err
}

type downloadJob struct {
	filename   string
	backendIdx uint8
}

type DownloadWorkerPool struct {
	engine  *Engine
	jobs    chan downloadJob
	workers int
	wg      sync.WaitGroup
}

func NewDownloadWorkerPool(engine *Engine, numWorkers int) *DownloadWorkerPool {
	return &DownloadWorkerPool{
		engine:  engine,
		jobs:    make(chan downloadJob, numWorkers*2),
		workers: numWorkers,
	}
}

func (p *DownloadWorkerPool) Start(ctx context.Context, stopCh <-chan struct{}) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopCh:
					return
				case job, ok := <-p.jobs:
					if !ok {
						return
					}
					p.processDownload(ctx, stopCh, &job)
				}
			}
		}()
	}
}

func (p *DownloadWorkerPool) Stop() {
	p.wg.Wait()
}

func (p *DownloadWorkerPool) Submit(job downloadJob, stopCh <-chan struct{}) bool {
	select {
	case p.jobs <- job:
		return true
	case <-stopCh:
		return false
	default:
		return false
	}
}

func (p *DownloadWorkerPool) processDownload(ctx context.Context, stopCh <-chan struct{}, job *downloadJob) {
	defer func() {
		if r := recover(); r != nil {
			logger.Info("download panic %s: %v", job.filename, r)
		}
	}()

	e := p.engine

	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return
	case <-stopCh:
		return
	}
	defer func() { <-e.sem }()

	var rc io.ReadCloser
	attempts, err := retryStorage(ctx, stopCh, "download "+job.filename, func() error {
		var err error
		rc, err = e.backend.DownloadByIndex(ctx, job.filename, job.backendIdx)
		return err
	})
	if attempts > 1 {
		e.downloadRetries.Add(int64(attempts - 1))
	}
	if err != nil {
		// On 429 rate-limit, try non-indexed download (searches all backends)
		if storage.IsRateLimited(err) {
			logger.Info("download 429 %s (backend %d): trying fallback across all backends", job.filename, job.backendIdx)
			if rc != nil {
				rc.Close()
			}
			rc, err = e.backend.Download(ctx, job.filename)
			if err == nil {
				logger.Info("download fallback succeeded %s", job.filename)
			}
		}
		if err != nil {
			if rc != nil {
				rc.Close()
			}
			logger.Info("download error %s (backend %d): %v", job.filename, job.backendIdx, err)
			return
		}
	}
	defer rc.Close()

	for {
		var env Envelope
		if e.cryptoCfg != nil {
			decodedEnv, err := DecodeEnvelopeWithCrypto(rc, e.cryptoCfg)
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					logger.Info("mux crypto decode error %s: %v", job.filename, err)
				}
				break
			}
			env = *decodedEnv
		} else {
			if err := env.Decode(rc); err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					logger.Info("mux decode error %s: %v", job.filename, err)
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

		e.sessionMu.RLock()
		s, exists := e.sessions[env.SessionID]
		e.sessionMu.RUnlock()

		if !exists && e.myDir == DirRes && e.OnNewSession != nil {
			e.sessionMu.Lock()
			s, exists = e.sessions[env.SessionID] // double-check
			if !exists {
				e.closedSessionsMu.Lock()
				if _, tombstoned := e.closedSessions[env.SessionID]; tombstoned {
					e.closedSessionsMu.Unlock()
					e.sessionMu.Unlock()
					continue
				}
				e.closedSessionsMu.Unlock()

				if e.MaxSessions > 0 && len(e.sessions) >= e.MaxSessions {
					e.sessionMu.Unlock()
					logger.Info("Engine: session limit reached (%d), dropping new session %s", e.MaxSessions, env.SessionID)
					continue
				}
				s = NewSession(env.SessionID)
				s.BackendIdx = env.BackendIdx
				s.notifyActivity = func() {
					select {
					case e.pollActivityCh <- struct{}{}:
					default:
					}
				}
				e.sessions[env.SessionID] = s
			}
			sessionID := env.SessionID
			targetAddr := env.TargetAddr
			backendIdx := env.BackendIdx
			e.sessionMu.Unlock()
			if !exists {
				logger.Info("Engine: Triggering new session %s (backend %d)", sessionID, backendIdx)
				e.OnNewSession(sessionID, targetAddr, s)
			}
		}

		if s != nil {
			envCopy := env
			s.ProcessRx(&envCopy)
		}
	}

	if _, err := retryStorage(ctx, stopCh, "delete "+job.filename, func() error {
		return e.backend.Delete(ctx, job.filename)
	}); err != nil {
		logger.Info("delete error %s: %v — keeping processed entry for TTL-based retry", job.filename, err)
	} else {
		e.processedMu.Lock()
		delete(e.processed, job.filename)
		e.processedMu.Unlock()
	}
}
