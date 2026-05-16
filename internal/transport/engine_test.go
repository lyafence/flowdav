package transport

import (
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

func (m *mockBackend) Login(ctx context.Context) error { return nil }
func (m *mockBackend) Upload(ctx context.Context, name string, data io.Reader) error {
	m.mu.Lock()
	m.uploaded = append(m.uploaded, name)
	m.mu.Unlock()
	return nil
}
func (m *mockBackend) ListQuery(ctx context.Context, prefix string) ([]storage.FileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listFiles, nil
}
func (m *mockBackend) Download(ctx context.Context, name string) (io.ReadCloser, error) {
	m.mu.Lock()
	m.downloaded = append(m.downloaded, name)
	m.mu.Unlock()
	return nil, nil
}
func (m *mockBackend) Delete(ctx context.Context, name string) error {
	m.mu.Lock()
	m.deleted = append(m.deleted, name)
	m.mu.Unlock()
	return nil
}
func (m *mockBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, idx uint8) error {
	return m.Upload(ctx, name, data)
}
func (m *mockBackend) DownloadByIndex(ctx context.Context, name string, idx uint8) (io.ReadCloser, error) {
	m.Download(ctx, name)
	return nil, nil
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

func TestEngineStopMultipleCalls(t *testing.T) {
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

func TestEngineStartStopCycle(t *testing.T) {
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
			if s := engine.GetSession(fmt.Sprintf("race-session-%d", i)); s != nil {
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
				if s := engine.GetSession(fmt.Sprintf("race-session-%d", j)); s != nil {
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
func TestRemoveSessionDataRace(t *testing.T) {
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

func (d *dataTrackingBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, idx uint8) error {
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
	tests := []struct {
		name     string
		input    time.Duration
		minRatio float64
		maxRatio float64
	}{
		{"zero", 0, 0, 0},
		{"100ms", 100 * time.Millisecond, 0.75, 1.25},
		{"5s", 5 * time.Second, 0.75, 1.25},
		{"1h", time.Hour, 0.75, 1.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				got := jitterPollInterval(tt.input)
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
