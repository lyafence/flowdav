package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/lyafence/flowdav/internal/storage"
)

type benchBackend struct{}

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

// Package-level result variables prevent compiler dead-code elimination (DCE)
// from eliding the function calls being benchmarked.
var benchResult interface{}

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
			var lastN int
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := env.EncodeWithCrypto(&buf, benchCfg); err != nil {
					b.Fatal(err)
				}
			}
			lastN = buf.Len()
			benchResult = lastN
			b.ReportMetric(float64(lastN), "wire_bytes")
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
			var lastEnv *Envelope
			for i := 0; i < b.N; i++ {
				r := bytes.NewReader(data)
				decoded, err := DecodeEnvelopeWithCrypto(r, benchCfg)
				if err != nil {
					b.Fatal(err)
				}
				lastEnv = decoded
			}
			benchResult = lastEnv
		})
	}
}

// BenchmarkEncodeWithCryptoReusableWriter measures EncodeWithCrypto with a
// reusable gzip.Writer to show the allocation savings from avoiding a new
// gzip.Writer (and underlying deflate buffers) per envelope.
//
// Calls unexported encodeWithCrypto — the public EncodeWithCrypto doesn't
// expose a reusable gzip.Writer parameter. This is a micro-benchmark of an
// internal optimization, not a behavioral test. The exception is documented
// and intentional (AGENTS.md: "test through public API, not extracted private
// methods" applies to behavioral tests, not micro-benchmarks of internals).
func BenchmarkEncodeWithCryptoReusableWriter(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576, 4194304}
	for _, size := range sizes {
		env := makeEnvelope(size)
		gw, err := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		if err != nil {
			b.Fatal(err)
		}
		defer gw.Close()
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			var buf bytes.Buffer
			var lastN int
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := env.encodeWithCrypto(&buf, benchCfg, gw); err != nil {
					b.Fatal(err)
				}
			}
			lastN = buf.Len()
			benchResult = lastN
			b.ReportMetric(float64(lastN), "wire_bytes")
		})
	}
}

// BenchmarkDecodeWithCryptoReusableReader measures DecodeEnvelopeWithCrypto with
// a reusable gzip.Reader to show the allocation savings from avoiding a new
// gzip.Reader per compressed envelope.
//
// Calls unexported decodeEnvelopeWithCrypto — the public DecodeEnvelopeWithCrypto
// doesn't expose a reusable gzip.Reader parameter. See the ReusableWriter variant
// for the rationale on why this benchmarks an internal path.
func BenchmarkDecodeWithCryptoReusableReader(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576, 4194304}
	var gr *gzip.Reader
	{
		var initBuf bytes.Buffer
		w := gzip.NewWriter(&initBuf)
		w.Close()
		gr, _ = gzip.NewReader(&initBuf)
	}
	defer gr.Close()
	for _, size := range sizes {
		env := makeEnvelope(size)
		var buf bytes.Buffer
		if err := env.EncodeWithCrypto(&buf, benchCfg); err != nil {
			b.Fatal(err)
		}
		data := buf.Bytes()
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			var lastEnv *Envelope
			for i := 0; i < b.N; i++ {
				r := bytes.NewReader(data)
				decoded, err := decodeEnvelopeWithCrypto(r, benchCfg, gr)
				if err != nil {
					b.Fatal(err)
				}
				lastEnv = decoded
			}
			benchResult = lastEnv
		})
	}
}

// BenchmarkMarshalBinary measures envelope serialization to binary format.
func BenchmarkMarshalBinary(b *testing.B) {
	sizes := []int{256, 4096, 65536, 1048576}
	for _, size := range sizes {
		env := makeEnvelope(size)
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			var last []byte
			for i := 0; i < b.N; i++ {
				data, err := env.MarshalBinary()
				if err != nil {
					b.Fatal(err)
				}
				last = data
			}
			benchResult = last
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
			var lastN int
			for i := 0; i < b.N; i++ {
				var decoded Envelope
				n, err := decoded.UnmarshalBinary(data)
				if err != nil {
					b.Fatal(err)
				}
				lastN = n
			}
			benchResult = lastN
			b.ReportMetric(float64(len(data)), "wire_bytes")
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
			var lastEnv *Envelope
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				wireBuf.Reset()
				if err := env.EncodeWithCrypto(&wireBuf, benchCfg); err != nil {
					b.Fatal(err)
				}
				decoded, err := DecodeEnvelopeWithCrypto(&wireBuf, benchCfg)
				if err != nil {
					b.Fatal(err)
				}
				lastEnv = decoded
			}
			benchResult = lastEnv
		})
	}
}

// BenchmarkFlushAll measures flushAll with M sessions, each with N bytes of txBuf.
//
// Calls unexported flushAll directly — comparable to a synchronous micro-benchmark
// of the internal flush pipeline's CPU cost. The public path (flushLoop goroutine)
// adds scheduling latency that would dominate measurement noise. This is a
// micro-benchmark of internals, not a behavioral test.
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
			var gw *gzip.Writer
			if benchCfg != nil {
				gw, _ = gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
				defer gw.Close()
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				engine.flushAll(ctx, gw)
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
			benchResult = len(sessions)
			b.StopTimer()
			cancel()
			engine.Stop()
		})
	}
}

// BenchmarkProcessRx tests ProcessRx with increasing number of out-of-order packets.
// Each iteration creates a fresh session, pre-fills the OOO queue with sequentially
// numbered packets (Seq=1..N), then sends Seq=0 to drain the queue. A concurrent
// goroutine drains RxChan so ProcessRx never blocks on channel sends.
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
			payload := make([]byte, cfg.payloadSize)
			env0 := &Envelope{
				SessionID: "bench-processrx",
				Seq:       0,
				Payload:   payload,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := NewSession("bench-processrx")
				for j := 0; j < cfg.oooPackets; j++ {
					s.ProcessRx(&Envelope{
						SessionID: "bench-processrx",
						Seq:       uint64(j + 1),
						Payload:   payload,
					})
				}

				// Concurrent drainer prevents ProcessRx from blocking
				// when draining the OOO queue into RxChan.
				drainDone := make(chan struct{})
				go func() {
					for range s.RxChan {
					}
					close(drainDone)
				}()

				b.StartTimer()
				s.ProcessRx(env0)
				b.StopTimer()

				s.Close()
				<-drainDone
			}
			benchResult = cfg.oooPackets
			b.ReportMetric(float64(cfg.oooPackets), "ooo_queue_depth")
		})
	}
}

// BenchmarkJitterFunctions measures the cost of random jitter calculations.
func BenchmarkJitterFunctions(b *testing.B) {
	e := NewEngine(&benchBackend{}, true, nil)
	durations := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 5 * time.Second}

	for _, d := range durations {
		b.Run(fmt.Sprintf("pollJitter=%dms", d.Milliseconds()), func(b *testing.B) {
			var last time.Duration
			for i := 0; i < b.N; i++ {
				last = e.jitterPollInterval(d)
			}
			benchResult = last
		})
	}

	for _, d := range durations {
		b.Run(fmt.Sprintf("flushJitter=%dms", d.Milliseconds()), func(b *testing.B) {
			var last time.Duration
			for i := 0; i < b.N; i++ {
				last = e.jitterFlushInterval(d)
			}
			benchResult = last
		})
	}
}

// BenchmarkSessionEnqueueTx measures the hot path of EnqueueTx with increasing payload sizes.
// Each iteration appends data to the session's txBuf under the mutex and immediately
// drains via ExtractTxBatch to avoid hitting the 2MB backpressure limit.
func BenchmarkSessionEnqueueTx(b *testing.B) {
	sizes := []int{64, 256, 1024, 4096, 16384}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("payload=%d", size), func(b *testing.B) {
			s := NewSession("bench-enqueue")
			data := make([]byte, size)
			ctx := context.Background()
			var lastPayload []byte
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.EnqueueTx(ctx, data)
				payload, _, _, ok := s.ExtractTxBatch(false)
				if ok {
					lastPayload = payload
				}
			}
			benchResult = lastPayload
		})
	}
}

// benchPoolBackend returns pre-encoded envelope data for pool benchmarks.
type benchPoolBackend struct {
	benchBackend
	data []byte
}

func (b *benchPoolBackend) DownloadByIndex(_ context.Context, _ string, _ uint8) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

// BenchmarkPoolDecodeLoop measures the core decode+reassembly loop from
// pool.go's processDownload (decodeEnvelopeWithCrypto + ProcessRx + session dispatch).
// This is the CPU-intensive part of the DownloadWorkerPool: decrypting,
// decompressing, and delivering envelopes. I/O (storage Download, retry, Delete)
// is excluded as it is I/O-bound, not CPU-bound.
//
// Pre-encodes N envelopes into a single byte stream (simulating one mux file),
// then decodes them through the actual processDownload pipeline.
func BenchmarkPoolDecodeLoop(b *testing.B) {
	configs := []struct {
		envelopes   int
		payloadSize int
	}{
		{1, 1024},
		{5, 1024},
		{10, 1024},
		{25, 1024},
		{100, 1024},
		{10, 65536},
	}
	for _, cfg := range configs {
		b.Run(fmt.Sprintf("env=%d/payload=%d", cfg.envelopes, cfg.payloadSize), func(b *testing.B) {
			var buf bytes.Buffer
			for i := 0; i < cfg.envelopes; i++ {
				env := makeEnvelope(cfg.payloadSize)
				env.Seq = uint64(i)
				if err := env.EncodeWithCrypto(&buf, benchCfg); err != nil {
					b.Fatal(err)
				}
			}
			fileData := buf.Bytes()

			// Initialize reusable gzip reader (same pattern as pool.go:134-141)
			var gr *gzip.Reader
			{
				var initBuf bytes.Buffer
				w := gzip.NewWriter(&initBuf)
				w.Close()
				gr, _ = gzip.NewReader(&initBuf)
			}
			defer gr.Close()

			be := &benchPoolBackend{data: fileData}
			s := NewSession(benchSessionID)
			s.RxChan = make(chan []byte, cfg.envelopes*2)
			ctx := context.Background()

			var totalEnvelopes int
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Drain rxQueue channel before each iteration
			drainLoop:
				for {
					select {
					case <-s.RxChan:
						totalEnvelopes++
					default:
						break drainLoop
					}
				}

				s.mu.Lock()
				s.rxSeq = 0
				s.rxQueue = make(map[uint64]Envelope)
				s.rxQueueBytes = 0
				s.rxClosed = false
				s.closed = false
				s.mu.Unlock()

				rc, err := be.DownloadByIndex(ctx, "bench", 0)
				if err != nil {
					b.Fatal(err)
				}
				for {
					decodedEnv, dErr := decodeEnvelopeWithCrypto(rc, benchCfg, gr)
					if dErr != nil {
						break
					}
					s.ProcessRx(decodedEnv)
				}
				rc.Close()

				// Consume RxChan payloads to unblock session
				for {
					select {
					case <-s.RxChan:
						totalEnvelopes++
					default:
						goto doneDrain
					}
				}
			doneDrain:
			}
			benchResult = totalEnvelopes
			b.ReportMetric(float64(totalEnvelopes)/float64(b.N), "envelopes/op")
		})
	}
}
