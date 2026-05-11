// flowdav-encrypt reads JSON from stdin, optionally injects encryption keys,
// and writes an encrypted config to stdout.
//
// Usage:
//   FLOWDAV_PASSWORD=secret flowdav-encrypt --gen-keys < config.json > config.enc
//
// Environment:
//   FLOWDAV_PASSWORD  — master password (required)

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lyafence/flowdav/internal/config"
)

var version = "dev"

func main() {
	genKeys := flag.Bool("gen-keys", false, "Generate enc_key and hmac_key if missing")
	showVersion := flag.Bool("version", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Println("flowdav-encrypt", version)
		os.Exit(0)
	}

	password := os.Getenv("FLOWDAV_PASSWORD")
	if password == "" {
		os.Stderr.WriteString("FLOWDAV_PASSWORD environment variable is required\n")
		os.Exit(1)
	}

	plaintext, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Stderr.WriteString("failed to read stdin: " + err.Error() + "\n")
		os.Exit(1)
	}

	if *genKeys {
		plaintext = injectKeys(plaintext)
	}

	encrypted, err := config.EncryptConfig(plaintext, password)
	if err != nil {
		os.Stderr.WriteString("encryption failed: " + err.Error() + "\n")
		os.Exit(1)
	}

	data := config.MarshalEncrypted(encrypted)
	if _, err := os.Stdout.Write(data); err != nil {
		os.Stderr.WriteString("write failed: " + err.Error() + "\n")
		os.Exit(1)
	}
}

type rawConfig map[string]any

func injectKeys(plaintext []byte) []byte {
	var cfg rawConfig
	if err := json.Unmarshal(plaintext, &cfg); err != nil {
		return plaintext
	}

	if _, hasKey := cfg["enc_key"]; !hasKey {
		cfg["enc_key"] = base64.StdEncoding.EncodeToString(randBytes(32))
	}
	if _, hasKey := cfg["hmac_key"]; !hasKey {
		cfg["hmac_key"] = base64.StdEncoding.EncodeToString(randBytes(32))
	}

	result, _ := json.Marshal(cfg)
	return result
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
}
