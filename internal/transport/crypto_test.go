package transport

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
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
		_, _ = io.Copy(&buf, pr)
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
		_ = env.EncodeWithCrypto(pw, cfg)
		pw.Close()
	}()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, pr); err != nil {
		t.Fatal(err)
	}

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

// TestVersionByte verifies version byte at offset 1 validates on decode.
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

	// byte 1 must be VersionByte, not sidLen high byte
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
			_, _ = pw.Write(data)
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

// TestDecodeLargePayload rejects a payload length header that exceeds
// MaxMessageSize on both 32-bit and 64-bit builds in the no-crypto path.
func TestDecodeLargePayload(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(MagicByte)
	buf.WriteByte(VersionByte)
	// SessionID length = 0
	buf.WriteByte(0)
	buf.WriteByte(0)
	// Seq = 0
	buf.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	// TargetAddr length = 0
	buf.WriteByte(0)
	buf.WriteByte(0)
	// Close = false
	buf.WriteByte(0)
	// Payload length = 0x80000000 (2 GB)
	plen := make([]byte, 4)
	binary.BigEndian.PutUint32(plen, 0x80000000)
	buf.Write(plen)

	env := &Envelope{}
	if err := env.Decode(&buf); err == nil {
		t.Fatal("expected error for oversized payload length")
	}
}

// TestDecodeEnvelopeWithCryptoLargeLength rejects oversized length (uint32 safe).
func TestDecodeEnvelopeWithCryptoLargeLength(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}

	// 0x80000000 (2 GB) is larger than MaxMessageSize and, on 32-bit,
	// wraps to a negative int. The uint32 comparison must reject it.
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, 0x80000000)

	_, err := DecodeEnvelopeWithCrypto(bytes.NewReader(buf), cfg)
	if err == nil {
		t.Fatal("expected error for oversized length header")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("too large")) {
		t.Fatalf("expected 'too large' error, got: %v", err)
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

// TestEncodeWithCryptoReusableGzipWriter verifies that a reusable gzip.Writer
// can be passed to the internal encodeWithCrypto function and correctly encode
// multiple sequential envelopes without corruption.
func TestEncodeWithCryptoReusableGzipWriter(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}
	gw, err := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	for i := 0; i < 3; i++ {
		env := &Envelope{
			SessionID: "test-session-reuse-writer",
			Seq:       uint64(i),
			Payload:   bytes.Repeat([]byte("A"), 1000),
		}
		var buf bytes.Buffer
		if err := env.encodeWithCrypto(&buf, cfg, gw); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		decoded, err := DecodeEnvelopeWithCrypto(&buf, cfg)
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if decoded.Seq != env.Seq || !bytes.Equal(decoded.Payload, env.Payload) {
			t.Fatalf("roundtrip mismatch at seq=%d", i)
		}
	}
}

func TestDecodeEnvelopeTooSmallPayload(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, 1)

	_, err := DecodeEnvelopeWithCrypto(bytes.NewReader(buf), cfg)
	if err == nil {
		t.Fatal("expected error for too-small payload length")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("too small")) {
		t.Fatalf("expected 'too small' error, got: %v", err)
	}
}

func TestMarshalBinarySessionIDTooLong(t *testing.T) {
	env := &Envelope{
		SessionID:  string(make([]byte, 70000)),
		Seq:        1,
		Payload:    []byte("hello"),
		TargetAddr: "example.com:80",
	}
	_, err := env.MarshalBinary()
	if err == nil {
		t.Fatal("expected error for oversized session ID")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("session ID too long")) {
		t.Fatalf("expected 'session ID too long' error, got: %v", err)
	}
}

// TestDecodeWithCryptoReusableGzipReader verifies that a reusable gzip.Reader
// can be passed to the internal decodeEnvelopeWithCrypto function and correctly
// decode multiple sequential compressed envelopes without corruption.
func TestDecodeWithCryptoReusableGzipReader(t *testing.T) {
	cfg := &CryptoConfig{
		EncKey:  make([]byte, 32),
		HMacKey: make([]byte, 32),
	}
	var gr *gzip.Reader
	{
		var initBuf bytes.Buffer
		w := gzip.NewWriter(&initBuf)
		w.Close()
		gr, _ = gzip.NewReader(&initBuf)
	}
	defer gr.Close()

	payloads := [][]byte{
		bytes.Repeat([]byte("A"), 1000),
		bytes.Repeat([]byte("B"), 2000),
		bytes.Repeat([]byte("C"), 500),
	}
	for _, payload := range payloads {
		env := &Envelope{
			SessionID: "test-reader",
			Payload:   payload,
		}
		var enc bytes.Buffer
		if err := env.EncodeWithCrypto(&enc, cfg); err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeEnvelopeWithCrypto(&enc, cfg, gr)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(decoded.Payload, payload) {
			t.Fatalf("payload mismatch: got %d bytes, want %d bytes", len(decoded.Payload), len(payload))
		}
	}
}
