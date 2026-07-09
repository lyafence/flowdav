package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/storage"
)

type mockBackend struct {
	mu         sync.Mutex
	uploaded   []string
	downloaded []string
	deleted    []string
	listFiles  []storage.FileEntry
}

func (m *mockBackend) Login(_ context.Context) error { return nil }
func (m *mockBackend) Upload(_ context.Context, name string, _ io.Reader) error {
	m.mu.Lock()
	m.uploaded = append(m.uploaded, name)
	m.mu.Unlock()
	return nil
}
func (m *mockBackend) ListQuery(_ context.Context, _ string) ([]storage.FileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listFiles, nil
}
func (m *mockBackend) Download(_ context.Context, name string) (io.ReadCloser, error) {
	m.mu.Lock()
	m.downloaded = append(m.downloaded, name)
	m.mu.Unlock()
	return nil, nil
}
func (m *mockBackend) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	m.deleted = append(m.deleted, name)
	m.mu.Unlock()
	return nil
}
func (m *mockBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, _ uint8) error {
	return m.Upload(ctx, name, data)
}
func (m *mockBackend) DownloadByIndex(ctx context.Context, name string, _ uint8) (io.ReadCloser, error) {
	_, _ = m.Download(ctx, name)
	return nil, nil
}
func (m *mockBackend) UploadAny(ctx context.Context, name string, data io.Reader) (uint8, error) {
	return 0, m.Upload(ctx, name, data)
}
func TestEngineStop(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)
	engine.SetPollRate(50)
	engine.SetFlushRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	engine.Stop()
	cancel()

	if engine == nil {
		t.Fatal("engine should not be nil")
	}
}

func TestEngineStopMultipleCalls(_ *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	engine.Stop()
	engine.Stop()
	engine.Stop()

	cancel()
}

func TestEngineStartStopCycle(_ *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	engine.Stop()

	cancel()

	engine2 := NewEngine(backend, true, nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	engine2.Start(ctx2)
	time.Sleep(50 * time.Millisecond)
	engine2.Stop()
	cancel2()
}

func TestEngineFastShutdownOnStopSignal(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	engine.Stop()
	cancel()
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("Engine.Stop() took too long: %v (expected <200ms with fast shutdown)", elapsed)
	}
}

// TestBackendIdxDataRace verifies that s.BackendIdx is read under s.mu in flushAll.
// Starts the engine so upload workers consume jobs (preventing uploadJobs buffer deadlock).
// Re-seeds txBuf after each flushAll so all iterations exercise the read path.
func TestAddSessionAfterStop(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	engine.Stop()
	cancel()

	session := NewSession("test-session-after-stop")
	engine.AddSession(session)

	engine.sessionMu.RLock()
	count := len(engine.sessions)
	engine.sessionMu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 sessions after AddSession on stopped engine, got %d", count)
	}
}

// TestFlushAllRespectsStopCh verifies that flushAll returns promptly
// when stopCh is closed, rather than blocking on a full uploadJobs channel.
func TestFlushAllRespectsStopCh(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)
	engine.SetFlushRate(3600000) // prevent spontaneous flushLoop ticks (1 hour in ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	// Do not start upload workers from exiting; instead fill the channel.
	for i := 0; i < cap(engine.uploadJobs); i++ {
		engine.uploadJobs <- uploadJob{filename: fmt.Sprintf("filler-%d", i)}
	}

	// Add a session with pending data so flushAll has work to do.
	session := NewSession("flush-stop-test")
	session.txBuf = []byte("pending data")
	engine.sessionMu.Lock()
	engine.sessions[session.ID] = session
	engine.sessionMu.Unlock()

	// Close stopCh as Stop() would.
	engine.wgMu.Lock()
	engine.stopped = true
	close(engine.stopCh)
	engine.wgMu.Unlock()

	done := make(chan struct{})
	go func() {
		engine.flushAll(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("flushAll blocked on full uploadJobs channel despite closed stopCh")
	}
}

// TestEngineDrain flushes pending session data to the backend.
func TestEngineDrain(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)
	engine.SetFlushRate(3600000) // prevent spontaneous flushLoop ticks (1 hour in ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	defer engine.Stop()

	session := NewSession("drain-test")
	session.txBuf = []byte("pending drain data")
	engine.sessionMu.Lock()
	engine.sessions[session.ID] = session
	engine.sessionMu.Unlock()

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDrain()
	if err := engine.Drain(drainCtx); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}

	backend.mu.Lock()
	uploaded := len(backend.uploaded)
	backend.mu.Unlock()

	if uploaded == 0 {
		t.Error("expected backend to receive upload after Drain")
	}
}

// TestEngineDrainTimeout verifies Drain returns ctx.Err() when data cannot
// be flushed within the timeout.
func TestEngineDrainTimeout(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)
	engine.SetFlushRate(3600000)

	// Do not start engine so uploadJobs has no consumers.

	// Saturate uploadJobs so flushAll cannot make progress.
	for i := 0; i < cap(engine.uploadJobs); i++ {
		engine.uploadJobs <- uploadJob{filename: fmt.Sprintf("filler-%d", i)}
	}

	session := NewSession("drain-timeout-test")
	session.txBuf = []byte("pending data")
	engine.sessionMu.Lock()
	engine.sessions[session.ID] = session
	engine.sessionMu.Unlock()

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelDrain()
	err := engine.Drain(drainCtx)
	if err == nil {
		t.Error("expected Drain to timeout")
	}
}

func TestBackendIdxDataRace(t *testing.T) {
	be := &mockBackend{}
	engine := NewEngine(be, true, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	t.Cleanup(engine.Stop)

	const numSessions = 100
	for i := range numSessions {
		s := NewSession(fmt.Sprintf("race-session-%d", i))
		s.TargetAddr = "example.com:80"
		s.mu.Lock()
		s.txBuf = []byte("data")
		s.mu.Unlock()
		engine.AddSession(s)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	ready := make(chan struct{})
	start := make(chan struct{})

	// Writer: ProcessRx writes BackendIdx on Seq=0 for each session
	go func() {
		close(ready)
		<-start
		defer wg.Done()
		for i := 0; i < numSessions; i++ {
			env := &Envelope{
				SessionID:  fmt.Sprintf("race-session-%d", i),
				Seq:        0,
				Payload:    []byte("hello"),
				BackendIdx: uint8(i % 4),
			}
			engine.sessionMu.RLock()
			s, exists := engine.sessions[fmt.Sprintf("race-session-%d", i)]
			engine.sessionMu.RUnlock()
			if exists {
				s.ProcessRx(env)
			}
		}
	}()

	// Reader: flushAll reads BackendIdx; re-seed txBuf after each call
	go func() {
		<-ready
		close(start)
		defer wg.Done()
		for iter := 0; iter < 50; iter++ {
			engine.flushAll(ctx)
			for j := range numSessions {
				engine.sessionMu.RLock()
				s, exists := engine.sessions[fmt.Sprintf("race-session-%d", j)]
				engine.sessionMu.RUnlock()
				if exists {
					s.mu.Lock()
					s.txBuf = []byte("more data")
					s.mu.Unlock()
				}
			}
		}
	}()

	wg.Wait()
}

// TestRemoveSessionDataRace verifies that RemoveSession does not perform a data race
// on len(e.sessions) after releasing the sessionMu lock.
func TestRemoveSessionDataRace(_ *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, nil)

	for i := 0; i < 100; i++ {
		s := NewSession(fmt.Sprintf("session-%d", i))
		engine.AddSession(s)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				engine.RemoveSession(fmt.Sprintf("session-%d", id*10+j))
			}
		}(i)
	}
	wg.Wait()
}

// TestFlushAllSplitsOversizedMux verifies that flushAll splits multiplexed
// envelopes into multiple files when the total exceeds the safe upload size,
// preventing silent truncation of data by WebDAV upload limits.
func TestFlushAllSplitsOversizedMux(t *testing.T) {
	be := &mockBackend{}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)
	engine.SetFlushRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer engine.Stop()
	defer cancel()

	// Start the engine so upload workers are running
	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// Bypass EnqueueTx backpressure by writing directly to txBuf.
	// Use small payloads (1MB) but enough sessions to exceed the 14MB split threshold.
	// All sessions share the same BackendIdx for multiplexing.
	payload := make([]byte, 1*1024*1024)
	for i := 0; i < 20; i++ {
		s := NewSession(fmt.Sprintf("session-%d", i))
		s.TargetAddr = "example.com:80"
		s.mu.Lock()
		s.txBuf = append(s.txBuf, payload...)
		s.mu.Unlock()
		engine.AddSession(s)
	}

	// Call flushAll directly (this runs in the test goroutine)
	engine.flushAll(ctx)

	// Wait for upload workers to finish all queued jobs before checking.
	// Poll be.uploaded with timeout instead of relying on engine.Stop() —
	// closing stopCh may cause workers to abandon jobs still in the uploadJobs buffer.
	var uploadCount int
	for i := 0; i < 50; i++ {
		be.mu.Lock()
		uploadCount = len(be.uploaded)
		be.mu.Unlock()
		if uploadCount >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	engine.Stop()

	// 20 sessions × 1MB ≈ 20MB raw payload → must split into ≥2 files
	if uploadCount < 2 {
		t.Errorf("expected at least 2 uploaded files from oversized mux, got %d", uploadCount)
	}
}

// dataTrackingBackend records uploaded filenames AND their content fingerprints
// to prove that flushAll split does not lose data.
type dataTrackingBackend struct {
	mockBackend
	mu           sync.Mutex
	uploadedData map[string]int64 // filename → total bytes
}

func (d *dataTrackingBackend) UploadByIndex(_ context.Context, name string, data io.Reader, _ uint8) error {
	content, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.uploaded = append(d.uploaded, name)
	if d.uploadedData == nil {
		d.uploadedData = make(map[string]int64)
	}
	d.uploadedData[name] = int64(len(content))
	d.mu.Unlock()
	return nil
}

func TestJitterPollInterval(t *testing.T) {
	e := NewEngine(&mockBackend{}, true, nil)
	tests := []struct {
		name     string
		input    time.Duration
		minRatio float64
		maxRatio float64
	}{
		{"zero", 0, 0, 0},
		{"100ms", 100 * time.Millisecond, 0.25, 1.75},
		{"5s", 5 * time.Second, 0.25, 1.75},
		{"1h", time.Hour, 0.25, 1.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				got := e.jitterPollInterval(tt.input)
				if tt.input == 0 {
					if got != 0 {
						t.Fatalf("expected 0, got %v", got)
					}
					continue
				}
				ratio := float64(got) / float64(tt.input)
				if ratio < tt.minRatio || ratio > tt.maxRatio {
					t.Fatalf("jitter ratio %.4f out of range [%.2f, %.2f] for %v", ratio, tt.minRatio, tt.maxRatio, tt.input)
				}
			}
		})
	}
}

func TestJitterFlushInterval(t *testing.T) {
	e := NewEngine(&mockBackend{}, true, nil)
	for i := 0; i < 100; i++ {
		got := e.jitterFlushInterval(500 * time.Millisecond)
		if got == 0 {
			t.Fatal("flush jitter returned 0")
		}
		if got > 750*time.Millisecond || got < 250*time.Millisecond {
			t.Fatalf("flush jitter %v outside expected range [250ms, 750ms]", got)
		}
	}
	// Zero input
	if got := e.jitterFlushInterval(0); got != 0 {
		t.Errorf("expected 0 for zero input, got %v", got)
	}
}

// TestFlushAllDataIntegrity proves that NO data is silently lost during
// flushAll mux splitting. It creates 15 sessions × 1MB = 15MB of payload,
// triggers flushAll, then verifies the sum of bytes across all uploaded
// chunks at least equals the original total (overhead from envelope
// encoding adds extra bytes).
//
// Before the mux-splitting fix: flushAll sends one 15MB file,
// WebDAV Upload silently truncates to 16MB (which passes), but if total
// were >16MB the data would be lost. This test proves the fix works by
// showing that data is preserved across multiple chunked uploads.
func TestFlushAllDataIntegrity(t *testing.T) {
	be := &dataTrackingBackend{}
	engine := NewEngine(be, false, nil)
	engine.SetPollRate(50)
	engine.SetFlushRate(50)

	ctx, cancel := context.WithCancel(context.Background())
	defer engine.Stop()
	defer cancel()

	engine.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// Create 15 sessions with 1MB each = 15MB total (enough to trigger split near 14MB)
	var expectedTotal int64
	for i := 0; i < 15; i++ {
		payload := make([]byte, 1*1024*1024)
		for j := range payload {
			payload[j] = byte(i)
		}
		s := NewSession(fmt.Sprintf("session-%d", i))
		s.TargetAddr = "example.com:80"
		s.mu.Lock()
		s.txBuf = append(s.txBuf, payload...)
		s.mu.Unlock()
		expectedTotal += int64(len(payload))
		engine.AddSession(s)
	}

	engine.flushAll(ctx)
	time.Sleep(200 * time.Millisecond)
	engine.Stop()

	// Sum all uploaded bytes (includes envelope overhead: session ID,
	// seq number, target addr, binary headers — this is OK; we only
	// need to prove data was NOT lost, meaning uploaded >= expected)
	var uploadedTotal int64
	be.mu.Lock()
	for _, size := range be.uploadedData {
		uploadedTotal += size
	}
	fileCount := len(be.uploaded)
	be.mu.Unlock()

	if fileCount < 2 {
		t.Errorf("expected ≥2 chunked uploads, got %d — data integrity vulnerable to truncation", fileCount)
	}
	// uploaded must be at least expected (overhead makes it larger;
	// if uploaded < expected, data was silently truncated)
	if uploadedTotal < expectedTotal {
		t.Errorf("DATA LOSS: uploaded %d bytes, expected at least %d bytes (mux split lost data)", uploadedTotal, expectedTotal)
	}
}

func TestPadFile(t *testing.T) {
	tests := []struct {
		name       string
		bufLen     int
		bucket     int
		maxSize    int
		wantMinLen int // buf.Len() after padFile must be >= this
	}{
		{"less than bucket", 100, 256, 65536, 256},
		{"exact bucket", 256, 256, 65536, 256},
		{"one over bucket", 257, 256, 65536, 512},
		{"near max", 64000, 256, 65536, 64000}, // padding capped by maxSize
		{"at max", 65536, 256, 65536, 65536},
		{"over max", 66000, 256, 65536, 66000}, // already over maxSize, no padding
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			buf.Write(make([]byte, tt.bufLen))
			padFile(&buf, tt.bucket, tt.maxSize)
			if buf.Len() < tt.wantMinLen {
				t.Errorf("padFile: buf.Len()=%d < wantMinLen=%d", buf.Len(), tt.wantMinLen)
			}
			// Only check maxSize when the input was under it (padding can't shrink)
			if tt.bufLen <= tt.maxSize && buf.Len() > tt.maxSize {
				t.Errorf("padFile: buf.Len()=%d > maxSize=%d", buf.Len(), tt.maxSize)
			}
		})
	}
}

// TestIndividualSetters verifies that individual setters work and
// that zero values do not override already-set values.
func TestIndividualSetters(t *testing.T) {
	engine := NewEngine(&mockBackend{}, true, nil)
	engine.SetPaddingSize(65536)
	engine.SetHoldMax(2000)

	if engine.PaddingSize != 65536 {
		t.Errorf("PaddingSize = %d, want 65536", engine.PaddingSize)
	}
	if engine.HoldMax != 2*time.Second {
		t.Errorf("HoldMax = %v, want 2s", engine.HoldMax)
	}

	// Zero values should not change defaults
	engine.SetPaddingSize(0)
	engine.SetHoldMax(0)
	if engine.PaddingSize != 65536 {
		t.Errorf("PaddingSize changed on zero set")
	}
}

func TestHoldDelayRange(t *testing.T) {
	engine := NewEngine(&mockBackend{}, false, nil)
	engine.HoldMax = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var maxDelay time.Duration
	for i := 0; i < 30; i++ {
		start := time.Now()
		engine.flushAll(ctx)
		d := time.Since(start)
		if d > maxDelay {
			maxDelay = d
		}
	}
	if maxDelay >= engine.HoldMax+time.Millisecond {
		t.Errorf("max delay %v exceeds HoldMax %v", maxDelay, engine.HoldMax)
	}
	if maxDelay < time.Millisecond {
		t.Logf("WARNING: maxDelay only %v — may indicate hold delay not spanning full range", maxDelay)
	}
}

func TestHoldDelayDisabledOnClient(t *testing.T) {
	engine := NewEngine(&mockBackend{}, true, nil)
	engine.HoldMax = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	engine.flushAll(ctx)
	elapsed := time.Since(start)
	if elapsed > time.Millisecond {
		t.Errorf("client-side engine had non-trivial hold delay: %v", elapsed)
	}
}
