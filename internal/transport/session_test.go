package transport

import (
	"context"
	"testing"
	"time"
)

func TestEnqueueTxCtxCancellation(t *testing.T) {
	s := NewSession("test-session")

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// EnqueueTx with canceled context should not block
	s.EnqueueTxCtx(ctx, []byte("test data"))

	// Session should not be in a broken state
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	if closed {
		t.Error("session should not be closed after context cancellation")
	}
}

func TestEnqueueTxBasic(t *testing.T) {
	s := NewSession("test-session")

	s.EnqueueTx(context.Background(), []byte("test data"))

	s.mu.Lock()
	if len(s.txBuf) == 0 {
		t.Error("txBuf should contain data")
	}
	s.mu.Unlock()
}

func TestEnqueueTxCanceledContext(t *testing.T) {
	s := NewSession("test-session")
	s.txBuf = make([]byte, 2*1024*1024) // fill buffer to trigger backpressure

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		s.EnqueueTx(ctx, []byte("more data"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("EnqueueTx blocked on backpressure despite cancelled context")
	}
}

func TestSessionProcessRx(t *testing.T) {
	s := NewSession("test-session")

	env := &Envelope{
		SessionID: "test-session",
		Seq:       0,
		Payload:   []byte("test payload"),
	}

	s.ProcessRx(env)

	select {
	case data := <-s.RxChan:
		if string(data) != "test payload" {
			t.Errorf("expected 'test payload', got '%s'", string(data))
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for data on RxChan")
	}
}

func TestSessionProcessRxOutOfOrder(t *testing.T) {
	s := NewSession("test-session")

	// Enqueue seq 1 first
	env1 := &Envelope{
		SessionID: "test-session",
		Seq:       1,
		Payload:   []byte("payload 1"),
	}
	s.ProcessRx(env1)

	// Enqueue seq 0 - should trigger delivery of both
	env0 := &Envelope{
		SessionID: "test-session",
		Seq:       0,
		Payload:   []byte("payload 0"),
	}
	s.ProcessRx(env0)

	// Should receive seq 0 first, then seq 1
	select {
	case data := <-s.RxChan:
		if string(data) != "payload 0" {
			t.Errorf("expected 'payload 0', got '%s'", string(data))
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for first payload")
	}

	select {
	case data := <-s.RxChan:
		if string(data) != "payload 1" {
			t.Errorf("expected 'payload 1', got '%s'", string(data))
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for second payload")
	}
}

func TestEnqueueTxCtxEarlyContextCheck(t *testing.T) {
	s := NewSession("test-session")

	s.mu.Lock()
	s.txBuf = make([]byte, 2*1024*1024)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	s.EnqueueTxCtx(ctx, []byte("test"))
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("context check at loop entry failed, took %v", elapsed)
	}
}

// TestEnqueueTxCtxCancelDuringBackpressureWait verifies that EnqueueTxCtx returns
// promptly when the context is cancelled while the goroutine is blocked on backpressure,
// WITHOUT requiring an explicit wakeupTx call. The old
// sync.Cond.Wait() would block forever if ctx was cancelled without a Broadcast.
func TestEnqueueTxCtxCancelDuringBackpressureWait(t *testing.T) {
	s := NewSession("test-backpressure-cancel")

	s.mu.Lock()
	s.txBuf = make([]byte, 2*1024*1024+1)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.EnqueueTxCtx(ctx, []byte("x"))
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel context WITHOUT calling wakeupTx
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EnqueueTxCtx did not return after context cancellation — goroutine leak")
	}
}

// TestEnqueueTxCtxWakeupTxUnblocksWriter verifies the complementary path:
// a writer blocked on backpressure resumes once the flush loop drains the
// buffer and calls wakeupTx (the <-waitCh branch), without context cancellation.
func TestEnqueueTxCtxWakeupTxUnblocksWriter(t *testing.T) {
	s := NewSession("test-backpressure-wakeup")

	s.mu.Lock()
	s.txBuf = make([]byte, 2*1024*1024+1)
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.EnqueueTxCtx(context.Background(), []byte("x"))
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Mimic the flush loop: drain the buffer, then wake blocked writers.
	s.mu.Lock()
	s.txBuf = nil
	s.mu.Unlock()
	s.wakeupTx()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EnqueueTxCtx did not return after wakeupTx — goroutine leak")
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		t.Fatal("session must not be closed by wakeupTx")
	}
	s.mu.Unlock()
}

// TestExtractTxBatchClientFirstPacket verifies the client sends its first
// (empty) flush on a fresh session (txSeq==0 && isClient), even with no
// payload, so the session creates the file and the handshake completes.
func TestExtractTxBatchClientFirstPacket(t *testing.T) {
	s := NewSession("test-first-packet")

	payload, seq, _, ok := s.ExtractTxBatch(true)
	if !ok {
		t.Fatal("client ExtractTxBatch should return ok on first call (txSeq==0 && isClient)")
	}
	if len(payload) != 0 {
		t.Errorf("expected empty payload on first packet, got %d bytes", len(payload))
	}
	if seq != 0 {
		t.Errorf("expected seq 0 on first packet, got %d", seq)
	}
}

// TestProcessRxDropsWhenQueueFull verifies the OOM guard drops out-of-order
// packets once the rxQueue hits MaxRxQueueSize instead of growing unbounded.
func TestProcessRxDropsWhenQueueFull(t *testing.T) {
	s := NewSession("test-rxqueue-drop")

	// Fill the out-of-order queue to the cap directly (avoids flooding the
	// 64-slot RxChan with thousands of deliveries).
	s.mu.Lock()
	for i := 0; i < MaxRxQueueSize; i++ {
		seq := uint64(i) + 100000
		s.rxQueue[seq] = Envelope{SessionID: s.ID, Seq: seq}
	}
	s.mu.Unlock()

	// One more out-of-order packet must be dropped, not queued.
	s.ProcessRx(&Envelope{
		SessionID: s.ID,
		Seq:       500000,
		Payload:   []byte("overflow"),
	})

	s.mu.Lock()
	if len(s.rxQueue) != MaxRxQueueSize {
		s.mu.Unlock()
		t.Fatalf("rxQueue grew past cap: got %d, want %d", len(s.rxQueue), MaxRxQueueSize)
	}
	if _, dup := s.rxQueue[500000]; dup {
		s.mu.Unlock()
		t.Fatal("dropped packet was still queued")
	}
	s.mu.Unlock()
}

// SessionIdleTimeout closes session via ExtractTxBatch when idle.
func TestSessionIdleTimeout(t *testing.T) {
	s := NewSession("test-idle")
	s.IdleTimeout = 50 * time.Millisecond
	s.lastActivity = time.Now().Add(-100 * time.Millisecond)
	_, _, closed, ok := s.ExtractTxBatch(false)
	if !closed {
		t.Error("session should be closed after idle timeout")
	}
	if !ok {
		t.Error("ExtractTxBatch should return ok for closed session")
	}
}

func TestProcessRxTimerReuseNoDeadlock(t *testing.T) {
	s := NewSession("test-timer-reuse")

	// First call: normal send to RxChan
	env1 := &Envelope{
		SessionID: "test-timer-reuse",
		Seq:       0,
		Payload:   []byte("first"),
	}
	s.ProcessRx(env1)

	select {
	case <-s.RxChan:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first payload")
	}

	// Second call: timer.Stop/drain must not cause deadlock
	env2 := &Envelope{
		SessionID: "test-timer-reuse",
		Seq:       1,
		Payload:   []byte("second"),
	}
	s.ProcessRx(env2)

	select {
	case data := <-s.RxChan:
		if string(data) != "second" {
			t.Errorf("expected 'second', got %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for second payload — timer drain deadlock")
	}
}
