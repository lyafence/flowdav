package transport

import (
	"bytes"
	"io"
	"testing"
)

func TestEncodeDecodeWithCrypto(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}

	env := &Envelope{
		SessionID: "test-session",
		Seq:       1,
		Payload:   []byte("hello world"),
		TargetAddr: "example.com:80",
	}

	pr, pw := io.Pipe()
	done := make(chan bool)
	var buf bytes.Buffer

	go func() {
		io.Copy(&buf, pr)
		done <- true
	}()

	err := env.EncodeWithCrypto(pw, cfg)
	if err != nil {
		t.Fatalf("EncodeWithCrypto failed: %v", err)
	}
	pw.Close()
	<-done

	decoded, err := DecodeEnvelopeWithCrypto(&buf, cfg)
	if err != nil {
		t.Fatalf("DecodeEnvelopeWithCrypto failed: %v", err)
	}

	if decoded.SessionID != env.SessionID {
		t.Errorf("SessionID mismatch: got %s, want %s", decoded.SessionID, env.SessionID)
	}
	if decoded.Seq != env.Seq {
		t.Errorf("Seq mismatch: got %d, want %d", decoded.Seq, env.Seq)
	}
	if string(decoded.Payload) != string(env.Payload) {
		t.Errorf("Payload mismatch: got %s, want %s", decoded.Payload, env.Payload)
	}
}

func TestDecodeInvalidHMAC(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}

	env := &Envelope{
		SessionID: "test",
		Payload:   []byte("data"),
	}

	pr, pw := io.Pipe()
	go func() {
		env.EncodeWithCrypto(pw, cfg)
		pw.Close()
	}()

	var buf bytes.Buffer
	io.Copy(&buf, pr)

	// Corrupt the HMAC
	data := buf.Bytes()
	if len(data) > 32 {
		data[len(data)-1] ^= 0xFF
	}

	_, err := DecodeEnvelopeWithCrypto(bytes.NewReader(data), cfg)
	if err == nil {
		t.Fatal("expected HMAC verification to fail")
	}
}

func TestMarshalUnmarshal(t *testing.T) {
	env := &Envelope{
		SessionID: "test-session-123",
		Seq:       42,
		Payload:   []byte("test payload data"),
		TargetAddr: "127.0.0.1:8080",
		Close:      false,
	}

	data, err := env.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	decoded := &Envelope{}
	n, err := decoded.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("bytes read mismatch: got %d, want %d", n, len(data))
	}

	if decoded.SessionID != env.SessionID {
		t.Errorf("SessionID mismatch")
	}
	if decoded.Seq != env.Seq {
		t.Errorf("Seq mismatch")
	}
	if string(decoded.Payload) != string(env.Payload) {
		t.Errorf("Payload mismatch")
	}
}

func TestMaxMessageSize(t *testing.T) {
	if MaxMessageSize != 16*1024*1024 {
		t.Errorf("unexpected MaxMessageSize")
	}
}

func TestEncodeWithNilCrypto(t *testing.T) {
	env := &Envelope{
		SessionID: "test",
		Payload:   []byte("data"),
	}

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		// Read from pipe to unblock the writer
		_, err := io.Copy(io.Discard, pr)
		done <- err
	}()

	err := env.EncodeWithCrypto(pw, nil)
	pw.Close()
	<-done // Wait for reader to finish

	if err != nil {
		t.Fatalf("EncodeWithCrypto with nil cfg should work: %v", err)
	}
}

func TestDecodeWithNilCrypto(t *testing.T) {
	env := &Envelope{
		SessionID: "test",
		Payload:   []byte("data"),
	}

	pr, pw := io.Pipe()
	go func() {
		env.Encode(pw)
		pw.Close()
	}()

	decoded, err := DecodeEnvelopeWithCrypto(pr, nil)
	if err != nil {
		t.Fatalf("DecodeEnvelopeWithCrypto with nil cfg failed: %v", err)
	}
	if decoded.SessionID != env.SessionID {
		t.Errorf("SessionID mismatch")
	}
}
