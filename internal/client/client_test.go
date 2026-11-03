package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// If the TLS handshake fails (a plaintext server, or an attacker interfering
// with the handshake), the client must give up rather than retry over plain
// TCP and hand its token to whoever is listening.
func TestConnectDoesNotFallBackToPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var (
		mu       sync.Mutex
		received []byte
		accepted int
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			first := accepted == 1
			mu.Unlock()

			go func() {
				defer conn.Close()
				if first {
					// Break the TLS handshake.
					return
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				data, _ := io.ReadAll(conn)
				mu.Lock()
				received = append(received, data...)
				mu.Unlock()
			}()
		}
	}()

	const token = "super-secret-token"
	c := New(&Config{Server: ln.Addr().String(), Token: token, Port: 3000, Mode: "http"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.connectOnce(ctx); err == nil {
		t.Fatal("connectOnce succeeded against a server that broke the TLS handshake")
	}

	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("client opened %d connections, want 1 (no plaintext retry)", accepted)
	}
	if bytes.Contains(received, []byte(token)) {
		t.Error("token was sent in plaintext")
	}
}
