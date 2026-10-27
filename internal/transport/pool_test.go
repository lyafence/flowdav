package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/storage"
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

// envelopeBackend returns a fixed reader for DownloadByIndex so a test can
// feed pre-encoded envelopes into processDownload.
type envelopeBackend struct {
	mockBackend
	rc io.ReadCloser
}

func (e *envelopeBackend) DownloadByIndex(_ context.Context, _ string, _ uint8) (io.ReadCloser, error) {
	return e.rc, nil
}

// TestProcessDownloadMaxSessions verifies the MaxSessions guard (pool.go):
// when the session limit is reached, new out-of-order sessions are dropped
// instead of being created, and no panic occurs.
func TestProcessDownloadMaxSessions(t *testing.T) {
	env1, err := (&Envelope{SessionID: "sess-max-1", Seq: 0, TargetAddr: "10.0.0.1:443"}).MarshalBinary()
	if err != nil {
		t.Fatalf("marshal env1: %v", err)
	}
	env2, err := (&Envelope{SessionID: "sess-max-2", Seq: 0, TargetAddr: "10.0.0.2:443"}).MarshalBinary()
	if err != nil {
		t.Fatalf("marshal env2: %v", err)
	}
	data := append(append([]byte{}, env1...), env2...)

	be := &envelopeBackend{rc: io.NopCloser(bytes.NewReader(data))}
	engine := NewEngine(be, false, nil) // myDir = DirRes (server side)
	engine.SetMaxSessions(1)
	engine.SetPollRate(50)
	engine.OnNewSession = func(string, string, *Session) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	engine.downloadPool.Submit(downloadJob{filename: "max-sessions.bin", backendIdx: 0}, engine.stopCh)

	// Wait deterministically for the worker to process the job (the select in
	// pool.go may pick stopCh over the job channel otherwise). Deadline is
	// mandatory: without it a decode error would hang the test.
	deadline := time.Now().Add(5 * time.Second)
	for {
		engine.sessionMu.RLock()
		n := len(engine.sessions)
		engine.sessionMu.RUnlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			engine.Stop()
			t.Fatal("timed out waiting for session creation")
		}
		time.Sleep(10 * time.Millisecond)
	}

	engine.Stop()

	engine.sessionMu.RLock()
	n := len(engine.sessions)
	engine.sessionMu.RUnlock()
	if n != 1 {
		t.Errorf("expected exactly 1 session with MaxSessions=1, got %d", n)
	}
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

// TestRetryStorageStopsOnRateLimit verifies retryStorage returns the 429
// immediately instead of burning retries inside the 60s cooldown. Retrying
// masked the 429 from the upload/download fallback logic, so sessions pinned
// to a rate-limited backend never migrated and data was lost.
func TestRetryStorageStopsOnRateLimit(t *testing.T) {
	attempts := 0
	_, err := retryStorage(context.Background(), make(chan struct{}), "test-429", func() error {
		attempts++
		return &storage.HTTPError{Code: http.StatusTooManyRequests, Err: fmt.Errorf("rate limited")}
	})
	if attempts != 1 {
		t.Errorf("rate limit must not be retried, got %d attempts", attempts)
	}
	if !storage.IsRateLimited(err) {
		t.Errorf("retryStorage must return the 429 so callers can trigger fallback, got %v", err)
	}
}

// failingUploadBackend fails UploadByIndex with a generic (non-429) error and
// records fallback invocations via UploadAny.
type failingUploadBackend struct {
	mockBackend
	mu       sync.Mutex
	fallback int
}

func (f *failingUploadBackend) UploadByIndex(_ context.Context, _ string, _ io.Reader, _ uint8) error {
	return errors.New("backend 0 down")
}

func (f *failingUploadBackend) UploadAny(_ context.Context, _ string, _ io.Reader) (uint8, error) {
	f.mu.Lock()
	f.fallback++
	f.mu.Unlock()
	return 1, nil
}

// TestUploadWorkerFallsBackOnNon429Error verifies the payload is re-tried on
// another backend instead of being dropped when the pinned backend fails with
// a non-429 error (the txBuf was already consumed at this point).
func TestUploadWorkerFallsBackOnNon429Error(t *testing.T) {
	be := &failingUploadBackend{}
	engine := NewEngine(be, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)

	engine.uploadJobs <- uploadJob{filename: "f.bin", data: []byte("payload"), backendIdx: 0}

	deadline := time.Now().Add(5 * time.Second)
	for {
		be.mu.Lock()
		n := be.fallback
		be.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			engine.Stop()
			t.Fatal("UploadAny fallback was not invoked for a non-429 upload error")
		}
		time.Sleep(10 * time.Millisecond)
	}
	engine.Stop()
}

// TestPollGCSkipsInFlightFile verifies GC does not delete a file that has a
// live processed entry (queued/processing), which previously destroyed
// in-flight payloads under load.
func TestPollGCSkipsInFlightFile(t *testing.T) {
	be := &mockBackend{listFiles: []storage.FileEntry{
		{Filename: "old.bin", ModTime: time.Now().Add(-10 * time.Minute)},
	}}
	engine := NewEngine(be, false, nil) // server side: pollLoop is active
	engine.SetPollRate(50)

	engine.processedMu.Lock()
	engine.processed["old.bin"] = time.Now()
	engine.processedMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	time.Sleep(300 * time.Millisecond)
	engine.Stop()

	be.mu.Lock()
	defer be.mu.Unlock()
	for _, d := range be.deleted {
		if d == "old.bin" {
			t.Error("GC deleted a file with a live processed entry (in-flight data loss)")
		}
	}
}

// TestPollGCDeletesStaleUnprocessedFile verifies GC still removes files never
// picked up (e.g. left behind by a dead peer) so storage stays bounded.
func TestPollGCDeletesStaleUnprocessedFile(t *testing.T) {
	be := &mockBackend{listFiles: []storage.FileEntry{
		{Filename: "dead.bin", ModTime: time.Now().Add(-10 * time.Minute)},
	}}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	time.Sleep(300 * time.Millisecond)
	engine.Stop()

	be.mu.Lock()
	defer be.mu.Unlock()
	for _, d := range be.deleted {
		if d == "dead.bin" {
			return
		}
	}
	t.Error("stale unprocessed file (dead peer) must be GC-deleted")
}

type rateLimitedOnceBackend struct {
	mockBackend
	rateOnce bool
}

func (b *rateLimitedOnceBackend) Upload(ctx context.Context, name string, r io.Reader) error {
	if b.rateOnce {
		b.rateOnce = false
		return &storage.HTTPError{Code: http.StatusTooManyRequests, Err: errors.New("rate limited")}
	}
	return b.mockBackend.Upload(ctx, name, r)
}

// TestUploadWorkerMigratesSessionAfterCooldown verifies a session is moved off
// a backend that is already in 429 cooldown (where UploadByIndex returns a
// generic ErrBackendUnavailable instead of an HTTPError).
func TestUploadWorkerMigratesSessionAfterCooldown(t *testing.T) {
	be0 := &rateLimitedOnceBackend{rateOnce: true}
	be1 := &mockBackend{}
	multi := storage.NewMultiBackend([]storage.Backend{be0, be1})

	// Trigger a 429 once so backend 0 enters rate-limit cooldown.
	_ = multi.UploadByIndex(context.Background(), "prime.bin", bytes.NewReader(nil), 0)

	engine := NewEngine(multi, true, nil)
	session := NewSession("migrate-session")
	session.BackendIdx = 0
	engine.sessionMu.Lock()
	engine.sessions[session.ID] = session
	engine.sessionMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	engine.uploadJobs <- uploadJob{filename: "f.bin", data: []byte("payload"), backendIdx: 0, sessions: []*Session{session}}

	deadline := time.Now().Add(5 * time.Second)
	for {
		session.mu.Lock()
		idx := session.BackendIdx
		session.mu.Unlock()
		if idx != 0 {
			break
		}
		if time.Now().After(deadline) {
			engine.Stop()
			t.Fatal("session was not migrated off the cooling-down backend")
		}
		time.Sleep(10 * time.Millisecond)
	}
	engine.Stop()
}

type downloadRateLimitedBackend struct{ mockBackend }

func (b *downloadRateLimitedBackend) DownloadByIndex(_ context.Context, _ string, _ uint8) (io.ReadCloser, error) {
	return nil, &storage.HTTPError{Code: http.StatusTooManyRequests, Err: errors.New("rate limited")}
}

func (b *downloadRateLimitedBackend) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, &storage.HTTPError{Code: http.StatusTooManyRequests, Err: errors.New("rate limited")}
}

type payloadDownloadBackend struct {
	mockBackend
	data []byte
}

func (b *payloadDownloadBackend) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

// TestDownload429MigratesSession verifies a session is moved off a backend
// after a rate-limited download fallback succeeds on another backend.
func TestDownload429MigratesSession(t *testing.T) {
	env := &Envelope{SessionID: "dl-session", Seq: 0, TargetAddr: "example.com:80", Payload: []byte("hello")}
	payload, err := env.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	be0 := &downloadRateLimitedBackend{}
	be1 := &payloadDownloadBackend{data: payload}
	multi := storage.NewMultiBackend([]storage.Backend{be0, be1})

	engine := NewEngine(multi, true, nil)
	session := NewSession("dl-session")
	session.BackendIdx = 0
	engine.sessionMu.Lock()
	engine.sessions[session.ID] = session
	engine.sessionMu.Unlock()

	engine.downloadPool.processDownload(context.Background(), engine.stopCh, &downloadJob{filename: "r0123456789abcdef", backendIdx: 0})

	if session.BackendIdx == 0 {
		t.Fatal("session was not migrated after download 429 fallback")
	}
}
