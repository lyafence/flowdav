package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockBackend is a mock implementation of the Backend interface for testing.
type mockBackend struct {
	mock.Mock
	UploadFunc          func(ctx context.Context, filename string, data io.Reader) error
	DownloadFunc        func(ctx context.Context, filename string) (io.ReadCloser, error)
	ListQueryFunc       func(ctx context.Context, prefix string) ([]FileEntry, error)
	DeleteFunc          func(ctx context.Context, filename string) error
	LoginFunc           func(ctx context.Context) error
	UploadByIndexFunc   func(ctx context.Context, filename string, data io.Reader, idx uint8) error
	DownloadByIndexFunc func(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error)
	UploadAnyFunc       func(ctx context.Context, filename string, data io.Reader) (uint8, error)
}

func (m *mockBackend) Upload(ctx context.Context, filename string, data io.Reader) error {
	if m.UploadFunc != nil {
		return m.UploadFunc(ctx, filename, data)
	}
	args := m.Called(ctx, filename, data)
	return args.Error(0)
}

func (m *mockBackend) Download(ctx context.Context, filename string) (io.ReadCloser, error) {
	if m.DownloadFunc != nil {
		return m.DownloadFunc(ctx, filename)
	}
	args := m.Called(ctx, filename)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *mockBackend) ListQuery(ctx context.Context, prefix string) ([]FileEntry, error) {
	if m.ListQueryFunc != nil {
		return m.ListQueryFunc(ctx, prefix)
	}
	args := m.Called(ctx, prefix)
	return args.Get(0).([]FileEntry), args.Error(1)
}

func (m *mockBackend) Delete(ctx context.Context, filename string) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, filename)
	}
	args := m.Called(ctx, filename)
	return args.Error(0)
}

func (m *mockBackend) Login(ctx context.Context) error {
	if m.LoginFunc != nil {
		return m.LoginFunc(ctx)
	}
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockBackend) UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error {
	if m.UploadByIndexFunc != nil {
		return m.UploadByIndexFunc(ctx, filename, data, idx)
	}
	args := m.Called(ctx, filename, data, idx)
	return args.Error(0)
}

func (m *mockBackend) DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error) {
	if m.DownloadByIndexFunc != nil {
		return m.DownloadByIndexFunc(ctx, filename, idx)
	}
	args := m.Called(ctx, filename, idx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.ReadCloser), args.Error(1)
}

func (m *mockBackend) UploadAny(ctx context.Context, filename string, data io.Reader) (uint8, error) {
	if m.UploadAnyFunc != nil {
		return m.UploadAnyFunc(ctx, filename, data)
	}
	args := m.Called(ctx, filename, data)
	return uint8(args.Int(0)), args.Error(1)
}

func TestMultiBackend(t *testing.T) {
	// Create three mock backends.
	mock1 := &mockBackend{}
	mock2 := &mockBackend{}
	mock3 := &mockBackend{}

	mock1.ListQueryFunc = func(_ context.Context, _ string) ([]FileEntry, error) {
		return []FileEntry{{Filename: "file1.txt"}}, nil
	}
	mock2.ListQueryFunc = func(_ context.Context, _ string) ([]FileEntry, error) {
		return []FileEntry{{Filename: "file2.txt"}}, nil
	}
	mock3.ListQueryFunc = func(_ context.Context, _ string) ([]FileEntry, error) {
		return []FileEntry{{Filename: "file3.txt"}}, nil
	}

	// Create a MultiBackend with the three mocks.
	multi := NewMultiBackend([]Backend{mock1, mock2, mock3})

	// Test NumBackends.
	require.Equal(t, 3, multi.NumBackends(), "NumBackends should return the number of backends")

	// Test round-robin: first three calls should return mock1, mock2, mock3, then wrap.
	multi.mu.Lock()
	first, _ := multi.nextAvailableBackend()
	second, _ := multi.nextAvailableBackend()
	third, _ := multi.nextAvailableBackend()
	fourth, _ := multi.nextAvailableBackend()
	multi.mu.Unlock()

	require.Equal(t, mock1, first, "First backend should return mock1")
	require.Equal(t, mock2, second, "Second backend should return mock2")
	require.Equal(t, mock3, third, "Third backend should return mock3")
	require.Equal(t, mock1, fourth, "Fourth backend should wrap to mock1")

	// Test backendByIndexLocked: index 0 -> mock1, 1 -> mock2, 2 -> mock3, 3 -> mock0 (wrap), etc.
	multi.mu.Lock()
	b0 := multi.backendByIndexLocked(0)
	b1 := multi.backendByIndexLocked(1)
	b2 := multi.backendByIndexLocked(2)
	b3 := multi.backendByIndexLocked(3)
	b4 := multi.backendByIndexLocked(4)
	multi.mu.Unlock()

	require.Equal(t, mock1, b0, "backendByIndexLocked(0) should return mock1")
	require.Equal(t, mock2, b1, "backendByIndexLocked(1) should return mock2")
	require.Equal(t, mock3, b2, "backendByIndexLocked(2) should return mock3")
	require.Equal(t, mock1, b3, "backendByIndexLocked(3) should wrap to mock1")
	require.Equal(t, mock2, b4, "backendByIndexLocked(4) should wrap to mock2")

	// Test Login: should call Login on all backends.
	var loginCalls1, loginCalls2, loginCalls3 int
	mock1.LoginFunc = func(_ context.Context) error { loginCalls1++; return nil }
	mock2.LoginFunc = func(_ context.Context) error { loginCalls2++; return nil }
	mock3.LoginFunc = func(_ context.Context) error { loginCalls3++; return nil }
	err := multi.Login(context.Background())
	require.NoError(t, err, "Login should not error")
	require.Equal(t, 1, loginCalls1, "Login must be called on backend 1")
	require.Equal(t, 1, loginCalls2, "Login must be called on backend 2")
	require.Equal(t, 1, loginCalls3, "Login must be called on backend 3")

	// Test Upload: should use RoundRobinBackend and call Upload on the selected backend.
	var uploadCalls1, uploadCalls2, uploadCalls3 int
	mock1.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadCalls1++; return nil }
	mock2.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadCalls2++; return nil }
	mock3.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadCalls3++; return nil }
	// Reset the round-robin counter by creating a new multi-backend for a known state.
	multi2 := NewMultiBackend([]Backend{mock1, mock2, mock3})
	err = multi2.Upload(context.Background(), "test.txt", nil)
	require.NoError(t, err, "Upload should not error")
	// The first upload must go to the first available backend (mock1), nowhere else.
	require.Equal(t, 1, uploadCalls1, "round-robin should pick backend 1 first")
	require.Zero(t, uploadCalls2, "upload must not go to backend 2")
	require.Zero(t, uploadCalls3, "upload must not go to backend 3")

	// Test UploadByIndex: should call Upload on the backend at the given index.
	var uploadByIndexCalls1, uploadByIndexCalls2, uploadByIndexCalls3 int
	mock1.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadByIndexCalls1++; return nil }
	mock2.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadByIndexCalls2++; return nil }
	mock3.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error { uploadByIndexCalls3++; return nil }
	err = multi.UploadByIndex(context.Background(), "test.txt", nil, 1)
	require.NoError(t, err, "UploadByIndex(1) should not error")
	require.Zero(t, uploadByIndexCalls1, "UploadByIndex(1) must not touch backend 1")
	require.Equal(t, 1, uploadByIndexCalls2, "UploadByIndex(1) must hit backend 2")
	require.Zero(t, uploadByIndexCalls3, "UploadByIndex(1) must not touch backend 3")

	// Test DownloadByIndex: should call Download on the backend at the given index.
	var downloadCalls1, downloadCalls2, downloadCalls3 int
	mock1.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		downloadCalls1++
		return io.NopCloser(nil), nil
	}
	mock2.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		downloadCalls2++
		return io.NopCloser(nil), nil
	}
	mock3.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		downloadCalls3++
		return io.NopCloser(nil), nil
	}
	_, err = multi.DownloadByIndex(context.Background(), "test.txt", 0)
	require.NoError(t, err, "DownloadByIndex(0) should not error")
	require.Equal(t, 1, downloadCalls1, "DownloadByIndex(0) must hit backend 1")
	require.Zero(t, downloadCalls2, "DownloadByIndex(0) must not touch backend 2")
	require.Zero(t, downloadCalls3, "DownloadByIndex(0) must not touch backend 3")

	// Test ListQuery: should return files from all backends with correct BackendIdx.
	files, err := multi.ListQuery(context.Background(), "")
	require.NoError(t, err, "ListQuery should not error")
	require.Len(t, files, 3, "ListQuery should return 3 files (one from each backend)")
	// Check that the files have the correct BackendIdx and names.
	// We don't know the order, so we'll check by filename and index.
	fileMap := make(map[string]uint8)
	for _, f := range files {
		fileMap[f.Filename] = f.BackendIdx
	}
	require.Equal(t, uint8(0), fileMap["file1.txt"], "file1.txt should have BackendIdx 0")
	require.Equal(t, uint8(1), fileMap["file2.txt"], "file2.txt should have BackendIdx 1")
	require.Equal(t, uint8(2), fileMap["file3.txt"], "file3.txt should have BackendIdx 2")

	// Test Download: should try each backend until one succeeds.
	// We'll set up mock1 to fail, mock2 to succeed, and mock3 to not be called.
	mock1.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		// Since we don't have a predefined ErrNotExist in storage, we'll use a generic error and check that it tries the next.
		return nil, errors.New("not found")
	}
	mock2.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		return io.NopCloser(nil), nil // success
	}
	mock3.DownloadFunc = func(_ context.Context, _ string) (io.ReadCloser, error) {
		// This should not be called.
		t.Error("mock2.DownloadFunc should not be called if mock2 succeeds")
		return nil, errors.New("should not be called")
	}
	rc, err := multi.Download(context.Background(), "test.txt")
	require.NoError(t, err, "Download should succeed by trying the next backend")
	require.NotNil(t, rc, "Download should return a non-nil ReadCloser")
	_ = rc.Close()

	// Test Delete: should call Delete on all backends.
	mock1.DeleteFunc = func(_ context.Context, _ string) error { return nil }
	mock2.DeleteFunc = func(_ context.Context, _ string) error { return nil }
	mock3.DeleteFunc = func(_ context.Context, _ string) error { return nil }
	err = multi.Delete(context.Background(), "test.txt")
	require.NoError(t, err, "Delete should not error")
}

// TestMultiBackendDeleteReturnsError verifies that Delete propagates errors
// when at least one backend fails.
func TestMultiBackendDeleteReturnsError(t *testing.T) {
	mock1 := &mockBackend{}
	mock2 := &mockBackend{}
	mock1.DeleteFunc = func(_ context.Context, _ string) error {
		return errors.New("delete failed on backend 1")
	}
	mock2.DeleteFunc = func(_ context.Context, _ string) error {
		return nil
	}

	multi := NewMultiBackend([]Backend{mock1, mock2})
	err := multi.Delete(context.Background(), "test.bin")
	if err == nil {
		t.Error("MultiBackend.Delete should return error when at least one backend fails")
	}
}

// Regression: errors.Join — all backends failing must return non-nil.
func TestMultiBackendDeleteAllAdversarial(t *testing.T) {
	mock1 := &mockBackend{}
	mock2 := &mockBackend{}
	mock3 := &mockBackend{}
	mock1.DeleteFunc = func(_ context.Context, _ string) error {
		return errors.New("backend 1 timeout")
	}
	mock2.DeleteFunc = func(_ context.Context, _ string) error {
		return errors.New("backend 2 timeout")
	}
	mock3.DeleteFunc = func(_ context.Context, _ string) error {
		return errors.New("backend 3 timeout")
	}

	multi := NewMultiBackend([]Backend{mock1, mock2, mock3})
	err := multi.Delete(context.Background(), "critical.bin")
	if err == nil {
		t.Error("MultiBackend.Delete must return error when ALL backends fail")
	}
	// The error must contain all three failure messages
	if !strings.Contains(err.Error(), "backend 1 timeout") ||
		!strings.Contains(err.Error(), "backend 2 timeout") ||
		!strings.Contains(err.Error(), "backend 3 timeout") {
		t.Errorf("Delete error should aggregate all backend failures, got: %v", err)
	}
}

// TestIsAvailableNoSideEffect verifies isAvailable is a pure query (no mutation).
func TestIsAvailableNoSideEffect(t *testing.T) {
	m := NewMultiBackend([]Backend{&mockBackend{}})

	m.mu.Lock()
	m.health[0].failures = cbThreshold
	m.health[0].lastFail = time.Now().Add(-cbCooldown * 2) // cooldown well past
	expectedFailures := m.health[0].failures
	m.mu.Unlock()

	m.mu.Lock()
	ok, expired := m.isAvailable(0)
	failuresAfter := m.health[0].failures
	m.mu.Unlock()

	if !ok {
		t.Error("isAvailable should return true after cooldown")
	}
	if !expired {
		t.Error("isAvailable should report cooldown expired")
	}
	if failuresAfter != expectedFailures {
		t.Errorf("isAvailable mutated health.failures: got %d, want %d (query method must have no side effects)", failuresAfter, expectedFailures)
	}
}

func TestCircuitBreakerTripsOnFailures(t *testing.T) {
	mock1 := &mockBackend{}
	mock2 := &mockBackend{}
	multi := NewMultiBackend([]Backend{mock1, mock2})
	ctx := context.Background()

	// Mock1 always fails; mock2 always succeeds
	failCount := 0
	mock1.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error {
		failCount++
		return errors.New("upload failed")
	}
	mock2.UploadFunc = func(_ context.Context, _ string, _ io.Reader) error {
		return nil
	}

	// Trip circuit on backend 0 with cbThreshold failures
	for i := 0; i < cbThreshold; i++ {
		err := multi.UploadByIndex(ctx, "test.bin", nil, 0)
		require.Error(t, err)
	}

	// Backend 0 should now be unavailable
	multi.mu.Lock()
	b0 := multi.backendByIndexLocked(0)
	multi.mu.Unlock()
	require.Nil(t, b0, "backend 0 should be tripped")

	// Backend 1 unaffected
	multi.mu.Lock()
	b1 := multi.backendByIndexLocked(1)
	multi.mu.Unlock()
	require.NotNil(t, b1, "backend 1 should still be available")
}

func TestCryptoRandInt(t *testing.T) {
	if idx := CryptoRandInt(0); idx != 0 {
		t.Errorf("expected 0 for n=0, got %d", idx)
	}
	if idx := CryptoRandInt(-1); idx != 0 {
		t.Errorf("expected 0 for n=-1, got %d", idx)
	}
	for n := 1; n <= 10; n++ {
		seen := make(map[int]int)
		const samples = 5000
		for i := 0; i < samples; i++ {
			seen[CryptoRandInt(n)]++
		}
		if len(seen) != n {
			t.Errorf("n=%d: expected %d distinct values, got %d", n, n, len(seen))
		}
		for idx := 0; idx < n; idx++ {
			if seen[idx] == 0 {
				t.Errorf("n=%d: index %d never produced in %d samples", n, idx, samples)
			}
		}
	}
}

func TestMultiBackend429RateLimit(t *testing.T) {
	mock1 := &mockBackend{}
	multi := NewMultiBackend([]Backend{mock1})

	err429 := &HTTPError{Code: http.StatusTooManyRequests, Err: fmt.Errorf("rate limited")}
	multi.mu.Lock()
	multi.recordFailure(0, err429)
	multi.mu.Unlock()

	multi.mu.Lock()
	ok, expired := multi.isAvailable(0)
	failures := multi.health[0].failures
	multi.mu.Unlock()

	assert.False(t, ok, "backend should be unavailable due to rate limit")
	assert.False(t, expired, "rate limit should not report cooldown expired")
	assert.Equal(t, 0, failures, "429 must NOT increment failure count")

	multi.mu.Lock()
	multi.health[0].rateLimitedUntil = time.Now().Add(-time.Second)
	multi.mu.Unlock()

	multi.mu.Lock()
	ok, expired = multi.isAvailable(0)
	failures = multi.health[0].failures
	multi.mu.Unlock()

	assert.True(t, ok, "backend should recover after rate-limit cooldown")
	assert.False(t, expired, "no cooldown expiry flag when there were no circuit breaker failures")
	assert.Equal(t, 0, failures, "failures must still be 0 after rate limit")

	multi.mu.Lock()
	multi.recordFailure(0, errors.New("connection timeout"))
	multi.mu.Unlock()

	multi.mu.Lock()
	failures = multi.health[0].failures
	multi.mu.Unlock()
	assert.Equal(t, 1, failures, "non-429 must increment failure count")
}

func TestListQueryAllBackendsFail(t *testing.T) {
	be1 := &mockBackend{ListQueryFunc: func(_ context.Context, _ string) ([]FileEntry, error) {
		return nil, errors.New("connection refused")
	}}
	be2 := &mockBackend{ListQueryFunc: func(_ context.Context, _ string) ([]FileEntry, error) {
		return nil, errors.New("connection refused")
	}}
	multi := NewMultiBackend([]Backend{be1, be2})

	files, err := multi.ListQuery(context.Background(), "")
	require.Error(t, err, "ListQuery must fail when every backend fails")
	require.Nil(t, files)
	assert.ErrorContains(t, err, "all 2 backends failed")
}

func TestListQueryPartialFailureReturnsAvailableFiles(t *testing.T) {
	be1 := &mockBackend{ListQueryFunc: func(_ context.Context, _ string) ([]FileEntry, error) {
		return nil, errors.New("backend down")
	}}
	be2 := &mockBackend{ListQueryFunc: func(_ context.Context, _ string) ([]FileEntry, error) {
		return []FileEntry{{Filename: "aa11", ModTime: time.Now()}}, nil
	}}
	multi := NewMultiBackend([]Backend{be1, be2})

	files, err := multi.ListQuery(context.Background(), "")
	require.NoError(t, err, "partial failure is not an error")
	require.Len(t, files, 1)
	assert.Equal(t, uint8(1), files[0].BackendIdx)
}
