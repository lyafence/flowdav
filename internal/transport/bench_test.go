package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/storage"
)

type benchBackend struct {
	mu sync.Mutex
}

func (b *benchBackend) Login(_ context.Context) error { return nil }
func (b *benchBackend) Upload(_ context.Context, _ string, _ io.Reader) error {
	return nil
}
func (b *benchBackend) ListQuery(_ context.Context, _ string) ([]storage.FileEntry, error) {
	return nil, nil
}
func (b *benchBackend) Download(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (b *benchBackend) Delete(_ context.Context, _ string) error { return nil }
func (b *benchBackend) UploadByIndex(_ context.Context, _ string, _ io.Reader, _ uint8) error {
	return nil
}
func (b *benchBackend) DownloadByIndex(_ context.Context, _ string, _ uint8) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (b *benchBackend) UploadAny(_ context.Context, _ string, _ io.Reader) (uint8, error) {
	return 0, nil
}

var benchCfg = &CryptoConfig{
	EncKey:  make([]byte, 32),
	HMacKey: make([]byte, 32),
}

var benchSessionID = "bench-session-0123456789abcdef"
var benchTargetAddr = "example.com:443"

func makeEnvelope(payloadSize int) Envelope {
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i)
	}
	return Envelope{
		SessionID:  benchSessionID,
		Seq:        1,
		Payload:    payload,
		TargetAddr: benchTargetAddr,
		Close:      false,
		BackendIdx: 0,
	}
}

func makeSessions(n int, payloadSize int) []*Session {
	sessions := make([]*Session, n)
	for i := range n {
		s := NewSession(fmt.Sprintf("bench-session-%d", i))
		s.TargetAddr = benchTargetAddr
		payload := make([]byte, payloadSize)
		for j := range payload {
			payload[j] = byte(i + j)
		}
		s.mu.Lock()
		s.txBuf = payload
		s.mu.Unlock()
		sessions[i] = s
	}
	return sessions
}

// BenchmarkEncodeWithCrypto measures AES-256-GCM + HMAC + optional gzip for a single envelope.
func BenchmarkEncodeWithCrypto(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576, 4194304}
	for _, size := range sizes {
		env := makeEnvelope(size)
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			var buf bytes.Buffer
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := env.EncodeWithCrypto(&buf, benchCfg); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(buf.Len()), "wire_bytes")
		})
	}
}

// BenchmarkDecodeWithCrypto measures HMAC verify + AES-256-GCM decrypt + optional gunzip.
func BenchmarkDecodeWithCrypto(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576, 4194304}
	for _, size := range sizes {
		env := makeEnvelope(size)
		var buf bytes.Buffer
		if err := env.EncodeWithCrypto(&buf, benchCfg); err != nil {
			b.Fatal(err)
		}
		data := buf.Bytes()
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				r := bytes.NewReader(data)
				_, err := DecodeEnvelopeWithCrypto(r, benchCfg)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMarshalBinary measures envelope serialization to binary format.
func BenchmarkMarshalBinary(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576}
	for _, size := range sizes {
		env := makeEnvelope(size)
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := env.MarshalBinary(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkUnmarshalBinary measures envelope deserialization from binary format.
func BenchmarkUnmarshalBinary(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576}
	for _, size := range sizes {
		env := makeEnvelope(size)
		data, err := env.MarshalBinary()
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var decoded Envelope
				if _, err := decoded.UnmarshalBinary(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFullRoundtrip measures complete encode → wire → decode cycle.
func BenchmarkFullRoundtrip(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576}
	for _, size := range sizes {
		env := makeEnvelope(size)
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			var wireBuf bytes.Buffer
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				wireBuf.Reset()
				if err := env.EncodeWithCrypto(&wireBuf, benchCfg); err != nil {
					b.Fatal(err)
				}
				if _, err := DecodeEnvelopeWithCrypto(&wireBuf, benchCfg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFlushAll measures flushAll with M sessions, each with N bytes of txBuf.
func BenchmarkFlushAll(b *testing.B) {
	configs := []struct {
		sessions    int
		payloadSize int
	}{
		{1, 65536},
		{10, 65536},
		{50, 65536},
		{100, 65536},
		{10, 1048576},
		{50, 1048576},
	}
	for _, cfg := range configs {
		b.Run(fmt.Sprintf("sessions=%d/payload=%d", cfg.sessions, cfg.payloadSize), func(b *testing.B) {
			be := &benchBackend{}
			engine := NewEngine(be, true, benchCfg)
			engine.SetPollRate(3600000) // prevent polling
			engine.SetFlushRate(3600000)
			ctx, cancel := context.WithCancel(context.Background())
			engine.Start(ctx)

			// Pre-create sessions and add them to engine
			sessions := makeSessions(cfg.sessions, cfg.payloadSize)
			for _, s := range sessions {
				engine.AddSession(s)
			}

			// Prevent upload worker blocking by setting large upload buffer
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				engine.flushAll(ctx)
				// Re-seed txBuf so flushAll has data next call
				if i+1 < b.N {
					for _, s := range sessions {
						s.mu.Lock()
						payload := make([]byte, cfg.payloadSize)
						s.txBuf = payload
						s.mu.Unlock()
					}
				}
			}
			b.StopTimer()
			cancel()
			engine.Stop()
		})
	}
}

// BenchmarkProcessRx tests ProcessRx with increasing number of out-of-order packets.
func BenchmarkProcessRx(b *testing.B) {
	configs := []struct {
		oooPackets  int // number of out-of-order packets to queue
		payloadSize int
	}{
		{0, 1024},    // In-order
		{10, 1024},   // 10 OOO
		{100, 1024},  // 100 OOO
		{500, 1024},  // 500 OOO
		{1000, 1024}, // 1000 OOO (max queue)
	}
	for _, cfg := range configs {
		b.Run(fmt.Sprintf("ooo=%d/payload=%d", cfg.oooPackets, cfg.payloadSize), func(b *testing.B) {
			s := NewSession("bench-processrx")
			payload := make([]byte, cfg.payloadSize)

			// Pre-fill queue with OOO packets
			for i := 0; i < cfg.oooPackets; i++ {
				env := &Envelope{
					SessionID: "bench-processrx",
					Seq:       uint64((i + 1) * 2), // all after rxSeq=0
					Payload:   payload,
				}
				s.ProcessRx(env)
			}

			// Now send seq=0 to flush all queued packets
			env := &Envelope{
				SessionID: "bench-processrx",
				Seq:       0,
				Payload:   payload,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.ProcessRx(env)
			}
		})
	}
}

// BenchmarkJitterFunctions measures the cost of random jitter calculations.
func BenchmarkJitterFunctions(b *testing.B) {
	e := NewEngine(&benchBackend{}, true, nil)
	durations := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 5 * time.Second}

	for _, d := range durations {
		b.Run(fmt.Sprintf("pollJitter=%dms", d.Milliseconds()), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				e.jitterPollInterval(d)
			}
		})
	}

	for _, d := range durations {
		b.Run(fmt.Sprintf("flushJitter=%dms", d.Milliseconds()), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				e.jitterFlushInterval(d)
			}
		})
	}
}
