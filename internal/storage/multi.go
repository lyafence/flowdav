package storage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/lyafence/flowdav/internal/logger"
)

const (
	cbThreshold = 3
	cbCooldown  = 30 * time.Second
)

type backendHealth struct {
	failures int
	lastFail time.Time
}

type MultiBackend struct {
	backends  []Backend
	health    []backendHealth
	mu        sync.Mutex
	rrCounter uint64
}

func NewMultiBackend(backends []Backend) *MultiBackend {
	return &MultiBackend{
		backends: backends,
		health:   make([]backendHealth, len(backends)),
	}
}

func (m *MultiBackend) NumBackends() int {
	return len(m.backends)
}

func (m *MultiBackend) isAvailable(idx int) bool {
	if idx >= len(m.health) {
		return false
	}
	h := &m.health[idx]
	if h.failures < cbThreshold {
		return true
	}
	if time.Since(h.lastFail) > cbCooldown {
		h.failures = 0
		return true
	}
	return false
}

func (m *MultiBackend) recordFailure(idx int) {
	if idx >= len(m.health) {
		return
	}
	m.health[idx].failures++
	m.health[idx].lastFail = time.Now()
}

func (m *MultiBackend) recordSuccess(idx int) {
	if idx >= len(m.health) {
		return
	}
	m.health[idx].failures = 0
}

func (m *MultiBackend) nextAvailableBackend() (Backend, int) {
	n := len(m.backends)
	if n == 0 {
		return nil, -1
	}
	for i := 0; i < n; i++ {
		idx := int(m.rrCounter % uint64(n))
		m.rrCounter++
		if m.isAvailable(idx) {
			return m.backends[idx], idx
		}
	}
	return nil, -1
}

func (m *MultiBackend) RoundRobinBackend() Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	be, _ := m.nextAvailableBackend()
	return be
}

func (m *MultiBackend) BackendByIndex(idx uint8) Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backendByIndexLocked(idx)
}

func (m *MultiBackend) backendByIndexLocked(idx uint8) Backend {
	n := len(m.backends)
	if n == 0 {
		return nil
	}
	i := int(idx) % n
	if !m.isAvailable(i) {
		logger.Info("MultiBackend: backend %d unavailable (circuit open)", i)
		return nil
	}
	return m.backends[i]
}

func (m *MultiBackend) Login(ctx context.Context) error {
	for i, be := range m.backends {
		if err := be.Login(ctx); err != nil {
			return err
		}
		logger.Debug("MultiBackend: backend[%d] login OK", i)
	}
	return nil
}

func (m *MultiBackend) Upload(ctx context.Context, filename string, data io.Reader) error {
	m.mu.Lock()
	be, idx := m.nextAvailableBackend()
	m.mu.Unlock()
	if be == nil {
		return errors.New("no backends available")
	}
	err := be.Upload(ctx, filename, data)
	m.mu.Lock()
	if err != nil {
		m.recordFailure(idx)
	} else {
		m.recordSuccess(idx)
	}
	m.mu.Unlock()
	return err
}

func (m *MultiBackend) ListQuery(ctx context.Context, prefix string) ([]FileEntry, error) {
	var allFiles []FileEntry
	for i, be := range m.backends {
		files, err := be.ListQuery(ctx, prefix)
		if err != nil {
			logger.Info("MultiBackend: backend[%d] ListQuery error: %v", i, err)
			continue
		}
		for _, f := range files {
			f.BackendIdx = uint8(i)
			allFiles = append(allFiles, f)
		}
	}
	return allFiles, nil
}

func (m *MultiBackend) Download(ctx context.Context, filename string) (io.ReadCloser, error) {
	for i, be := range m.backends {
		rc, err := be.Download(ctx, filename)
		if err == nil {
			return rc, nil
		}
		logger.Info("MultiBackend: backend[%d] Download %s error: %v", i, filename, err)
	}
	return nil, errors.New("file not found in any backend")
}

func (m *MultiBackend) Delete(ctx context.Context, filename string) error {
	var errs []error
	for i, be := range m.backends {
		if err := be.Delete(ctx, filename); err != nil {
			logger.Info("MultiBackend: backend[%d] Delete %s error: %v", i, filename, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *MultiBackend) UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error {
	m.mu.Lock()
	be := m.backendByIndexLocked(idx)
	if be == nil {
		m.mu.Unlock()
		return errors.New("no backend available for index")
	}
	m.mu.Unlock()

	err := be.Upload(ctx, filename, data)

	m.mu.Lock()
	if err != nil {
		m.recordFailure(int(idx))
	} else {
		m.recordSuccess(int(idx))
	}
	m.mu.Unlock()
	return err
}

func (m *MultiBackend) DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error) {
	m.mu.Lock()
	be := m.backendByIndexLocked(idx)
	if be == nil {
		m.mu.Unlock()
		return nil, errors.New("no backend available for index")
	}
	m.mu.Unlock()

	rc, err := be.Download(ctx, filename)

	m.mu.Lock()
	if err != nil {
		m.recordFailure(int(idx))
	} else {
		m.recordSuccess(int(idx))
	}
	m.mu.Unlock()
	return rc, err
}

func RandBackendIndex(n int) int {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	v := binary.BigEndian.Uint64(b[:])
	return int(v % uint64(n))
}
