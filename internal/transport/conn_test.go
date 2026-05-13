package transport

import (
	"context"
	"io"
	"os"
	"sync"
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

// TestVirtualConnCloseUnblocksRead verifies that Close() unblocks a
// concurrent Read() call. Without readWake in the readRxChan select,
// Read() blocks forever on <-session.RxChan after Close().
func TestVirtualConnCloseUnblocksRead(t *testing.T) {
	s := NewSession("test-close-read")
	v := NewVirtualConn(s, nil)

	readDone := make(chan error, 1)
	go func() {
		_, err := v.Read(make([]byte, 1024))
		readDone <- err
	}()

	// Let Read() settle into readRxChan
	time.Sleep(50 * time.Millisecond)

	v.Close()

	select {
	case err := <-readDone:
		if err != io.EOF {
			t.Fatalf("expected io.EOF after Close, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read blocked forever after Close — readWake not in select")
	}
}

// TestVirtualConnCloseDrainsRxChan verifies that when Close() is called
// concurrently with data in RxChan, the data is returned before EOF.
// Without the drain in the readWake case, data would be lost on a race.
func TestVirtualConnCloseDrainsRxChan(t *testing.T) {
	s := NewSession("test-close-drain")
	v := NewVirtualConn(s, nil)

	expected := []byte("data before close")
	s.RxChan <- expected

	readDone := make(chan struct{})
	var got []byte
	go func() {
		buf := make([]byte, 1024)
		n, _ := v.Read(buf)
		got = append([]byte{}, buf[:n]...)
		close(readDone)
	}()

	// Let Read() settle into readRxChan (RxChan already has data)
	time.Sleep(50 * time.Millisecond)

	v.Close()

	select {
	case <-readDone:
		if string(got) != string(expected) {
			t.Fatalf("expected %q from drain, got %q", expected, got)
		}
	case <-time.After(time.Second):
		t.Fatal("Read blocked — drain in readWake not working")
	}
}

// TestVirtualConnDoubleClose verifies that calling Close() twice does not
// block or panic, fixing a deadlock where onClose (e.g. connLimit receive)
// would block forever on the second invocation.
func TestVirtualConnDoubleClose(t *testing.T) {
	closeCount := 0
	v := NewVirtualConnWithOnClose(NewSession("test-double-close"), nil, func() {
		closeCount++
	})

	// First close must succeed and invoke onClose
	err := v.Close()
	if err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if closeCount != 1 {
		t.Fatalf("onClose called %d times, want 1", closeCount)
	}

	// Second close must NOT block (would deadlock without sync.Once)
	done := make(chan struct{})
	go func() {
		err := v.Close()
		if err != nil {
			t.Errorf("second Close failed: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Close blocked — goroutine leak / deadlock")
	}

	if closeCount != 1 {
		t.Fatalf("onClose called %d times after second close, want 1", closeCount)
	}
}

// TestVirtualConnConcurrentDoubleClose reproduces the double-close deadlock under
// heavy concurrent load. Without sync.Once, N-1 out of N goroutines block
// forever on the second+ call to v.Close() when onClose is a blocking
// operation (e.g., <-connLimit). This test MUST complete within 2 seconds
// on the fixed code; on the vulnerable code it deadlocks permanently.
//
// PoC: In the unfixed code, 9 of 10 goroutines never return from Close()
// because onClose (which does <-connLimit) is called 10 times but the
// channel only has 1 item. The goroutines hang forever → resource leak.
func TestVirtualConnConcurrentDoubleClose(t *testing.T) {
	const goroutines = 10
	closeCh := make(chan struct{}, 1) // simulate connLimit: capacity 1
	closeCh <- struct{}{}

	var mu sync.Mutex
	closeCount := 0

	v := NewVirtualConnWithOnClose(NewSession("test-concurrent-close"), nil, func() {
		mu.Lock()
		closeCount++
		mu.Unlock()
		// Block until we can receive from closeCh.
		// In the vulnerable code, this blocks forever after the 1st call
		// because the channel only has 1 item.
		<-closeCh
	})

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.Close() // may block forever in vulnerable code
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines completed. With fix: onClose called once,
		// the rest are no-ops. closeCh is drained once by the single
		// real onClose invocation, then all wg.Done() fire.
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent Close deadlock: %d goroutines blocked forever", goroutines-1)
	}

	mu.Lock()
	if closeCount != 1 {
		t.Fatalf("onClose called %d times, want 1 — sync.Once broken", closeCount)
	}
	mu.Unlock()
}
