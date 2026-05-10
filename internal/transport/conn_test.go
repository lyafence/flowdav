package transport

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

func TestVirtualConnWriteClosed(t *testing.T) {
	s := NewSession("test-write-closed")
	v := NewVirtualConn(s, nil)
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	n, err := v.Write([]byte("data"))
	if err != io.ErrClosedPipe {
		t.Fatalf("expected io.ErrClosedPipe, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected n=0 on closed write, got %d", n)
	}
}

func TestVirtualConnClosePreservesSessionForFlush(t *testing.T) {
	backend := &mockBackend{}
	engine := NewEngine(backend, true, "test-client", nil)
	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)
	defer engine.Stop()
	defer cancel()

	s := NewSession("test-close-preserve")
	engine.AddSession(s)
	v := NewVirtualConn(s, engine)
	v.Write([]byte("final data"))
	v.Close()

	// Session must remain in engine after Close for flushLoop to upload remaining txBuf
	if engine.GetSession("test-close-preserve") == nil {
		t.Fatal("expected session to remain in engine after Close for pending flush")
	}

	// Verify session is marked closed
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		t.Fatal("expected session to be marked closed")
	}
}

func TestVirtualConnWriteDeadlineExpired(t *testing.T) {
	s := NewSession("test-deadline")
	v := NewVirtualConn(s, nil)
	v.SetWriteDeadline(time.Now().Add(-1 * time.Second))

	n, err := v.Write([]byte("data"))
	if err != os.ErrDeadlineExceeded {
		t.Fatalf("expected os.ErrDeadlineExceeded, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected n=0 on deadline exceeded, got %d", n)
	}
}

func TestVirtualConnReadDeadlineExpired(t *testing.T) {
	s := NewSession("test-read-deadline")
	v := NewVirtualConn(s, nil)
	v.SetReadDeadline(time.Now().Add(-1 * time.Second))

	_, err := v.Read(make([]byte, 1024))
	if err != os.ErrDeadlineExceeded {
		t.Fatalf("expected os.ErrDeadlineExceeded, got %v", err)
	}
}

func TestVirtualConnReadWithData(t *testing.T) {
	s := NewSession("test-read-data")
	v := NewVirtualConn(s, nil)

	expected := []byte("hello proxy")
	s.RxChan <- expected

	buf := make([]byte, 1024)
	n, err := v.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != string(expected) {
		t.Fatalf("expected %q, got %q", expected, buf[:n])
	}
}

func TestVirtualConnWriteNormalFlow(t *testing.T) {
	s := NewSession("test-write-normal")
	v := NewVirtualConn(s, nil)

	n, err := v.Write([]byte("test data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 9 {
		t.Fatalf("expected n=9, got %d", n)
	}
}
