package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type deleteRecordingBackend struct {
	mockBackend
	failDelete bool
	mu         sync.Mutex
}

func (d *deleteRecordingBackend) DownloadByIndex(_ context.Context, _ string, _ uint8) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (d *deleteRecordingBackend) Delete(_ context.Context, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failDelete {
		return fmt.Errorf("simulated delete failure")
	}
	return nil
}

// TestPoolSubmitAfterStop verifies that Submit returns immediately
// when stopCh is already closed (no deadlock on shutdown).
func TestPoolSubmitAfterStop(t *testing.T) {
	be := &deleteRecordingBackend{failDelete: true}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	engine.Stop()

	done := make(chan struct{})
	go func() {
		engine.downloadPool.Submit(downloadJob{
			filename:   "after-stop.bin",
			backendIdx: 0,
		}, engine.stopCh)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Submit blocked on closed stopCh")
	}
}

// TestPoolStartStopLifecycle verifies Start → Stop completes within timeout.
func TestPoolStartStopLifecycle(t *testing.T) {
	pool := NewDownloadWorkerPool(nil, 4)
	ctx, cancel := context.WithCancel(context.Background())
	stopCh := make(chan struct{})

	pool.Start(ctx, stopCh)
	close(stopCh)

	done := make(chan struct{})
	go func() {
		pool.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Stop() did not complete within timeout")
	}
	cancel()
}

// TestDeleteErrorPreservesProcessedEntry verifies that when Delete fails,
// the processed entry is NOT removed, allowing TTL-based retry.
func TestDeleteErrorPreservesProcessedEntry(t *testing.T) {
	be := &deleteRecordingBackend{failDelete: true}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	engine.processedMu.Lock()
	engine.processed["test-file.bin"] = time.Now()
	engine.processedMu.Unlock()

	engine.downloadPool.Submit(downloadJob{
		filename:   "test-file.bin",
		backendIdx: 0,
	}, engine.stopCh)

	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	engine.processedMu.Lock()
	_, exists := engine.processed["test-file.bin"]
	engine.processedMu.Unlock()

	if !exists {
		t.Error("processed entry removed despite Delete failure")
	}
}

// TestProcessDownloadStopsOnContext verifies that a download worker
// returns promptly when its context is cancelled, even if the semaphore
// is saturated.
func TestProcessDownloadStopsOnContext(t *testing.T) {
	be := &deleteRecordingBackend{failDelete: false}
	engine := NewEngine(be, false, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)

	// Saturate the semaphore.
	for i := 0; i < cap(engine.sem); i++ {
		engine.sem <- struct{}{}
	}

	cancel()

	done := make(chan struct{})
	go func() {
		engine.downloadPool.processDownload(ctx, engine.stopCh, &downloadJob{
			filename:   "ctx-cancelled.bin",
			backendIdx: 0,
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("processDownload blocked on saturated semaphore despite cancelled context")
	}

	// Drain semaphore to avoid leaking goroutines in other tests.
	for i := 0; i < cap(engine.sem); i++ {
		select {
		case <-engine.sem:
		default:
		}
	}
	engine.Stop()
}

// TestDeleteSuccessRemovesProcessedEntry verifies the happy path:
// when Delete succeeds, the processed entry IS removed.
func TestDeleteSuccessRemovesProcessedEntry(t *testing.T) {
	be := &deleteRecordingBackend{failDelete: false}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	engine.processedMu.Lock()
	engine.processed["test-file-ok.bin"] = time.Now()
	engine.processedMu.Unlock()

	engine.downloadPool.Submit(downloadJob{
		filename:   "test-file-ok.bin",
		backendIdx: 0,
	}, engine.stopCh)

	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	engine.processedMu.Lock()
	_, exists := engine.processed["test-file-ok.bin"]
	engine.processedMu.Unlock()

	if exists {
		t.Error("processed entry NOT removed despite successful Delete")
	}
}
