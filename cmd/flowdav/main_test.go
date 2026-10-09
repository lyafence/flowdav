package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFile0600TightensExistingFile verifies that writing a config over an
// existing world-readable file tightens its permissions. os.WriteFile alone
// only applies the mode at creation, which could leave plaintext key material
// readable by other local users.
func TestWriteFile0600TightensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile0600(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("writeFile0600 left permissions %o, want 0600", perm)
	}
}
