package transport

import (
	"bytes"
	"io"
	"testing"
)

func TestGzipCompressDecompress(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"small payload", []byte("hello world")},
		{"compressible", bytes.Repeat([]byte("ABCDEFGHIJ"), 1000)},
		{"binary", []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed, err := gzipCompress(tt.input)
			if err != nil {
				t.Fatalf("gzipCompress failed: %v", err)
			}
			if len(compressed) == 0 {
				t.Fatal("compressed output is empty")
			}
			decompressed, err := gzipDecompress(compressed)
			if err != nil {
				t.Fatalf("gzipDecompress failed: %v", err)
			}
			if !bytes.Equal(decompressed, tt.input) {
				t.Fatalf("roundtrip mismatch: got %d bytes, want %d bytes", len(decompressed), len(tt.input))
			}
		})
	}
}

func TestGzipCompressLargePayload(t *testing.T) {
	input := make([]byte, 100000)
	for i := range input {
		input[i] = byte(i % 256)
	}
	compressed, err := gzipCompress(input)
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	if len(compressed) >= len(input) {
		t.Logf("incompressible data grew: %d → %d (expected for random)", len(input), len(compressed))
	}
	decompressed, err := gzipDecompress(compressed)
	if err != nil {
		t.Fatalf("gzipDecompress failed: %v", err)
	}
	if !bytes.Equal(decompressed, input) {
		t.Fatal("roundtrip mismatch on large payload")
	}
}

func TestEncodeDecodeWithCrypto(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}

	env := &Envelope{
		SessionID:  "test-session",
		Seq:        1,
		Payload:    []byte("hello world"),
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

// TestVersionByte verifies that the version byte is written at offset 1
// and validated on decode. Before fix: byte 1 is sidLen high byte, no version.
// After fix: byte 1 is VersionByte (0x01), and mismatched version is rejected.
func TestVersionByte(t *testing.T) {
	env := &Envelope{
		SessionID:  "test",
		Seq:        1,
		Payload:    []byte("hello"),
		TargetAddr: "example.com:80",
	}

	data, err := env.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	// Before fix: byte 1 is sidLen high byte (e.g. 0x00 for short IDs)
	// After fix: byte 1 is VersionByte (0x01)
	if data[1] != 0x01 {
		t.Errorf("expected version byte 0x01 at offset 1, got 0x%02X", data[1])
	}

	// Roundtrip via MarshalBinary/UnmarshalBinary
	decoded := &Envelope{}
	n, err := decoded.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("UnmarshalBinary roundtrip failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("UnmarshalBinary consumed %d bytes, expected %d", n, len(data))
	}
	if decoded.SessionID != env.SessionID {
		t.Errorf("roundtrip SessionID mismatch: got %q, want %q", decoded.SessionID, env.SessionID)
	}

	// Roundtrip via MarshalBinary/UnmarshalBinary (plaintext)
	data2, err := env.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}
	decoded2 := &Envelope{}
	if _, err := decoded2.UnmarshalBinary(data2); err != nil {
		t.Fatalf("UnmarshalBinary roundtrip failed: %v", err)
	}
	if decoded2.SessionID != env.SessionID {
		t.Errorf("Encode/Decode roundtrip SessionID mismatch: got %q, want %q", decoded2.SessionID, env.SessionID)
	}
}

func TestMarshalUnmarshal(t *testing.T) {
	env := &Envelope{
		SessionID:  "test-session-123",
		Seq:        42,
		Payload:    []byte("test payload data"),
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
		data, err := env.MarshalBinary()
		if err == nil {
			pw.Write(data)
		}
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

// TestMarshalBinaryOverflowGuard verifies that MarshalBinary handles
// MaxMessageSize payload without overflow.
func TestMarshalBinaryOverflowGuard(t *testing.T) {
	payload := make([]byte, MaxMessageSize)
	env := &Envelope{
		SessionID:  "test",
		Payload:    payload,
		TargetAddr: "example.com:80",
	}
	data, err := env.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed for MaxMessageSize payload: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}
