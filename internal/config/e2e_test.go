package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEncryptedConfigEndToEnd verifies:
// 1. Encrypted config detected without -p
// 2. Wrong password rejected
// 3. Correct password decrypts and engine initializes
// 4. FLOWDAV_PASSWORD env var works
func TestEncryptedConfigEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}

	root := findProjectRoot(t)
	binDir := t.TempDir()
	serverBin := filepath.Join(binDir, "flowdav-server")
	clientBin := filepath.Join(binDir, "flowdav-client")

	for _, c := range []struct{ src, out string }{
		{filepath.Join(root, "cmd", "server"), serverBin},
		{filepath.Join(root, "cmd", "client"), clientBin},
	} {
		build := exec.Command("go", "build", "-o", c.out, c.src)
		build.Dir = root
		out, err := build.CombinedOutput()
		require.NoError(t, err, "build %s: %s", c.src, out)
	}

	password := "test-master-password-123"
	cfg := &AppConfig{
		ListenAddr:  "0.0.0.0:11080",
		StorageType: "webdav",
		WebDAV: &WebDAVConfig{
			Provider: "custom",
			URL:      "http://127.0.0.1:18080",
			Login:    "user",
			Token:    "test",
		},
		RefreshRateMs: 500,
		FlushRateMs:   500,
		LogLevel:      "info",
		HealthPort:    "127.0.0.1:19090",
		EncKey:        "dGVzdC1rZXktMzItYnl0ZXMtMTIzNDU2Nzg5MDEyMzQ1Njc4OTA=",
		HMacKey:       "dGVzdC1obWFjLWtleS0zMi1ieXRlcy0xMjM0NTY3ODkwMTIzNDU2Nzg5MA==",
	}
	plaintext, err := json.Marshal(cfg)
	require.NoError(t, err)
	enc, err := EncryptConfig(plaintext, password)
	require.NoError(t, err)

	encFile := filepath.Join(binDir, "server.enc")
	require.NoError(t, os.WriteFile(encFile, MarshalEncrypted(enc), 0600))

	// Helper: run binary with context deadline, return output
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, serverBin, args...)
		cmd.Dir = binDir
		out, err := cmd.CombinedOutput()
		if err != nil && ctx.Err() != nil {
			return string(out), nil // timed out = process ran until timeout (expected for long-lived)
		}
		return string(out), err
	}
	t.Run("detects encrypted config", func(t *testing.T) {
		out, _ := run("-c", encFile)
		if !ciFold(out, "encrypted") {
			t.Fatalf("expected 'encrypted', got: %s", out)
		}
	})

	t.Run("wrong password rejected", func(t *testing.T) {
		out, err := run("-c", encFile, "-p", "wrong-password")
		if err == nil {
			t.Fatalf("expected error, got output: %s", out)
		}
		if !ciFold(out, "password") && !ciFold(out, "corrupted") {
			t.Fatalf("expected password/corrupted error, got: %s", out)
		}
	})

	t.Run("correct password reaches engine initialization", func(t *testing.T) {
		out, _ := run("-c", encFile, "-p", password)
		if !ciFold(out, "start") && !ciFold(out, "poll") && !ciFold(out, "failed") {
			t.Fatalf("expected engine startup messages, got: %s", out)
		}
	})

	t.Run("FLOWDAV_PASSWORD env var works", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, clientBin, "-c", encFile)
		cmd.Dir = binDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("FLOWDAV_PASSWORD=%s", password))
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		cmd.Run() // ignore error, process gets killed by timeout
		output := out.String()
		if !ciFold(output, "start") && !ciFold(output, "listening") && !ciFold(output, "poll") {
			t.Fatalf("expected startup via env, got: %s", output)
		}
	})
}

func ciFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
