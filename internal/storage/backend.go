package storage

import (
	"context"
	"io"
	"time"
)

// FileEntry represents a file discovered in a storage backend, tagged with its origin backend index.
type FileEntry struct {
	Filename   string
	BackendIdx uint8
	ModTime    time.Time
}

// Backend defines the interface for our pluggable storage mechanism that acts as the
// covert transport layer.
type Backend interface {
	// Login performs any necessary authentication.
	Login(ctx context.Context) error

	// Upload writes a new file to the storage backend.
	// filename is typically of the format {dir_byte}{16_hex}
	Upload(ctx context.Context, filename string, data io.Reader) error

	// ListQuery searches the backend for files matching a specific prefix or criteria.
	// We use this to discover new request or response payloads.
	ListQuery(ctx context.Context, prefix string) ([]FileEntry, error)

	// Download returns an io.ReadCloser for the file content from the backend.
	Download(ctx context.Context, filename string) (io.ReadCloser, error)

	// Delete removes a file from the backend after it has been read or expired.
	Delete(ctx context.Context, filename string) error

	// UploadByIndex uploads to a specific backend by index (for multi-backend mode).
	UploadByIndex(ctx context.Context, filename string, data io.Reader, idx uint8) error

	// DownloadByIndex downloads from a specific backend by index (for multi-backend mode).
	DownloadByIndex(ctx context.Context, filename string, idx uint8) (io.ReadCloser, error)

	// UploadAny uploads to the next available backend and returns the chosen index.
	// Single-backend implementations return idx=0.
	UploadAny(ctx context.Context, filename string, data io.Reader) (uint8, error)
}
