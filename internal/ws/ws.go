// Package ws is a minimal RFC 6455 websocket client (stdlib only).
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

const maxMessage = 64 << 20 // 64 MB (map images are a few MB)

type Conn struct {
	nc  net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// AcceptKey computes Sec-WebSocket-Accept for a key.
func AcceptKey(key string) string {
	h := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

// Dial connects to ws:// or wss:// URL.
func Dial(rawURL string, timeout time.Duration) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	var nc net.Conn
	if u.Scheme == "wss" {
		nc, err = tls.DialWithDialer(d, "tcp", host, &tls.Config{ServerName: u.Hostname()})
	} else {
		nc, err = d.Dial("tcp", host)
	}
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	path := u.RequestURI()
	nc.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nUser-Agent: RustWatch\r\n\r\n", path, u.Host, key)
	if _, err := io.WriteString(nc, req); err != nil {
		nc.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(nc, 64<<10)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		nc.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		nc.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s", resp.Status)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != AcceptKey(key) {
		nc.Close()
		return nil, errors.New("websocket handshake: bad accept key")
	}
	nc.SetDeadline(time.Time{})
	return &Conn{nc: nc, br: br}, nil
}

// NewServerConn wraps an already-upgraded server-side connection (used in tests).
func NewServerConn(nc net.Conn, br *bufio.Reader) *Conn { return &Conn{nc: nc, br: br} }

func (c *Conn) Close() error { return c.nc.Close() }

func (c *Conn) SetReadDeadline(t time.Time) error { return c.nc.SetReadDeadline(t) }

// WriteMessage writes a single (masked if client) frame.
func (c *Conn) WriteMessage(op byte, data []byte) error { return c.write(op, data, true) }

// WriteServer writes an unmasked frame (server side, tests).
func (c *Conn) WriteServer(op byte, data []byte) error { return c.write(op, data, false) }

func (c *Conn) write(op byte, data []byte, mask bool) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	hdr := []byte{0x80 | op}
	mbit := byte(0)
	if mask {
		mbit = 0x80
	}
	n := len(data)
	switch {
	case n < 126:
		hdr = append(hdr, mbit|byte(n))
	case n < 65536:
		hdr = append(hdr, mbit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, mbit|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	payload := data
	if mask {
		mk := make([]byte, 4)
		rand.Read(mk)
		hdr = append(hdr, mk...)
		payload = make([]byte, n)
		for i := range data {
			payload[i] = data[i] ^ mk[i%4]
		}
	}
	c.nc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := c.nc.Write(append(hdr, payload...)); err != nil {
		return err
	}
	return nil
}

// ReadMessage returns the next data message, answering pings and assembling fragments.
func (c *Conn) ReadMessage() (byte, []byte, error) {
	var msg []byte
	var msgOp byte
	for {
		b0, err := c.br.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		b1, err := c.br.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		fin := b0&0x80 != 0
		op := b0 & 0x0f
		masked := b1&0x80 != 0
		length := uint64(b1 & 0x7f)
		switch length {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			length = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			length = binary.BigEndian.Uint64(b[:])
		}
		if length > maxMessage || uint64(len(msg))+length > maxMessage {
			return 0, nil, errors.New("websocket: message too large")
		}
		var mk [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, mk[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mk[i%4]
			}
		}
		switch op {
		case OpPing:
			c.WriteMessage(OpPong, payload)
			continue
		case OpPong:
			continue
		case OpClose:
			c.WriteMessage(OpClose, nil)
			return OpClose, payload, io.EOF
		case OpContinuation:
			msg = append(msg, payload...)
		default:
			msgOp = op
			msg = payload
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}
