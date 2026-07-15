package transport

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/lyafence/flowdav/internal/logger"
)

const (
	compressFlagNone = 0x00
	compressFlagGzip = 0x01
	compressMinBytes = 256 // minimum raw payload to bother compressing
)

// CryptoConfig holds the AES-256-GCM encryption key and HMAC-SHA256 key.
type CryptoConfig struct {
	EncKey  []byte
	HMacKey []byte
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, int64(MaxMessageSize)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxMessageSize {
		return nil, fmt.Errorf("decompressed payload exceeds max message size")
	}
	return b, nil
}

// encodeWithCrypto encrypts and writes the envelope to the writer.
// If gw is non-nil, it is reused for gzip compression; otherwise a fresh
// gzip.Writer is allocated for each compressible payload.
// Payloads ≥256 bytes are gzip-compressed before encryption (flag byte
// stored alongside to support backward-compatible decode).
func (e *Envelope) encodeWithCrypto(w io.Writer, cfg *CryptoConfig, gw *gzip.Writer) error {
	data, err := e.MarshalBinary()
	if err != nil {
		return err
	}

	if cfg == nil {
		_, err = w.Write(data)
		return err
	}

	// Compress (if beneficial) and prepend a 1-byte flag
	var payload []byte
	if len(data) >= compressMinBytes {
		if gw == nil {
			compressed, cerr := gzipCompress(data)
			if cerr != nil {
				return cerr
			}
			if len(compressed) < len(data) {
				payload = append([]byte{compressFlagGzip}, compressed...)
				logger.Debug("Crypto: compressed %d → %d bytes", len(data), len(compressed))
			}
		} else {
			var compressedBuf bytes.Buffer
			gw.Reset(&compressedBuf)
			if _, err := gw.Write(data); err != nil {
				return err
			}
			if err := gw.Close(); err != nil {
				return err
			}
			if compressedBuf.Len() < len(data) {
				compressed := compressedBuf.Bytes()
				payload = append([]byte{compressFlagGzip}, compressed...)
				logger.Debug("Crypto: compressed %d → %d bytes", len(data), len(compressed))
			}
		}
	}
	if payload == nil {
		payload = append([]byte{compressFlagNone}, data...)
	}

	block, err := aes.NewCipher(cfg.EncKey)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ciphertext := gcm.Seal(nonce, nonce, payload, nil)

	h := hmac.New(sha256.New, cfg.HMacKey)
	h.Write(ciphertext)
	hmacBytes := h.Sum(nil)

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(ciphertext)))
	if _, err := w.Write(lenBuf); err != nil {
		return err
	}
	if _, err := w.Write(ciphertext); err != nil {
		return err
	}
	if _, err := w.Write(hmacBytes); err != nil {
		return err
	}
	logger.Debug("Crypto: encoded envelope %s seq=%d (%d bytes)", e.SessionID, e.Seq, len(data))
	return nil
}

// EncodeWithCrypto encrypts and writes the envelope to the writer.
// Payloads ≥256 bytes are gzip-compressed before encryption (flag byte
// stored alongside to support backward-compatible decode).
func (e *Envelope) EncodeWithCrypto(w io.Writer, cfg *CryptoConfig) error {
	return e.encodeWithCrypto(w, cfg, nil)
}

// MaxMessageSize defines the maximum allowed message size to prevent OOM attacks.
// Set once at startup before any goroutines begin. All other state lives inside
// structs — no global state. Exception to "no global state" design invariant
// (see AGENTS.md), justified by OOM prevention.
var MaxMessageSize = 16 * 1024 * 1024 // 16 MB

// decodeEnvelopeWithCrypto reads and decrypts an envelope from the reader.
// If gr is non-nil, it is reused for gzip decompression; otherwise a fresh
// gzip.Reader is allocated for each compressed payload.
func decodeEnvelopeWithCrypto(r io.Reader, cfg *CryptoConfig, gr *gzip.Reader) (*Envelope, error) {
	if cfg == nil {
		// No crypto: just unmarshal directly from reader
		env := &Envelope{}
		if err := env.Decode(r); err != nil {
			return nil, err
		}
		return env, nil
	}

	// Read length
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, err
	}
	dataLen := binary.BigEndian.Uint32(lenBuf)

	// Validate dataLen to prevent overflow and OOM. Compare as uint32 before
	// converting to int so 32-bit builds cannot bypass the check via sign wrap.
	if dataLen > uint32(MaxMessageSize) {
		return nil, fmt.Errorf("message too large: %d bytes (max %d)", dataLen, MaxMessageSize)
	}
	if dataLen < 32 {
		return nil, fmt.Errorf("message too small: %d bytes (min 32 for HMAC)", dataLen)
	}

	// Read ciphertext + HMAC
	totalLen := dataLen + 32 // HMAC-SHA256 is 32 bytes
	// Check for uint32 overflow
	if totalLen < dataLen {
		return nil, fmt.Errorf("invalid data length: overflow")
	}
	data := make([]byte, totalLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	ciphertext := data[:dataLen]
	hmacReceived := data[dataLen:]

	// Verify HMAC
	h := hmac.New(sha256.New, cfg.HMacKey)
	h.Write(ciphertext)
	hmacExpected := h.Sum(nil)
	if !hmac.Equal(hmacExpected, hmacReceived) {
		return nil, fmt.Errorf("HMAC verification failed")
	}

	// Decrypt
	block, err := aes.NewCipher(cfg.EncKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short for nonce: %d < %d", len(ciphertext), nonceSize)
	}
	nonce := ciphertext[:nonceSize]
	ciphertext = ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	// Check compression flag (backward compat: flag 0x00/0x01 = new
	// format; anything else = old format without flag byte).
	var wire []byte
	switch plaintext[0] {
	case compressFlagGzip:
		if gr == nil {
			wire, err = gzipDecompress(plaintext[1:])
		} else {
			if err := gr.Reset(bytes.NewReader(plaintext[1:])); err != nil {
				return nil, fmt.Errorf("gzip reset error: %w", err)
			}
			wire, err = io.ReadAll(io.LimitReader(gr, int64(MaxMessageSize)+1))
		}
		if err != nil {
			return nil, fmt.Errorf("decompress error: %w", err)
		}
		if len(wire) > MaxMessageSize {
			return nil, fmt.Errorf("decompressed payload exceeds max message size")
		}
	case compressFlagNone:
		wire = plaintext[1:]
	default:
		wire = plaintext // old format, no flag byte
	}

	env := &Envelope{}
	if _, err := env.UnmarshalBinary(wire); err != nil {
		return nil, err
	}
	return env, nil
}

// DecodeEnvelopeWithCrypto reads and decrypts an envelope from the reader.
func DecodeEnvelopeWithCrypto(r io.Reader, cfg *CryptoConfig) (*Envelope, error) {
	return decodeEnvelopeWithCrypto(r, cfg, nil)
}
