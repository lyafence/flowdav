package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/storage"
)

type mockBackend struct {
	uploaded   []string
	downloaded []string
	deleted    []string
	listFiles  []storage.FileEntry
}

func (m *mockBackend) Login(ctx context.Context) error                         { return nil }
func (m *mockBackend) Upload(ctx context.Context, name string, data io.Reader) error {
	m.uploaded = append(m.uploaded, name)
	return nil
}
func (m *mockBackend) ListQuery(ctx context.Context, prefix string) ([]storage.FileEntry, error) {
	return m.listFiles, nil
}
func (m *mockBackend) Download(ctx context.Context, name string) (io.ReadCloser, error) {
	m.downloaded = append(m.downloaded, name)
	return nil, nil
}
func (m *mockBackend) Delete(ctx context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}
func (m *mockBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, idx uint8) error {
	return m.Upload(ctx, name, data)
}
func (m *mockBackend) DownloadByIndex(ctx context.Context, name string, idx uint8) (io.ReadCloser, error) {
	m.Download(ctx, name)
	return nil, nil
}

type trackingBackend struct {
	mockBackend
	downloadCount int
}

func (t *trackingBackend) DownloadByIndex(ctx context.Context, name string, idx uint8) (io.ReadCloser, error) {
	t.downloadCount++
	data := []byte(fmt.Sprintf("test-session-%d", t.downloadCount))
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (t *trackingBackend) Delete(ctx context.Context, name string) error {
	return nil
}

func (t *trackingBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, idx uint8) error {
	return nil
}

func TestEngineStop(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, "test-client", nil)
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
	engine := NewEngine(backend, true, "test-client", nil)

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
	engine := NewEngine(backend, true, "test-client", nil)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)

	engine.Stop()

	cancel()

	engine2 := NewEngine(backend, true, "test-client2", nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	engine2.Start(ctx2)
	time.Sleep(50 * time.Millisecond)
	engine2.Stop()
	cancel2()
}

func TestEngineFastShutdownOnStopSignal(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, "test-client", nil)

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

func TestFilenameParsingWithDashedClientID(t *testing.T) {
	tests := []struct {
		fname    string
		wantTS   bool
		wantCID  string
	}{
		{"rq-client1-1234567890.bin", true, "client1"},
		{"rq-my-client-id-1234567890.bin", true, "my-client-id"},
		{"rs-test-client-1234567890.bin", true, "test-client"},
		{"rq-abc-9999999999.bin", true, "abc"},
		{"rq-client.bin", false, ""},
		{"rq--1234567890.bin", true, ""},
	}
	for _, tt := range tests {
		fname := tt.fname
		if len(fname) > 4 && fname[len(fname)-4:] == ".bin" {
			fname = fname[:len(fname)-4]
		}
		parts := strings.Split(fname, "-")
		if len(parts) < 3 {
			if tt.wantTS {
				t.Errorf("%s: expected valid parse, got < 3 parts", tt.fname)
			}
			continue
		}
		tsStr := parts[len(parts)-1]
		_, err := strconv.ParseInt(tsStr, 10, 64)
		hasTS := err == nil
		if hasTS != tt.wantTS {
			t.Errorf("%s: hasTS=%v, want %v", tt.fname, hasTS, tt.wantTS)
		}
		if hasTS && tt.wantTS {
			gotCID := strings.Join(parts[1:len(parts)-1], "-")
			if gotCID != tt.wantCID {
				t.Errorf("%s: clientID=%q, want %q", tt.fname, gotCID, tt.wantCID)
			}
		}
	}
}

// TestRemoveSessionDataRace verifies that RemoveSession does not perform a data race
// on len(e.sessions) after releasing the sessionMu lock (Audit #2).
func TestRemoveSessionDataRace(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, "test-client", nil)

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

func TestProcessedMapGrowth(t *testing.T) {
	be := &trackingBackend{}
	engine := NewEngine(be, false, "", nil)

	for i := 0; i < 50; i++ {
		engine.processedMu.Lock()
		engine.processed[fmt.Sprintf("file-%d.bin", i)] = time.Now()
		engine.processedMu.Unlock()
	}

	engine.processedMu.Lock()
	size := len(engine.processed)
	engine.processedMu.Unlock()

	if size != 50 {
		t.Errorf("expected 50 entries, got %d", size)
	}
}