# Flowdav Wire Protocol

This document describes the binary protocol used by flowdav to encapsulate,
encrypt, and transport TCP streams over WebDAV storage.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Filename Convention](#filename-convention)
3. [Binary Envelope Format](#binary-envelope-format)
4. [Wire Format (Encrypted Transport)](#wire-format-encrypted-transport)
5. [Compression](#compression)
6. [Encryption & Authentication](#encryption--authentication)
7. [Session Lifecycle](#session-lifecycle)
8. [Multiplexing](#multiplexing)
9. [Constants & Limits](#constants--limits)

---

## Architecture Overview

```
  SOCKS5             flowdav -c                  WebDAV                  flowdav -s            Destination
    curl   ←→    encrypt / mux / upload   ←→   passive store   ←→   download / decrypt    ←→   TCP
```

Data flows in both directions. The client wraps SOCKS5 segments in
encrypted envelopes and stores them as files in WebDAV. The server polls
for new files, decrypts the envelopes, and forwards the payload to the
intended TCP destination. Responses follow the reverse path.

No side (client or server) listens on the network for data. All
communication is pull-based through the shared WebDAV storage.

---

## Filename Convention

Files written to WebDAV use a deliberately opaque naming scheme to avoid
leaking metadata (client ID, timestamps, session size).

### Format

```
{dir_byte}{16_lowercase_hex}
```

| Part | Size | Description |
|------|------|-------------|
| `dir_byte` | 1 byte | Direction indicator: `r` = request (client→server), `s` = response (server→client) |
| `16_lowercase_hex` | 16 chars | 8 cryptographically random bytes, hex-encoded |

### Storage Mapping

On the WebDAV backend, the direction byte determines the subdirectory, and
the random suffix is uppercased:

| Direction | Prefix | Subdirectory | Example filename on disk |
|-----------|--------|-------------|--------------------------|
| Request (client→server) | `r` | `invoices/` | `invoices/AB12CD34EF567890` |
| Response (server→client) | `s` | `receipts/` | `receipts/1234ABCD5678EF90` |

Mapping is handled by `internal/storage/webdav.go`.

### Example

```
Input:   r3a1f2b8c9d0e5f7a6b3c4d1e2f3a4b
Storage: invoices/3A1F2B8C9D0E5F7A6B3C4D1E2F3A4B

Input:   s9b8a7c6d5e4f3a2b1c0d9e8f7a6b5c4
Storage: receipts/9B8A7C6D5E4F3A2B1C0D9E8F7A6B5C4
```

---

## Binary Envelope Format

An envelope is the core unit of data transfer. Multiple envelopes may be
concatenated into a single WebDAV file (see [Multiplexing](#multiplexing)).

### Layout (without encryption)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Magic (0x1F) |  Version (1)  |      SessionID Length         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       SessionID (variable)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Sequence Number (uint64 BE)              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|    TargetAddr Length           |   TargetAddr (variable)       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Close (0/1)   |                  Payload Length                |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Payload (variable)                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  BackendIdx    |
+-+-+-+-+-+-+-+-+
```

| Field | Type | Offset | Size | Description |
|-------|------|--------|------|-------------|
| Magic | uint8 | 0 | 1 | Always `0x1F`. Used to identify flowdav envelopes. |
| Version | uint8 | 1 | 1 | Protocol version. Current: `0x01`. |
| SessionID Length | uint16 BE | 2 | 2 | Length of the SessionID field in bytes. Max 65535. |
| SessionID | UTF-8 string | 4 | variable | Unique session identifier. Typically a 32-char hex string (16 random bytes). |
| Sequence Number | uint64 BE | 4 + sidLen | 8 | Monotonically increasing per session. Starts at 0. |
| TargetAddr Length | uint16 BE | 12 + sidLen | 2 | Length of the TargetAddr field in bytes. Max 65535. |
| TargetAddr | UTF-8 string | 14 + sidLen | variable | Only valid on seq=0. Format: `host:port`. |
| Close | uint8 | 14 + sidLen + addrLen | 1 | `0` = normal, `1` = sender is closing its write side. |
| Payload Length | uint32 BE | 15 + sidLen + addrLen | 4 | Length of Payload in bytes. Max `MaxMessageSize`. |
| Payload | bytes | 19 + sidLen + addrLen | variable | Raw application data (SOCKS5 segment). |
| BackendIdx | uint8 | 19 + sidLen + addrLen + payLen | 1 | WebDAV backend index for multi-backend mode. Default 0. |

**Total header overhead:** 20 bytes + session ID length + target address length
(not including payload).

### Decoding

1. Read 2 bytes. Validate Magic == `0x1F`, Version == `0x01`. Reject
   unknown values.
2. Read SessionID Length (u16 BE), then SessionID bytes.
3. Read Sequence Number (u64 BE).
4. Read TargetAddr Length (u16 BE), then TargetAddr bytes.
5. Read Close flag (1 byte).
6. Read Payload Length (u32 BE), then Payload bytes.
7. Read BackendIdx (1 byte). If only `0` bytes remain (pread EOF), default to 0.

---

## Wire Format (Encrypted Transport)

When encryption is enabled (the normal case), each envelope is wrapped in a
crypto layer before being written to WebDAV. The encryption envelope
contains one or more binary envelopes.

### On-disk / On-wire layout

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Ciphertext Length (uint32 BE)                |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         Ciphertext                              |
|                   (nonce + AES-256-GCM output)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         HMAC-SHA256                             |
|                         (32 bytes)                              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Field | Type | Size | Description |
|-------|------|------|-------------|
| Ciphertext Length | uint32 BE | 4 | Length of the ciphertext that follows. |
| Ciphertext | bytes | variable | AES-256-GCM output: 12-byte nonce + encrypted payload + 16-byte GCM tag. |
| HMAC-SHA256 | bytes | 32 | HMAC-SHA256 of the ciphertext, keyed with `hmac_key`. |

**Inside the ciphertext** (after AES-256-GCM decryption):

```
 0
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Compress Flag |  Envelope(s)...
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Field | Type | Size | Description |
|-------|------|------|-------------|
| Compress Flag | uint8 | 1 | `0x00` = no compression, `0x01` = gzip compressed. |
| Envelope(s) | bytes | variable | One or more [Binary Envelopes](#binary-envelope-format), optionally gzip-compressed as a block. |

### Decryption flow

1. Read Ciphertext Length (u32 BE). Reject if > `MaxMessageSize` or < 32.
2. Read ciphertext + 32 bytes of HMAC.
3. Compute HMAC-SHA256 over ciphertext using `hmac_key`. Compare with
   received HMAC. Reject on mismatch.
4. Extract 12-byte nonce from ciphertext start.
5. AES-256-GCM decrypt remainder (nonce → plaintext).
6. Read Compress Flag byte:
   - `0x00` → read envelopes directly from remaining bytes.
   - `0x01` → gzip-decompress remaining bytes, then read envelopes.
   - Any other value → backward-compatible: assume no flag byte (legacy
     format), treat entire plaintext as envelope data.

### Encryption flow

1. Marshal the envelope(s) into binary format (`MarshalBinary`).
2. If raw payload ≥ 256 bytes, attempt gzip compression. If compressed
   output is smaller, prepend `0x01` and use compressed data.
3. Otherwise prepend `0x00` and use uncompressed data.
4. Generate random 12-byte nonce.
5. AES-256-GCM encrypt (nonce + plaintext → ciphertext).
6. Compute HMAC-SHA256 over ciphertext.
7. Write: ciphertext length (u32 BE) + ciphertext + HMAC.

### Decryption without crypto (`cfg = nil`)

When `CryptoConfig` is nil (no encryption), the wire format is skipped
entirely. The raw binary envelope(s) are read directly via `Decode()`.
No length prefix, no HMAC, no AES.

---

## Compression

Compression is applied to the raw envelope data BEFORE encryption (so the
encrypted payload does not reveal compressibility).

- **Threshold:** 256 bytes or more of raw envelope data.
- **Algorithm:** gzip (deflate) with default compression level.
- **Flag byte** (see Wire Format): `0x00` = uncompressed, `0x01` = gzip.
- **Edge case:** If compression does not reduce size, the flag is `0x00`
  and the original (uncompressed) data is used.

---

## Encryption & Authentication

### Algorithms

| Operation | Algorithm | Key size | Tag/Nonce |
|-----------|-----------|----------|-----------|
| Encryption | AES-256-GCM | 32 bytes (256 bit) | 12-byte nonce, 16-byte GCM tag |
| Authentication | HMAC-SHA256 | 32 bytes (256 bit) | 32-byte HMAC |

### Key Derivation

Keys are user-provided, base64-encoded in the configuration file:

```json
{
  "enc_key": "<32_bytes_base64>",
  "hmac_key": "<32_bytes_base64>"
}
```

Both keys must be identical on the client and server for successful
communication.

### Nonce

A fresh 12-byte nonce is generated from `crypto/rand` for every
encrypted write. Nonces are never reused because each write produces a
new random value.

---

## Session Lifecycle

### 1. Session creation (client)

1. Client receives a SOCKS5 connection request from the application
   (browser, curl, etc.).
2. Client generates a random session ID: 16 bytes from `crypto/rand`,
   hex-encoded (32 hex chars).
3. Client assigns a WebDAV backend index (random for multi-backend mode,
   0 otherwise).
4. Client creates a `Session` object, calls `EnqueueTx(nil)` to send an
   empty seq=0 ping (which carries the TargetAddr and BackendIdx).

### 2. Data flow

1. Client receives SOCKS5 data and appends it to the session's transmit
   buffer (`EnqueueTx`).
2. Every `flush_rate_ms` (default 500 ms), the engine collects pending
   transmit data from all sessions, wraps each in an Envelope with an
   incrementing sequence number, and uploads them to WebDAV as files.
3. Server polls every `poll_ticker_ms` (default 500 ms) for new files
   with the expected direction prefix.
4. Server downloads each new file, decrypts it, extracts envelopes, and
   delivers payloads to the target TCP connection via `ProcessRx`.

### 3. Session close

1. When a session's transmit or receive is done, the sender sets
   `Close = true` on the last envelope.
2. The receiver sends a final response and also sets `Close = true`.
3. The session is removed from the engine.
4. A tombstone entry prevents re-processing of delayed packets for
   30 seconds.

### 4. Idle timeout

Sessions with no activity for 10 seconds are automatically marked closed.
Their remaining pending data is flushed one last time before removal.

---

## Multiplexing

Multiple envelopes destined for the same WebDAV backend are batched into
a single file to reduce storage API calls and metadata overhead.

### Batching rules

- Envelopes from all active sessions are collected during `flushAll`.
- Envelopes sharing the same `BackendIdx` are grouped.
- The batch is serialized by concatenating the binary envelope formats
  (one after another, no separators — `MarshalBinary` output is
  self-delimiting).
- The combined size must not exceed `safeUploadSize`, which is
  approximately 87.5% of `MaxMessageSize`. The 12.5% headroom accounts
  for envelope and crypto overhead (binary headers, nonces, GCM tags,
  HMAC).
- A batch with at least one consumed envelope is flushed; the remaining
  envelopes become the next batch.

### On the receiving side

`DecodeEnvelopeWithCrypto` reads and decodes envelopes in a loop until
the input stream is exhausted. Each envelope references its `SessionID`
for demultiplexing.

---

## Constants & Limits

| Constant | Default | Maximum | Description |
|----------|---------|---------|-------------|
| `MaxMessageSize` | 16 MB | 16 MB (configurable at startup) | Maximum payload per envelope (before encryption overhead). |
| `MaxStringLen` | — | 65535 | Maximum length of SessionID or TargetAddr. |
| `MaxRxQueueSize` | — | 1000 | Out-of-order packet queue limit per session. |
| Retry attempts | — | 3 | Number of storage operation retries before giving up. |
| Idle timeout | — | 10 s | Session idle timeout before forced close. |

### Poll intervals

| Parameter | Default | Range |
|-----------|---------|-------|
| Base poll interval | 500 ms | Configurable |
| Min poll (backoff floor) | 100 ms | Configurable |
| Max poll (backoff ceiling) | 5000 ms | Configurable |
| Flush interval | 500 ms | Configurable |

### Jitter

Poll intervals use ±25% random jitter to avoid predictable polling
patterns. The jitter factor is `0.75 + (rand_byte / 255.0) * 0.5`.

---

## References

- [Source: Binary envelope format](internal/transport/envelope.go)
- [Source: Crypto wire format](internal/transport/crypto.go)
- [Source: Filename generation](internal/transport/engine.go#L435)
- [Source: Storage mapping](internal/storage/webdav.go)
- [Source: Multiplexing logic](internal/transport/engine.go#L258)
