package transport

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Envelope represents a message exchanged between the client and server.
type Envelope struct {
	// SessionID is the unique identifier for the connection (UUID).
	SessionID string `json:"session_id"`

	// Seq is the sequence number for ordering packets.
	Seq uint64 `json:"seq"`

	// TargetAddr is used by the client on the first sequence to tell the server where to connect.
	TargetAddr string `json:"target_addr,omitempty"`

	// Payload contains the actual application data.
	Payload []byte `json:"payload,omitempty"`

	// Close implies that the sender is closing its write side of the session.
	Close bool `json:"close,omitempty"`

	// BackendIdx is the index of the WebDAV backend to use (for multi-backend mode).
	// On seq=0, the client writes the assigned backend index. The server reads it
	// and uses the same backend for responses. Range: 0 to numBackends-1.
	BackendIdx uint8 `json:"backend_idx,omitempty"`
}

const (
	MagicByte   = 0x1F
	VersionByte = 0x01
)

// MaxStringLen is the maximum allowed length for SessionID and TargetAddr
const MaxStringLen = 65535 // uint16 max

// MarshalBinary serializes the envelope into the custom Flow binary format.
func (e *Envelope) MarshalBinary() ([]byte, error) {
	// Validate string lengths
	if len(e.SessionID) > MaxStringLen {
		return nil, fmt.Errorf("session ID too long: %d bytes (max %d)", len(e.SessionID), MaxStringLen)
	}
	if len(e.TargetAddr) > MaxStringLen {
		return nil, fmt.Errorf("target address too long: %d bytes (max %d)", len(e.TargetAddr), MaxStringLen)
	}
	if len(e.Payload) > MaxMessageSize {
		return nil, fmt.Errorf("payload too large: %d bytes (max %d)", len(e.Payload), MaxMessageSize)
	}

	totalSize := 1 + 1 + 2 + len(e.SessionID) + 8 + 2 + len(e.TargetAddr) + 1 + 4 + len(e.Payload) + 1
	if totalSize < 0 {
		return nil, fmt.Errorf("envelope too large: %d bytes (overflow)", totalSize)
	}
	buf := make([]byte, totalSize)
	
	buf[0] = MagicByte
	buf[1] = VersionByte
	binary.BigEndian.PutUint16(buf[2:], uint16(len(e.SessionID)))
	offset := 4
	copy(buf[offset:], e.SessionID)
	offset += len(e.SessionID)
	
	binary.BigEndian.PutUint64(buf[offset:], e.Seq)
	offset += 8
	
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(e.TargetAddr)))
	offset += 2
	copy(buf[offset:], e.TargetAddr)
	offset += len(e.TargetAddr)
	
	if e.Close {
		buf[offset] = 1
	} else {
		buf[offset] = 0
	}
	offset++

	binary.BigEndian.PutUint32(buf[offset:], uint32(len(e.Payload)))
	offset += 4

	copy(buf[offset:], e.Payload)
	offset += len(e.Payload)

	buf[offset] = e.BackendIdx
	return buf, nil
}

// Encode writes the envelope directly to an io.Writer.
func (e *Envelope) Encode(w io.Writer) error {
	// Validate string lengths
	if len(e.SessionID) > MaxStringLen {
		return fmt.Errorf("session ID too long: %d bytes (max %d)", len(e.SessionID), MaxStringLen)
	}
	if len(e.TargetAddr) > MaxStringLen {
		return fmt.Errorf("target address too long: %d bytes (max %d)", len(e.TargetAddr), MaxStringLen)
	}
	if len(e.Payload) > MaxMessageSize {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(e.Payload), MaxMessageSize)
	}

	// Header needs: 1 magic + 1 version + 2 sidLen + sid + 8 seq + 2 addrLen + addr + 1 close + 4 payloadLen + 1 backendIdx
	hdrSize := 1 + 1 + 2 + len(e.SessionID) + 8 + 2 + len(e.TargetAddr) + 1 + 4 + 1
	hdr := make([]byte, hdrSize)
	
	hdr[0] = MagicByte
	hdr[1] = VersionByte
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(e.SessionID)))
	offset := 4
	copy(hdr[offset:], e.SessionID)
	offset += len(e.SessionID)

	binary.BigEndian.PutUint64(hdr[offset:], e.Seq)
	offset += 8

	binary.BigEndian.PutUint16(hdr[offset:], uint16(len(e.TargetAddr)))
	offset += 2
	copy(hdr[offset:], e.TargetAddr)
	offset += len(e.TargetAddr)

	if e.Close {
		hdr[offset] = 1
	} else {
		hdr[offset] = 0
	}
	offset++

	binary.BigEndian.PutUint32(hdr[offset:], uint32(len(e.Payload)))
	offset += 4

	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(e.Payload) > 0 {
		_, err := w.Write(e.Payload)
		if err != nil {
			return err
		}
	}
	_, err := w.Write([]byte{e.BackendIdx})
	return err
}

// UnmarshalBinary deserializes the envelope from the custom Flow binary format.
// It returns the number of bytes read or an error.
func (e *Envelope) UnmarshalBinary(data []byte) (int, error) {
	if len(data) < 2 {
		return 0, io.ErrUnexpectedEOF
	}
	if data[0] != MagicByte {
		return 0, fmt.Errorf("invalid magic byte: expected 0x%X, got 0x%X", MagicByte, data[0])
	}
	if data[1] != VersionByte {
		return 0, fmt.Errorf("unsupported version: expected 0x%02X, got 0x%02X", VersionByte, data[1])
	}
	
	offset := 2
	if len(data) < offset+2 { return 0, io.ErrUnexpectedEOF }
	sidLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	
	if sidLen > MaxStringLen {
		return 0, fmt.Errorf("session ID too long: %d bytes (max %d)", sidLen, MaxStringLen)
	}
	if len(data) < offset+sidLen { return 0, io.ErrUnexpectedEOF }
	e.SessionID = string(data[offset : offset+sidLen])
	offset += sidLen
	
	if len(data) < offset+8 { return 0, io.ErrUnexpectedEOF }
	e.Seq = binary.BigEndian.Uint64(data[offset:])
	offset += 8
	
	if len(data) < offset+2 { return 0, io.ErrUnexpectedEOF }
	addrLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	
	if addrLen > MaxStringLen {
		return 0, fmt.Errorf("target address too long: %d bytes (max %d)", addrLen, MaxStringLen)
	}
	if len(data) < offset+addrLen { return 0, io.ErrUnexpectedEOF }
	e.TargetAddr = string(data[offset : offset+addrLen])
	offset += addrLen
	
	if len(data) < offset+1 { return 0, io.ErrUnexpectedEOF }
	e.Close = data[offset] == 1
	offset++
	
	if len(data) < offset+4 { return 0, io.ErrUnexpectedEOF }
	payloadLen := int(binary.BigEndian.Uint32(data[offset:]))
	offset += 4
	
	if payloadLen > MaxMessageSize {
		return 0, fmt.Errorf("payload too large: %d bytes (max %d)", payloadLen, MaxMessageSize)
	}
	if len(data) < offset+payloadLen { return 0, io.ErrUnexpectedEOF }
	e.Payload = make([]byte, payloadLen)
	copy(e.Payload, data[offset:offset+payloadLen])
	offset += payloadLen

	if offset+1 > len(data) {
		e.BackendIdx = 0
	} else {
		e.BackendIdx = data[offset]
	}
	offset++

	return offset, nil
}
// Decode reads an envelope from an io.Reader.
func (e *Envelope) Decode(r io.Reader) error {
	var magicBuf [1]byte
	if _, err := io.ReadFull(r, magicBuf[:]); err != nil {
		return err
	}
	if magicBuf[0] != MagicByte {
		return fmt.Errorf("invalid magic byte: 0x%X", magicBuf[0])
	}

	var verBuf [1]byte
	if _, err := io.ReadFull(r, verBuf[:]); err != nil {
		return err
	}
	if verBuf[0] != VersionByte {
		return fmt.Errorf("unsupported version: expected 0x%02X, got 0x%02X", VersionByte, verBuf[0])
	}

	var sidLenBuf [2]byte
	if _, err := io.ReadFull(r, sidLenBuf[:]); err != nil {
		return err
	}
	sidLen := int(binary.BigEndian.Uint16(sidLenBuf[:]))
	if sidLen > MaxStringLen {
		return fmt.Errorf("session ID too long: %d bytes (max %d)", sidLen, MaxStringLen)
	}
	sidBuf := make([]byte, sidLen)
	if _, err := io.ReadFull(r, sidBuf); err != nil {
		return err
	}
	e.SessionID = string(sidBuf)

	var seqBuf [8]byte
	if _, err := io.ReadFull(r, seqBuf[:]); err != nil {
		return err
	}
	e.Seq = binary.BigEndian.Uint64(seqBuf[:])

	var addrLenBuf [2]byte
	if _, err := io.ReadFull(r, addrLenBuf[:]); err != nil {
		return err
	}
	addrLen := int(binary.BigEndian.Uint16(addrLenBuf[:]))
	if addrLen > MaxStringLen {
		return fmt.Errorf("target address too long: %d bytes (max %d)", addrLen, MaxStringLen)
	}
	addrBuf := make([]byte, addrLen)
	if _, err := io.ReadFull(r, addrBuf); err != nil {
		return err
	}
	e.TargetAddr = string(addrBuf)

	var closeBuf [1]byte
	if _, err := io.ReadFull(r, closeBuf[:]); err != nil {
		return err
	}
	e.Close = closeBuf[0] == 1

	var payLenBuf [4]byte
	if _, err := io.ReadFull(r, payLenBuf[:]); err != nil {
		return err
	}
	payLen := binary.BigEndian.Uint32(payLenBuf[:])
	if int(payLen) > MaxMessageSize {
		return fmt.Errorf("packet too large: %d bytes (max %d)", payLen, MaxMessageSize)
	}
	if payLen > 0 {
		e.Payload = make([]byte, payLen)
		if _, err := io.ReadFull(r, e.Payload); err != nil {
			return err
		}
	} else {
		e.Payload = nil
	}

	var backendIdxBuf [1]byte
	if _, err := io.ReadFull(r, backendIdxBuf[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			e.BackendIdx = 0
			return nil
		}
		return err
	}
	e.BackendIdx = backendIdxBuf[0]
	return nil
}
