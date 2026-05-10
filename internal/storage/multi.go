package storage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync/atomic"

	"github.com/lyafence/flowdav/internal/logger"
)

type MultiBackend struct {
	backends  []Backend
	rrCounter atomic.Uint32
}

func NewMultiBackend(backends []Backend) *MultiBackend {
	return &MultiBackend{
		backends: backends,
	}
}

func (m *MultiBackend) NumBackends() int {
	return len(m.backends)
}

func (m *MultiBackend) RoundRobinBackend() Backend {
	n := len(m.backends)
	if n == 0 {
		return nil
	}
	idx := (m.rrCounter.Add(1) - 1) % uint32(n)
	return m.backends[idx]
}

func (m *MultiBackend) BackendByIndex(idx uint8) Backend {
	n := len(m.backends)
	if n == 0 {
		return nil
	}
	return m.backends[idx%uint8(n)]
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
	backend := m.RoundRobinBackend()
	if backend == nil {
		return errors.New("no backends available")
	}
	return backend.Upload(ctx, filename, data)
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
	for i, be := range m.backends {
		if err := be.Delete(ctx, filename); err != nil {
			logger.Info("MultiBackend: backend[%d] Delete %s error: %v", i, filename, err)
		}
	}
	return nil
}

func (m *MultiBackend) UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error {
	backend := m.BackendByIndex(idx)
	if backend == nil {
		return errors.New("no backend available for index")
	}
	return backend.Upload(ctx, filename, data)
}

func (m *MultiBackend) DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error) {
	backend := m.BackendByIndex(idx)
	if backend == nil {
		return nil, errors.New("no backend available for index")
	}
	return backend.Download(ctx, filename)
}

// RandBackendIndex returns a random backend index in the range [0, n).
// Uses crypto/rand for cryptographically secure randomness.
func RandBackendIndex(n int) int {
	if n <= 0 {
		return 0
	}
	// Generate a random uint64 and reduce it modulo n
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// This should never happen, but if it does, fall back to deterministic behavior
		return 0
	}
	v := binary.BigEndian.Uint64(b[:])
	return int(v % uint64(n))
}
