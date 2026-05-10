package storage

import (
	"errors"
	"fmt"

	"github.com/lyafence/flowdav/internal/config"
	"github.com/lyafence/flowdav/internal/logger"
)

func NewBackendFromConfig(cfg *config.WebDAVConfig) (Backend, *MultiBackend, error) {
	if cfg == nil {
		return nil, nil, errors.New("webdav config is nil")
	}

	if len(cfg.Backends) > 0 {
		backends := make([]Backend, len(cfg.Backends))
		for i, be := range cfg.Backends {
			var err error
			backends[i], err = NewWebDAVBackend(be.Provider, be.Login, be.Token, be.BasePath, be.URL)
			if err != nil {
				return nil, nil, fmt.Errorf("backend[%d]: %w", i, err)
			}
		}
		multi := NewMultiBackend(backends)
		logger.Info("Using MultiBackend with %d providers", multi.NumBackends())
		return multi, multi, nil
	}

	be, err := NewWebDAVBackend(cfg.Provider, cfg.Login, cfg.Token, cfg.BasePath, cfg.URL)
	if err != nil {
		return nil, nil, err
	}
	return be, nil, nil
}
