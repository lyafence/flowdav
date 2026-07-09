package storage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type redirectBackend struct {
	status    int
	loc       string
	redirects int
}

func (rb *redirectBackend) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if rb.loc != "" && rb.redirects < 1 {
		rb.redirects++
		w.Header().Set("Location", rb.loc)
		w.WriteHeader(rb.status)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestRedirectGuardSameOrigin(t *testing.T) {
	rb := &redirectBackend{status: http.StatusFound, loc: "/new-path"}
	srv := httptest.NewServer(rb)
	defer srv.Close()

	tr := &redirectGuardTransport{inner: http.DefaultTransport}
	client := srv.Client()
	client.Transport = tr

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("same-origin redirect blocked: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func TestRedirectGuardCrossHost(t *testing.T) {
	rb := &redirectBackend{status: http.StatusFound, loc: "https://evil.example.com/steal"}
	srv := httptest.NewServer(rb)
	defer srv.Close()

	tr := &redirectGuardTransport{inner: http.DefaultTransport}
	client := srv.Client()
	client.Transport = tr

	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error for cross-host redirect")
	}
}

func TestRedirectGuardCrossScheme(t *testing.T) {
	rb := &redirectBackend{status: http.StatusFound, loc: "http://example.com/path"}
	srv := httptest.NewServer(rb)
	defer srv.Close()

	tr := &redirectGuardTransport{inner: http.DefaultTransport}
	client := srv.Client()
	client.Transport = tr

	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error for cross-scheme redirect")
	}
}

func TestRedirectGuardPrivateIP(t *testing.T) {
	rb := &redirectBackend{status: http.StatusFound, loc: "http://169.254.169.254/latest/meta-data/"}
	srv := httptest.NewServer(rb)
	defer srv.Close()

	tr := &redirectGuardTransport{inner: http.DefaultTransport}
	client := srv.Client()
	client.Transport = tr

	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error for private IP redirect")
	}
}
