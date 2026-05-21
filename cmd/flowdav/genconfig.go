package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/term"
)

func prompt(label, defaultVal string) string {
	dflt := ""
	if defaultVal != "" {
		dflt = " [" + defaultVal + "]"
	}
	fmt.Fprintf(os.Stderr, "%s%s: ", label, dflt)
	var s string
	n, _ := fmt.Scanln(&s)
	if n == 0 || s == "" {
		return defaultVal
	}
	return s
}

func promptSecret(label string) string {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", label, err)
		os.Exit(1)
	}
	return string(pass)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
}

func generateConfig() []byte {
	url := prompt("WebDAV URL", "https://webdav.example.com:8080")
	login := prompt("WebDAV login", "username")
	token := promptSecret("WebDAV token")

	encKey := base64.StdEncoding.EncodeToString(randBytes(32))
	hmacKey := base64.StdEncoding.EncodeToString(randBytes(32))

	cfg := map[string]any{
		"listen_addr": "127.0.0.1:1080",
		"webdav": map[string]any{
			"url":       url,
			"login":     login,
			"token":     token,
			"base_path": "data_sync",
		},
		"enc_key":  encKey,
		"hmac_key": hmacKey,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return data
}
