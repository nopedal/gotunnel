// Package proto defines the wire protocol for tunnel control messages
// exchanged between the client and server over the yamux control stream.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message types for the control protocol.
const (
	TypeAuthRequest    uint8 = 0x01
	TypeAuthResponse   uint8 = 0x02
	TypeTunnelRequest  uint8 = 0x03
	TypeTunnelResponse uint8 = 0x04
	TypeHTTPRequest    uint8 = 0x10
	TypeHTTPResponse   uint8 = 0x11
	TypeStreamData     uint8 = 0x20
	TypeStreamClose    uint8 = 0x21
	TypePing           uint8 = 0x30
	TypePong           uint8 = 0x31
)

// MaxMessageSize is the upper bound on a single control message (16 MB).
const MaxMessageSize = 16 * 1024 * 1024

// Header is the 5-byte wire header: [type:1][length:4].
const HeaderSize = 5

// AuthRequest is sent by the client immediately after the yamux session opens.
type AuthRequest struct {
	Token   string `json:"token"`
	Version string `json:"version"`
}

// AuthResponse is the server's reply to an auth attempt.
type AuthResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// TunnelRequest asks the server to allocate a public endpoint.
type TunnelRequest struct {
	Subdomain string `json:"subdomain,omitempty"` // requested; empty = auto
	Mode      string `json:"mode"`                // "http" or "tcp"
	LocalPort int    `json:"local_port"`
}

// TunnelResponse returns the allocated endpoint.
type TunnelResponse struct {
	OK        bool   `json:"ok"`
	URL       string `json:"url,omitempty"`
	Subdomain string `json:"subdomain,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RequestMeta carries HTTP request metadata for logging on the client side.
type RequestMeta struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Host   string `json:"host"`
	Path   string `json:"path"`
}

// ResponseMeta carries HTTP response metadata for logging.
type ResponseMeta struct {
	ID         string `json:"id"`
	StatusCode int    `json:"status_code"`
	Duration   int64  `json:"duration_ms"`
}

// WriteMessage encodes a typed message to the writer.
func WriteMessage(w io.Writer, msgType uint8, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("message too large: %d bytes", len(data))
	}

	hdr := make([]byte, HeaderSize)
	hdr[0] = msgType
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(data)))

	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadMessage decodes a typed message from the reader.
func ReadMessage(r io.Reader) (uint8, []byte, error) {
	hdr := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, fmt.Errorf("read header: %w", err)
	}

	msgType := hdr[0]
	length := binary.BigEndian.Uint32(hdr[1:])

	if length > MaxMessageSize {
		return 0, nil, fmt.Errorf("message too large: %d bytes", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, fmt.Errorf("read payload: %w", err)
	}

	return msgType, data, nil
}

// Decode unmarshals a JSON payload into the target.
func Decode(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
