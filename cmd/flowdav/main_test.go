package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestImportQRURI verifies the flowdav:// import URI round-trips encrypted
// config bytes with raw (unpadded) URL-safe base64, matching the decoder in
// MainActivity.importConfigText (android.util.Base64.URL_SAFE | NO_PADDING).
func TestImportQRURI(t *testing.T) {
	data := []byte{0xFF, 0x00, 0x10, 0x7F, 0x3D, 0x2B}
	uri := importQRURI(data)
	const prefix = "flowdav://import?data="
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("unexpected URI: %s", uri)
	}
	dec, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatalf("payload is not valid raw url-safe base64: %v", err)
	}
	if !bytes.Equal(dec, data) {
		t.Fatalf("roundtrip mismatch: %x != %x", dec, data)
	}
}

// TestRunEncryptRemoveSource verifies --remove-source deletes the plaintext
// config only after the encrypted copy was written successfully.
func TestRunEncryptRemoveSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := `{"enc_key":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) +
		`","hmac_key":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)) +
		`","webdav":{"url":"http://example.com"}}`
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}

	runEncrypt(path, "secret", true, false)

	if _, err := os.Stat(path + ".enc"); err != nil {
		t.Fatalf("encrypted file missing: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plaintext config still present after --remove-source: %v", err)
	}
}

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

// TestREADMEFlagOrder pins the README/CLI contract: flags land BEFORE the
// config path — flag.Parse stops at the first positional argument, so flags
// after the path are silently dropped. Covers fenced command lines (leading
// "./flowdav") and inline-code prose examples (backtick spans).
func TestREADMEFlagOrder(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	// -c/-s swallow the terminal config path as their value.
	pathFlags := map[string]bool{"-c": true, "-s": true, "--client": true, "--server": true}
	// -l/-p/-log/-password take plain values, parsing continues.
	argFlags := map[string]bool{"-l": true, "-p": true, "--log": true, "--password": true}

	type example struct {
		line int
		raw  string
		cmd  string
	}
	var examples []example
	// Inline-code spans cover prose examples: `./flowdav --qr -e ...`.
	inlineRe := regexp.MustCompile("`([^`]+)`")
	for lineNo, raw := range strings.Split(string(b), "\n") {
		line := lineNo + 1
		if strings.HasPrefix(raw, "./flowdav") {
			examples = append(examples, example{line, raw, raw})
			continue
		}
		for _, m := range inlineRe.FindAllStringSubmatch(raw, -1) {
			code := m[1]
			if strings.HasPrefix(code, "./flowdav") || strings.HasPrefix(code, "flowdav ") {
				examples = append(examples, example{line, raw, code})
			}
		}
	}

	for _, ex := range examples {
		cmd := ex.cmd
		if at := strings.Index(cmd, " #"); at >= 0 {
			cmd = cmd[:at]
		}
		toks := strings.Fields(strings.TrimPrefix(cmd, "./flowdav"))

		for i := 0; i < len(toks); i++ {
			switch {
			case pathFlags[toks[i]]:
				if i+1 >= len(toks) {
					t.Errorf("README:%d: %s needs a config path", ex.line, toks[i])
					return
				}
				if i+1 != len(toks)-1 {
					t.Errorf("README:%d: %q after the config path %q is silently dropped: %s", ex.line, toks[i+2], toks[i+1], ex.raw)
				}
				return
			case argFlags[toks[i]]:
				i++ // argument value; keep walking
			case strings.HasPrefix(toks[i], "-"):
				// bool flag (or --password=value form)
			default:
				if i != len(toks)-1 {
					t.Errorf("README:%d: flag %q after the config path %q is silently dropped: %s", ex.line, toks[i+1], toks[i], ex.raw)
				}
				return
			}
		}
	}
}
