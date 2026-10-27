package transport

import (
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/config"
	"github.com/lyafence/flowdav/internal/storage"
)

func TestApplyConfigRefreshRateSetsBasePoll(t *testing.T) {
	e := NewEngine(&mockBackend{}, true, nil)
	ApplyConfigToEngine(&config.AppConfig{RefreshRateMs: 5000}, e)
	if e.minPollInterval != 5*time.Second {
		t.Fatalf("minPollInterval = %v, want 5s", e.minPollInterval)
	}
	if e.pollTicker != 5*time.Second {
		t.Fatalf("pollTicker = %v, want 5s", e.pollTicker)
	}

	ApplyConfigToEngine(&config.AppConfig{RefreshRateMs: 5000, MinPollMs: 1000}, e)
	if e.minPollInterval != time.Second {
		t.Fatalf("explicit MinPollMs must win, got %v", e.minPollInterval)
	}
}

func TestApplyConfigResetsStickyGlobals(t *testing.T) {
	oldMax := MaxMessageSize
	oldFile := storage.MaxFileSize
	t.Cleanup(func() {
		MaxMessageSize = oldMax
		storage.MaxFileSize = oldFile
	})

	e := NewEngine(&mockBackend{}, true, nil)
	ApplyConfigToEngine(&config.AppConfig{MaxMessageSize: 64 * 1024 * 1024}, e)
	if MaxMessageSize != 64*1024*1024 || storage.MaxFileSize != 64*1024*1024 {
		t.Fatal("max message size was not applied")
	}

	ApplyConfigToEngine(&config.AppConfig{}, e)
	if MaxMessageSize != DefaultMaxMessageSize || storage.MaxFileSize != DefaultMaxMessageSize {
		t.Fatalf("globals were not reset: transport=%d storage=%d", MaxMessageSize, storage.MaxFileSize)
	}
}
