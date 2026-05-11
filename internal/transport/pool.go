package transport

import (
	"context"
	"io"
	"sync"

	"github.com/lyafence/flowdav/internal/logger"
)

type downloadJob struct {
	filename   string
	backendIdx uint8
}

type DownloadWorkerPool struct {
	engine    *Engine
	jobs      chan downloadJob
	workers   int
	wg        sync.WaitGroup
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
				default:
				}

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

func (p *DownloadWorkerPool) Submit(job downloadJob, stopCh <-chan struct{}) {
	select {
	case p.jobs <- job:
	case <-stopCh:
	}
}

func (p *DownloadWorkerPool) processDownload(ctx context.Context, stopCh <-chan struct{}, job *downloadJob) {
	defer func() {
		if r := recover(); r != nil {
			logger.Info("download panic %s: %v", job.filename, r)
		}
	}()

	e := p.engine

	e.sem <- struct{}{}
	defer func() { <-e.sem }()

	select {
	case <-ctx.Done():
		return
	case <-stopCh:
		return
	default:
	}

	rc, err := e.backend.DownloadByIndex(ctx, job.filename, job.backendIdx)
	if err != nil {
		if rc != nil {
			rc.Close()
		}
		logger.Info("download error %s (backend %d): %v", job.filename, job.backendIdx, err)
		return
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

		e.sessionMu.Lock()
		s, exists := e.sessions[env.SessionID]
		if !exists && e.myDir == DirRes && e.OnNewSession != nil {
			if e.MaxSessions > 0 && len(e.sessions) >= e.MaxSessions {
				e.sessionMu.Unlock()
				logger.Info("Engine: session limit reached (%d), dropping new session %s", e.MaxSessions, env.SessionID)
				continue
			}
			s = NewSession(env.SessionID)
			s.BackendIdx = env.BackendIdx
			e.sessions[env.SessionID] = s
			sessionID := env.SessionID
			targetAddr := env.TargetAddr
			backendIdx := env.BackendIdx
			e.sessionMu.Unlock()
			logger.Info("Engine: Triggering new session %s (backend %d)", sessionID, backendIdx)
			e.OnNewSession(sessionID, targetAddr, s)
		} else {
			e.sessionMu.Unlock()
		}

		if s != nil {
			envCopy := env
			s.ProcessRx(&envCopy)
		}
	}

	if err := e.backend.Delete(ctx, job.filename); err != nil {
		logger.Info("delete error %s: %v — keeping processed entry for TTL-based retry", job.filename, err)
	} else {
		e.processedMu.Lock()
		delete(e.processed, job.filename)
		e.processedMu.Unlock()
	}
}
