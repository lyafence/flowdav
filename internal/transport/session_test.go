package transport

import (
	"context"
	"sync"
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

	s.EnqueueTx([]byte("test data"))

	s.mu.Lock()
	if len(s.txBuf) == 0 {
		t.Error("txBuf should contain data")
	}
	s.mu.Unlock()
}

func TestEnqueueTxWithSmallData(t *testing.T) {
	s := NewSession("test-session")

	// Fill with small data that doesn't trigger backpressure
	for i := 0; i < 10; i++ {
		s.EnqueueTx([]byte("small data chunk"))
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

func TestEnqueueTxCtxCancellationBeforeBackpressure(t *testing.T) {
	s := NewSession("test-session")

	s.mu.Lock()
	s.txBuf = make([]byte, 2*1024*1024)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.EnqueueTxCtx(ctx, []byte("x"))
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

func TestEnqueueTxCtxCancellationDuringWait(t *testing.T) {
	s := NewSession("test-session")

	s.mu.Lock()
	s.txBuf = make([]byte, 2*1024*1024)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.EnqueueTxCtx(ctx, []byte("test data"))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	s.mu.Lock()
	s.txCond.Broadcast()
	s.mu.Unlock()

	wg.Wait()
}

