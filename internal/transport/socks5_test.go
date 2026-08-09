package transport

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"github.com/lyafence/flowdav/internal/storage"
)

func TestGenerateSessionID(t *testing.T) {
	id, err := generateSessionID()
	if err != nil {
		t.Fatalf("generateSessionID error: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("expected 32 hex chars, got %d (%q)", len(id), id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("session ID is not valid hex: %v", err)
	}

	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id, err := generateSessionID()
		if err != nil {
			t.Fatalf("generateSessionID error: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate session ID %q across iterations", id)
		}
		seen[id] = true
	}
}

func TestRawResolverReturnsNilIP(t *testing.T) {
	r := rawResolver{}
	ctx, ip, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx == nil {
		t.Error("expected non-nil context")
	}
	if ip != nil {
		t.Errorf("expected nil IP (avoid local DNS resolution), got %v", ip)
	}
}

// TestSocks5ConnectAddsSession verifies the dial handler creates a session,
// assigns a backend index, and the SOCKS5 handshake succeeds.
func TestSocks5ConnectAddsSession(t *testing.T) {
	engine := NewEngine(&mockBackend{}, true, nil)

	mb := storage.NewMultiBackend([]storage.Backend{
		&mockBackend{},
		&mockBackend{},
	})

	opts, err := NewSocks5Options(Socks5Config{
		ListenAddr:   "127.0.0.1:0",
		MaxConns:     10,
		Engine:       engine,
		MultiBackend: mb,
		LogFn:        func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewSocks5Options error: %v", err)
	}

	srv := socks5.NewServer(opts...)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer ln.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = srv.Serve(ln)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial socks5 server error: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// Method negotiation: no auth
	if _, err := conn.Write([]byte{
		statute.VersionSocks5, 1, statute.MethodNoAuth,
	}); err != nil {
		t.Fatalf("write method negotiation: %v", err)
	}
	methodResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodResp); err != nil {
		t.Fatalf("read method response: %v", err)
	}

	// CONNECT request to a placeholder target
	req := &statute.Request{
		Version:  statute.VersionSocks5,
		Command:  statute.CommandConnect,
		Reserved: 0,
		DstAddr: statute.AddrSpec{
			IP:       net.ParseIP("10.255.255.1"),
			Port:     443,
			AddrType: statute.ATYPIPv4,
		},
	}
	if _, err := conn.Write(req.Bytes()); err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	respHead := make([]byte, 4)
	if _, err := io.ReadFull(conn, respHead); err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if respHead[1] != statute.RepSuccess {
		t.Fatalf("expected RepSuccess (0x00), got 0x%02x", respHead[1])
	}

	// The dial handler must have registered exactly one session with a valid backend index.
	engine.sessionMu.RLock()
	sessions := make([]*Session, 0, len(engine.sessions))
	for _, s := range engine.sessions {
		sessions = append(sessions, s)
	}
	engine.sessionMu.RUnlock()

	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after handshake, got %d", len(sessions))
	}
	s := sessions[0]
	if int(s.BackendIdx) >= mb.NumBackends() {
		t.Errorf("BackendIdx %d out of range (numBackends=%d)", s.BackendIdx, mb.NumBackends())
	}
}
