package transport

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

var ErrTooManyConns = fmt.Errorf("too many concurrent connections")

// VirtualConn acts as a bridge fulfilling the net.Conn interface for use by standard
// SOCKS5 libraries, but routing data covertly through the WebDAV Session.
type VirtualConn struct {
	session *Session
	engine  *Engine
	readBuf []byte
	mu      sync.Mutex
	onClose func()

	readDeadline  time.Time
	writeDeadline time.Time
	deadlineMu    sync.Mutex
	deadlineTimer *time.Timer // reusable timer for deadline enforcement

	closeOnce sync.Once     // prevents double-close deadlock
	readWake  chan struct{} // closed on Close() to unblock Read
}

func NewVirtualConnWithOnClose(s *Session, e *Engine, fn func()) *VirtualConn {
	return &VirtualConn{
		session:  s,
		engine:   e,
		onClose:  fn,
		readWake: make(chan struct{}),
	}
}

func (v *VirtualConn) Read(b []byte) (n int, err error) {
	for {
		v.mu.Lock()
		if len(v.readBuf) > 0 {
			n = copy(b, v.readBuf)
			v.readBuf = v.readBuf[n:]
			v.mu.Unlock()
			return n, nil
		}
		v.mu.Unlock()

		data, err := v.readRxChan()
		if err != nil {
			return 0, err
		}

		if len(data) > 0 {
			v.mu.Lock()
			n = copy(b, data)
			if n < len(data) {
				v.readBuf = data[n:]
			}
			v.mu.Unlock()
			return n, nil
		}

		// 0-byte heartbeat: wait for wake signal or timeout
		select {
		case <-v.readWake:
		case <-time.After(time.Millisecond):
		}

		v.session.mu.Lock()
		closed := v.session.closed
		v.session.mu.Unlock()
		if closed {
			return 0, io.EOF
		}
	}
}

func (v *VirtualConn) readRxChan() ([]byte, error) {
	v.deadlineMu.Lock()
	rd := v.readDeadline
	if rd.IsZero() {
		v.deadlineMu.Unlock()
		select {
		case data, ok := <-v.session.RxChan:
			if !ok {
				return nil, io.EOF
			}
			return data, nil
		case <-v.readWake:
			select {
			case data, ok := <-v.session.RxChan:
				if ok {
					return data, nil
				}
			default:
			}
			return nil, io.EOF
		}
	}

	d := time.Until(rd)
	if d <= 0 {
		v.deadlineMu.Unlock()
		return nil, os.ErrDeadlineExceeded
	}

	if v.deadlineTimer == nil {
		v.deadlineTimer = time.NewTimer(d)
	} else {
		v.deadlineTimer.Reset(d)
	}
	timer := v.deadlineTimer
	v.deadlineMu.Unlock()

	select {
	case data, ok := <-v.session.RxChan:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if !ok {
			return nil, io.EOF
		}
		return data, nil
	case <-timer.C:
		return nil, os.ErrDeadlineExceeded
	case <-v.readWake:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		select {
		case data, ok := <-v.session.RxChan:
			if ok {
				return data, nil
			}
		default:
		}
		return nil, io.EOF
	}
}

func (v *VirtualConn) Write(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}

	// Check write deadline
	v.deadlineMu.Lock()
	wd := v.writeDeadline
	v.deadlineMu.Unlock()
	if !wd.IsZero() && time.Now().After(wd) {
		return 0, os.ErrDeadlineExceeded
	}

	v.session.mu.Lock()
	closed := v.session.closed
	v.session.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	v.session.EnqueueTx(b)
	return len(b), nil
}

func (v *VirtualConn) Close() error {
	v.closeOnce.Do(func() {
		v.session.mu.Lock()
		v.session.closed = true
		v.session.mu.Unlock()
		v.session.wakeupTx()
		close(v.readWake)

		if v.onClose != nil {
			v.onClose()
		}
	})

	// Session data remains in engine for flushLoop to upload remaining
	// txBuf and send close envelope. Engine.flushAll handles removal.
	return nil
}

func (v *VirtualConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 65535}
}
func (v *VirtualConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 65535}
}
func (v *VirtualConn) SetDeadline(t time.Time) error {
	v.deadlineMu.Lock()
	v.readDeadline = t
	v.writeDeadline = t
	v.deadlineMu.Unlock()
	return nil
}
func (v *VirtualConn) SetReadDeadline(t time.Time) error {
	v.deadlineMu.Lock()
	v.readDeadline = t
	v.deadlineMu.Unlock()
	return nil
}
func (v *VirtualConn) SetWriteDeadline(t time.Time) error {
	v.deadlineMu.Lock()
	v.writeDeadline = t
	v.deadlineMu.Unlock()
	return nil
}
