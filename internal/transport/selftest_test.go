package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/lyafence/flowdav/internal/storage"
)

// roundTripBackend is an in-memory Backend for StorageRoundTrip tests. It
// records uploads/deletes and can corrupt reads to exercise the mismatch path.
type roundTripBackend struct {
	mu       sync.Mutex
	files    map[string][]byte
	uploaded string
	deleted  string
	corrupt  bool
}

func (b *roundTripBackend) Login(context.Context) error { return nil }

func (b *roundTripBackend) Upload(_ context.Context, name string, data io.Reader) error {
	buf, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files = map[string][]byte{}
	}
	b.files[name] = buf
	b.uploaded = name
	return nil
}

func (b *roundTripBackend) ListQuery(context.Context, string) ([]storage.FileEntry, error) {
	return nil, nil
}

func (b *roundTripBackend) Download(_ context.Context, name string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.files[name]
	if !ok {
		return nil, errors.New("not found")
	}
	if b.corrupt {
		data = append([]byte("corrupted"), data...)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *roundTripBackend) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.files, name)
	b.deleted = name
	return nil
}

func (b *roundTripBackend) UploadByIndex(ctx context.Context, name string, data io.Reader, _ uint8) error {
	return b.Upload(ctx, name, data)
}

func (b *roundTripBackend) DownloadByIndex(ctx context.Context, name string, _ uint8) (io.ReadCloser, error) {
	return b.Download(ctx, name)
}

func (b *roundTripBackend) UploadAny(ctx context.Context, name string, data io.Reader) (uint8, error) {
	return 0, b.Upload(ctx, name, data)
}

func TestStorageRoundTrip(t *testing.T) {
	b := &roundTripBackend{}
	e := NewEngine(b, true, nil)

	if err := e.StorageRoundTrip(context.Background()); err != nil {
		t.Fatalf("StorageRoundTrip: %v", err)
	}

	b.mu.Lock()
	uploaded, deleted := b.uploaded, b.deleted
	b.mu.Unlock()

	if uploaded == "" {
		t.Fatal("no probe uploaded")
	}
	// Must not look like a session packet (r/s prefix) — the peer would try
	// to decrypt and delete it as a session payload.
	if strings.HasPrefix(uploaded, "r") || strings.HasPrefix(uploaded, "s") {
		t.Fatalf("probe name %q must not use an r/s session prefix", uploaded)
	}
	// No fingerprintable markers in the name.
	if strings.Contains(uploaded, "probe") || strings.Contains(uploaded, "flowdav") {
		t.Fatalf("probe name %q must not contain recognizable markers", uploaded)
	}
	if deleted != uploaded {
		t.Fatalf("probe not cleaned up: uploaded %q, deleted %q", uploaded, deleted)
	}
}

func TestStorageRoundTripMismatch(t *testing.T) {
	b := &roundTripBackend{corrupt: true}
	e := NewEngine(b, true, nil)

	if err := e.StorageRoundTrip(context.Background()); err == nil {
		t.Fatal("expected readback mismatch error")
	}

	b.mu.Lock()
	uploaded, deleted := b.uploaded, b.deleted
	b.mu.Unlock()
	if uploaded == "" || deleted != uploaded {
		t.Fatalf("probe must still be cleaned up on failure: uploaded %q deleted %q", uploaded, deleted)
	}
}
